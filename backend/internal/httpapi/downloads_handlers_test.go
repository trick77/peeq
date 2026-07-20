package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/sse"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// fakeWorker is a DownloadsWorker whose Resume calls are counted and whose
// paused/low-disk state is fixed by the test, so handler wiring (finding 1)
// and the status endpoint (finding 3) can be exercised without a real worker
// goroutine.
type fakeWorker struct {
	mu          sync.Mutex
	resumeCalls int
	paused      bool
	lowDisk     bool
}

func (f *fakeWorker) Cancel(int64) bool { return false }
func (f *fakeWorker) Resume() {
	f.mu.Lock()
	f.resumeCalls++
	f.mu.Unlock()
}
func (f *fakeWorker) Paused() bool  { return f.paused }
func (f *fakeWorker) LowDisk() bool { return f.lowDisk }

func (f *fakeWorker) resumes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resumeCalls
}

// TestDownloads_statusReportsPausedAndLowDisk asserts GET /api/downloads/status
// surfaces the worker's paused/low-disk flags (finding 3), and reports the
// not-stalled default when no worker is wired.
func TestDownloads_statusReportsPausedAndLowDisk(t *testing.T) {
	getStatus := func(t *testing.T, h http.Handler, c *http.Cookie) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/downloads/status", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/downloads/status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal status: %v", err)
		}
		return got
	}

	// Worker paused + low on disk: both flags surface as true.
	deps := downloadsTestDeps(t, &fakeDownloadsRunner{})
	deps.Worker = &fakeWorker{paused: true, lowDisk: true}
	h := New(deps)
	got := getStatus(t, h, loginAndGetCookie(t, h))
	if got["paused"] != true || got["low_disk"] != true {
		t.Fatalf("status = %v, want paused=true low_disk=true", got)
	}

	// No worker wired: not-stalled default, 200 (not 503).
	depsNil := downloadsTestDeps(t, &fakeDownloadsRunner{})
	hNil := New(depsNil)
	gotNil := getStatus(t, hNil, loginAndGetCookie(t, hNil))
	if gotNil["paused"] != false || gotNil["low_disk"] != false {
		t.Fatalf("status (no worker) = %v, want paused=false low_disk=false", gotNil)
	}
}

// fakeDownloadsRunner is a DownloadsRunner whose Metadata behavior is
// scripted per test, so these tests never shell out to yt-dlp. calls counts
// invocations so tests can assert Metadata was never reached (e.g. a
// playlist url must be rejected before it gets anywhere near the runner).
type fakeDownloadsRunner struct {
	meta  *ytdlp.Meta
	err   error
	calls int
}

func (f *fakeDownloadsRunner) Metadata(ctx context.Context, url string) (*ytdlp.Meta, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.meta, nil
}

// downloadsTestDeps builds Deps wired for the downloads API: dev auth, plus
// jobs/videos stores and the given fake runner sharing one test database.
func downloadsTestDeps(t *testing.T, runner DownloadsRunner) Deps {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	return Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		Jobs:           jobs.New(db),
		Videos:         videos.New(db),
		Runner:         runner,
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
}

func postDownload(t *testing.T, h http.Handler, sessionCookie *http.Cookie, url string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"url": url})
	req := httptest.NewRequest(http.MethodPost, "/api/downloads", bytes.NewReader(body))
	req.AddCookie(sessionCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestDownloads_postCanonicalizesAndEnqueues is the core Task 10 flow: a
// pasted url with playlist noise on it is canonicalized down to the bare
// video watch url before it ever reaches videos/jobs, and the resulting
// video + job rows use the canonical video id, never the raw pasted url.
func TestDownloads_postCanonicalizesAndEnqueues(t *testing.T) {
	runner := &fakeDownloadsRunner{meta: &ytdlp.Meta{
		ID:              "dQw4w9WgXcQ",
		Title:           "Never Gonna Give You Up",
		ChannelID:       "UCuAXFkgsw1L7xaCfnd5JJOw",
		Channel:         "Rick Astley",
		DurationSeconds: 212,
		PublishedAt:     "2009-10-25",
		Availability:    "public",
	}}
	deps := downloadsTestDeps(t, runner)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://youtu.be/dQw4w9WgXcQ?list=PLsomePlaylist")
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/downloads status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if runner.calls != 1 {
		t.Fatalf("Metadata calls = %d, want 1", runner.calls)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got["video_id"] != "dQw4w9WgXcQ" {
		t.Fatalf("response video_id = %v, want canonical video id", got["video_id"])
	}

	video, err := deps.Videos.Get("dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if video == nil {
		t.Fatal("expected a videos row for the canonical video id, got none")
	}
	if video.Status != "queued" {
		t.Fatalf("video status = %q, want %q", video.Status, "queued")
	}
	if video.URL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Fatalf("video url = %q, want the canonical watch url (no playlist param)", video.URL)
	}
	// Regression test: yt-dlp reports availability as "public" for a normal
	// video, which is NOT a valid videos.availability value (the DB CHECK
	// constraint only allows available/deleted/private/geo/unknown). The
	// handler must normalize "public" to "available" before writing it, or
	// the Upsert below would have already failed with a 500 above.
	if video.Availability != "available" {
		t.Fatalf("video availability = %q, want %q (normalized from yt-dlp's \"public\")", video.Availability, "available")
	}

	allJobs, err := deps.Jobs.List()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(allJobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(allJobs))
	}
	job := allJobs[0]
	if job.VideoID != "dQw4w9WgXcQ" {
		t.Fatalf("job video_id = %q, want canonical video id", job.VideoID)
	}
	if job.State != "pending" {
		t.Fatalf("job state = %q, want %q", job.State, "pending")
	}
	if job.Priority != 10 {
		t.Fatalf("job priority = %d, want 10", job.Priority)
	}
}

// TestDownloads_postNoCookie_409 asserts the cookie gate is surfaced to the
// caller, not swallowed: a Metadata call that fails with ErrNoCookie must
// produce a 409, never a silent failure or a 500.
func TestDownloads_postNoCookie_409(t *testing.T) {
	runner := &fakeDownloadsRunner{err: ytdlp.ErrNoCookie}
	h := New(downloadsTestDeps(t, runner))
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://youtu.be/dQw4w9WgXcQ")
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /api/downloads (no cookie) status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if got["error"] != "cookie required" {
		t.Fatalf("error = %q, want %q", got["error"], "cookie required")
	}
}

// TestDownloads_postPlaylist_400 asserts a playlist url is rejected before
// enqueueing (and before ever calling Metadata).
func TestDownloads_postPlaylist_400(t *testing.T) {
	runner := &fakeDownloadsRunner{err: context.DeadlineExceeded} // must never be called
	h := New(downloadsTestDeps(t, runner))
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://www.youtube.com/playlist?list=PLsomePlaylist")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/downloads (playlist) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if runner.calls != 0 {
		t.Fatalf("Metadata calls = %d, want 0 (playlist must be rejected before metadata fetch)", runner.calls)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if got["error"] != "Paste a single video link, not a playlist" {
		t.Fatalf("error = %q, want the playlist rejection message", got["error"])
	}
}

// TestDownloads_postChannel_400 asserts a channel url is rejected before
// enqueueing (and before ever calling Metadata) — channels are added under
// the Channels feature, not through the single-video downloads endpoint.
func TestDownloads_postChannel_400(t *testing.T) {
	runner := &fakeDownloadsRunner{err: context.DeadlineExceeded} // must never be called
	h := New(downloadsTestDeps(t, runner))
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://www.youtube.com/@SomeHandle")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/downloads (channel) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if runner.calls != 0 {
		t.Fatalf("Metadata calls = %d, want 0 (channel must be rejected before metadata fetch)", runner.calls)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if got["error"] != "That's a channel link — add it under Channels, not here" {
		t.Fatalf("error = %q, want the channel rejection message", got["error"])
	}
}

// TestDownloads_cancelMarksCanceled asserts POST /api/downloads/{id}/cancel
// moves a pending job to canceled.
func TestDownloads_cancelMarksCanceled(t *testing.T) {
	runner := &fakeDownloadsRunner{meta: &ytdlp.Meta{ID: "dQw4w9WgXcQ", Title: "t"}}
	deps := downloadsTestDeps(t, runner)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	postRec := postDownload(t, h, sessionCookie, "https://youtu.be/dQw4w9WgXcQ")
	if postRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/downloads status = %d, body = %s", postRec.Code, postRec.Body.String())
	}
	var posted map[string]any
	if err := json.Unmarshal(postRec.Body.Bytes(), &posted); err != nil {
		t.Fatalf("unmarshal post response: %v", err)
	}
	jobID := int64(posted["job_id"].(float64))

	cancelReq := httptest.NewRequest(http.MethodPost, "/api/downloads/"+strconv.FormatInt(jobID, 10)+"/cancel", nil)
	cancelReq.AddCookie(sessionCookie)
	cancelRec := httptest.NewRecorder()
	h.ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("POST /api/downloads/{id}/cancel status = %d, body = %s", cancelRec.Code, cancelRec.Body.String())
	}

	allJobs, err := deps.Jobs.List()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(allJobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(allJobs))
	}
	if allJobs[0].State != "canceled" {
		t.Fatalf("job state = %q, want %q", allJobs[0].State, "canceled")
	}
}

// TestDownloads_cancelUnknownJob_404 asserts canceling a job id that was
// never enqueued (and so is neither pending nor running) returns 404, not a
// false-positive 200 — this exercises the store-fallback path (no worker
// wired, matching downloadsTestDeps).
func TestDownloads_cancelUnknownJob_404(t *testing.T) {
	h := New(downloadsTestDeps(t, &fakeDownloadsRunner{}))
	sessionCookie := loginAndGetCookie(t, h)

	cancelReq := httptest.NewRequest(http.MethodPost, "/api/downloads/999999/cancel", nil)
	cancelReq.AddCookie(sessionCookie)
	cancelRec := httptest.NewRecorder()
	h.ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusNotFound {
		t.Fatalf("POST /api/downloads/999999/cancel status = %d, want 404, body = %s", cancelRec.Code, cancelRec.Body.String())
	}
}

// TestDownloadsStream_hubCloseReturnsPromptly asserts the SSE stream
// handler returns promptly once the Hub is closed, even while a client
// stays connected. This is the fix for graceful shutdown: http.Server.
// Shutdown does not cancel in-flight request contexts, so before this fix
// an open stream (blocked on r.Context().Done(), which a connected client
// never triggers) would make Shutdown burn its full timeout. Uses a real
// httptest.Server (not a plain recorder) so the client observes the
// connection actually being torn down when the handler returns.
func TestDownloadsStream_hubCloseReturnsPromptly(t *testing.T) {
	deps := downloadsTestDeps(t, &fakeDownloadsRunner{})
	hub := sse.NewHub()
	deps.SSEHub = hub
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/downloads/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.AddCookie(sessionCookie)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}

	streamDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(streamDone)
	}()

	// Give the handler a moment to reach its subscribe/select loop before
	// closing the hub, so the close genuinely interrupts an in-progress
	// receive rather than racing Subscribe.
	time.Sleep(50 * time.Millisecond)
	hub.Close()

	select {
	case <-streamDone:
		// Handler returned and the server closed the connection — exactly
		// what lets srv.Shutdown finish fast instead of blocking on this
		// client for its full 10s timeout.
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not return within 2s of hub.Close(); would block graceful shutdown")
	}
}

// TestDownloads_listReturnsQueue asserts GET /api/downloads returns the
// enqueued job.
func TestDownloads_listReturnsQueue(t *testing.T) {
	runner := &fakeDownloadsRunner{meta: &ytdlp.Meta{ID: "dQw4w9WgXcQ", Title: "Some Title"}}
	h := New(downloadsTestDeps(t, runner))
	sessionCookie := loginAndGetCookie(t, h)

	if rec := postDownload(t, h, sessionCookie, "https://youtu.be/dQw4w9WgXcQ"); rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/downloads status = %d, body = %s", rec.Code, rec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/downloads", nil)
	listReq.AddCookie(sessionCookie)
	listRec := httptest.NewRecorder()
	h.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/downloads status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(list) = %d, want 1", len(got))
	}
	if got[0]["video_id"] != "dQw4w9WgXcQ" {
		t.Fatalf("list[0].video_id = %v, want canonical video id", got[0]["video_id"])
	}
	if got[0]["title"] != "Some Title" {
		t.Fatalf("list[0].title = %v, want joined video title", got[0]["title"])
	}
}

// doRequest performs an authenticated request against h and returns the
// recorded response.
func doRequest(t *testing.T, h http.Handler, sessionCookie *http.Cookie, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestYoutubePauseResume is the core Task 10 flow: POST /api/youtube/pause
// engages the kill-switch (surfaced immediately on the status poll), and
// POST /api/youtube/resume clears it and resets the shared failure monitor
// via the injected OnResumeYoutube callback exactly once.
func TestYoutubePauseResume(t *testing.T) {
	resetCalls := 0
	deps := downloadsTestDeps(t, &fakeDownloadsRunner{})
	deps.OnResumeYoutube = func() { resetCalls++ }
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	if rec := doRequest(t, h, sessionCookie, http.MethodPost, "/api/youtube/pause"); rec.Code != http.StatusAccepted {
		t.Fatalf("pause status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}
	if paused, _ := deps.Settings.YoutubePaused(context.Background()); !paused {
		t.Fatal("not paused after POST /api/youtube/pause")
	}

	// Status reflects it.
	rec := doRequest(t, h, sessionCookie, http.MethodGet, "/api/downloads/status")
	if !strings.Contains(rec.Body.String(), `"youtube_paused":true`) {
		t.Errorf("status body = %s", rec.Body.String())
	}

	// Resume clears it AND resets the failure monitor.
	if rec := doRequest(t, h, sessionCookie, http.MethodPost, "/api/youtube/resume"); rec.Code != http.StatusAccepted {
		t.Fatalf("resume status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}
	if paused, _ := deps.Settings.YoutubePaused(context.Background()); paused {
		t.Fatal("still paused after resume")
	}
	if resetCalls != 1 {
		t.Fatalf("OnResumeYoutube called %d times, want 1", resetCalls)
	}
}

// TestDownloads_requireAuth asserts every downloads route is behind
// requireAuth.
func TestDownloads_requireAuth(t *testing.T) {
	h := New(downloadsTestDeps(t, &fakeDownloadsRunner{}))

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/downloads", bytes.NewReader([]byte("{}"))),
		httptest.NewRequest(http.MethodGet, "/api/downloads", nil),
		httptest.NewRequest(http.MethodPost, "/api/downloads/1/cancel", nil),
		httptest.NewRequest(http.MethodGet, "/api/downloads/stream", nil),
		httptest.NewRequest(http.MethodPost, "/api/youtube/pause", nil),
		httptest.NewRequest(http.MethodPost, "/api/youtube/resume", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", req.Method, req.URL.Path, rec.Code)
		}
	}
}
