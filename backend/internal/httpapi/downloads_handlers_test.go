package httpapi

import (
	"bytes"
	"context"
	"database/sql"
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
	mu           sync.Mutex
	resumeCalls  int
	paused       bool
	lowDisk      bool
	cancelResult bool
	cancelCallN  int
}

func (f *fakeWorker) Cancel(int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCallN++
	return f.cancelResult
}
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

func (f *fakeWorker) cancelCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancelCallN
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

// downloadsTestDepsDB is downloadsTestDeps but also returns the backing
// *sql.DB, so a test can drop/trigger-guard a table to force a store-level
// error out of an otherwise normal handler flow (Jobs/Videos are concrete
// *jobs.Store/*videos.Store, not interfaces, so this is the only way to
// exercise their error branches without touching source).
func downloadsTestDepsDB(t *testing.T, runner DownloadsRunner) (Deps, *sql.DB) {
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
	}, db
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

// TestDownloads_postNotConfigured asserts POST /api/downloads fails closed
// with 503 when any of jobs/videos/runner is unwired, rather than panicking
// on a nil dependency.
func TestDownloads_postNotConfigured(t *testing.T) {
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	deps := Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
		// Jobs, Videos, Runner all deliberately left nil.
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://youtu.be/dQw4w9WgXcQ")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/downloads (not configured) status = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
}

// TestDownloads_postMissingURL_400 asserts an empty/missing url is rejected
// before any canonicalization or metadata fetch is attempted.
func TestDownloads_postMissingURL_400(t *testing.T) {
	runner := &fakeDownloadsRunner{err: context.DeadlineExceeded} // must never be called
	h := New(downloadsTestDeps(t, runner))
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "   ")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/downloads (blank url) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if runner.calls != 0 {
		t.Fatalf("Metadata calls = %d, want 0", runner.calls)
	}
}

// TestDownloads_postUnrecognizedURL_400 asserts a url Canonicalize can't
// parse into a known YouTube shape is rejected with 400.
func TestDownloads_postUnrecognizedURL_400(t *testing.T) {
	runner := &fakeDownloadsRunner{err: context.DeadlineExceeded} // must never be called
	h := New(downloadsTestDeps(t, runner))
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://example.com/not-youtube")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/downloads (unrecognized url) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if runner.calls != 0 {
		t.Fatalf("Metadata calls = %d, want 0 (must be rejected before metadata fetch)", runner.calls)
	}
}

// TestDownloads_postLive_400 asserts a /live/ url is rejected before ever
// calling Metadata: live streams/premieres aren't supported.
func TestDownloads_postLive_400(t *testing.T) {
	runner := &fakeDownloadsRunner{err: context.DeadlineExceeded} // must never be called
	h := New(downloadsTestDeps(t, runner))
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://www.youtube.com/live/dQw4w9WgXcQ")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/downloads (live) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
	if runner.calls != 0 {
		t.Fatalf("Metadata calls = %d, want 0 (live must be rejected before metadata fetch)", runner.calls)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if !strings.Contains(got["error"], "Live videos") {
		t.Fatalf("error = %q, want the live-rejection message", got["error"])
	}
}

// TestDownloads_postMetadataFetchFailed_502 asserts a Metadata failure that
// is NOT ytdlp.ErrNoCookie surfaces as a 502, not a 500 or a silent failure.
func TestDownloads_postMetadataFetchFailed_502(t *testing.T) {
	runner := &fakeDownloadsRunner{err: context.DeadlineExceeded}
	h := New(downloadsTestDeps(t, runner))
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://youtu.be/dQw4w9WgXcQ")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("POST /api/downloads (metadata failure) status = %d, want 502, body = %s", rec.Code, rec.Body.String())
	}
}

// TestDownloads_postMetaWithoutID_usesCanonicalID asserts that when the
// runner's Meta comes back with an empty ID (yt-dlp didn't echo one), the
// handler falls back to the id Canonicalize extracted from the url, rather
// than writing an empty-string video id.
func TestDownloads_postMetaWithoutID_usesCanonicalID(t *testing.T) {
	runner := &fakeDownloadsRunner{meta: &ytdlp.Meta{Title: "No ID In Metadata"}}
	deps := downloadsTestDeps(t, runner)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://youtu.be/dQw4w9WgXcQ")
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/downloads status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got["video_id"] != "dQw4w9WgXcQ" {
		t.Fatalf("video_id = %v, want the canonical id from the url", got["video_id"])
	}
	video, err := deps.Videos.Get("dQw4w9WgXcQ")
	if err != nil || video == nil {
		t.Fatalf("get video: %v (video=%v)", err, video)
	}
}

// TestDownloads_postUpsertStoreError_500 covers the s.videos.Upsert error
// branch: a broken videos table must surface as a generic 500.
func TestDownloads_postUpsertStoreError_500(t *testing.T) {
	runner := &fakeDownloadsRunner{meta: &ytdlp.Meta{ID: "dQw4w9WgXcQ", Title: "t"}}
	deps, db := downloadsTestDepsDB(t, runner)
	if _, err := db.Exec(`DROP TABLE videos`); err != nil {
		t.Fatalf("drop videos table: %v", err)
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://youtu.be/dQw4w9WgXcQ")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /api/downloads (upsert store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestDownloads_postSetStatusStoreError_500 covers the s.videos.SetStatus
// error branch: Upsert succeeds (it never touches the status column) but a
// trigger blocks the subsequent status UPDATE, which must surface as 500.
func TestDownloads_postSetStatusStoreError_500(t *testing.T) {
	runner := &fakeDownloadsRunner{meta: &ytdlp.Meta{ID: "dQw4w9WgXcQ", Title: "t"}}
	deps, db := downloadsTestDepsDB(t, runner)
	if _, err := db.Exec(`CREATE TRIGGER block_status_update BEFORE UPDATE OF status ON videos BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://youtu.be/dQw4w9WgXcQ")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /api/downloads (set-status store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestDownloads_postEnqueueStoreError_500 covers the s.jobs.Enqueue error
// branch: the video row is saved successfully but the queue table is
// broken, which must surface as 500 (never a silent partial success).
func TestDownloads_postEnqueueStoreError_500(t *testing.T) {
	runner := &fakeDownloadsRunner{meta: &ytdlp.Meta{ID: "dQw4w9WgXcQ", Title: "t"}}
	deps, db := downloadsTestDepsDB(t, runner)
	if _, err := db.Exec(`DROP TABLE download_jobs`); err != nil {
		t.Fatalf("drop download_jobs table: %v", err)
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := postDownload(t, h, sessionCookie, "https://youtu.be/dQw4w9WgXcQ")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /api/downloads (enqueue store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
	// The video row itself must still have been saved, even though the
	// enqueue that would have made it downloadable failed.
	video, err := deps.Videos.Get("dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if video == nil {
		t.Fatal("expected the video row to exist despite the enqueue failure")
	}
}

// TestDownloads_listNoJobsConfigured_emptyArray asserts GET /api/downloads
// returns an empty array (not 503/500) when no Jobs store is wired.
func TestDownloads_listNoJobsConfigured_emptyArray(t *testing.T) {
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	deps := Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := doRequest(t, h, sessionCookie, http.MethodGet, "/api/downloads")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/downloads (no jobs store) status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("GET /api/downloads (no jobs store) body = %q, want []", got)
	}
}

// TestDownloads_listStoreError_500 covers the s.jobs.List error branch.
func TestDownloads_listStoreError_500(t *testing.T) {
	deps, db := downloadsTestDepsDB(t, &fakeDownloadsRunner{})
	if _, err := db.Exec(`DROP TABLE download_jobs`); err != nil {
		t.Fatalf("drop download_jobs table: %v", err)
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := doRequest(t, h, sessionCookie, http.MethodGet, "/api/downloads")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/downloads (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestYoutubePause_settingsNotConfigured_503 covers the s.settings == nil
// guard on both the pause and resume routes.
func TestYoutubePause_settingsNotConfigured_503(t *testing.T) {
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	deps := Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	for _, path := range []string{"/api/youtube/pause", "/api/youtube/resume"} {
		rec := doRequest(t, h, sessionCookie, http.MethodPost, path)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("POST %s (settings not configured) status = %d, want 503, body = %s", path, rec.Code, rec.Body.String())
		}
	}
}

// TestYoutubePause_storeError_500 covers the SetYoutubePaused error branch
// on both the pause and resume routes, using a flakyDBTX-backed settings
// store (see settings_handlers_test.go) to force ExecContext to fail.
func TestYoutubePause_storeError_500(t *testing.T) {
	for _, path := range []string{"/api/youtube/pause", "/api/youtube/resume"} {
		t.Run(path, func(t *testing.T) {
			deps, flaky := testDepsFlakySettings(t)
			flaky.failExec = true
			deps.Jobs = jobs.New(openTestDB(t))
			deps.Videos = videos.New(openTestDB(t))
			deps.Runner = &fakeDownloadsRunner{}
			h := New(deps)
			sessionCookie := loginAndGetCookie(t, h)

			rec := doRequest(t, h, sessionCookie, http.MethodPost, path)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("POST %s (store error) status = %d, want 500, body = %s", path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestDownloads_cancelInvalidID_400 asserts a non-numeric job id is rejected
// with 400 before any store/worker lookup.
func TestDownloads_cancelInvalidID_400(t *testing.T) {
	h := New(downloadsTestDeps(t, &fakeDownloadsRunner{}))
	sessionCookie := loginAndGetCookie(t, h)

	rec := doRequest(t, h, sessionCookie, http.MethodPost, "/api/downloads/not-a-number/cancel")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST cancel (invalid id) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestDownloads_cancelViaWorker asserts that when a worker is wired, it owns
// the cancel decision (rather than the store fallback), for both a
// successful cancel and an unknown job id.
func TestDownloads_cancelViaWorker(t *testing.T) {
	deps := downloadsTestDeps(t, &fakeDownloadsRunner{})
	fw := &fakeWorker{cancelResult: true}
	deps.Worker = fw
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := doRequest(t, h, sessionCookie, http.MethodPost, "/api/downloads/42/cancel")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST cancel (worker cancels) status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if fw.cancelCalls() != 1 {
		t.Fatalf("worker.Cancel calls = %d, want 1", fw.cancelCalls())
	}

	fw.cancelResult = false
	rec = doRequest(t, h, sessionCookie, http.MethodPost, "/api/downloads/43/cancel")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST cancel (worker refuses) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestDownloads_cancelStoreError_500 covers the jobs.Cancel error branch of
// the store-fallback path (no worker wired).
func TestDownloads_cancelStoreError_500(t *testing.T) {
	deps, db := downloadsTestDepsDB(t, &fakeDownloadsRunner{})
	if _, err := db.Exec(`DROP TABLE download_jobs`); err != nil {
		t.Fatalf("drop download_jobs table: %v", err)
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := doRequest(t, h, sessionCookie, http.MethodPost, "/api/downloads/1/cancel")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST cancel (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestDownloads_cancelNotConfigured_503 covers the "neither worker nor jobs
// store wired" default branch.
func TestDownloads_cancelNotConfigured_503(t *testing.T) {
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	deps := Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rec := doRequest(t, h, sessionCookie, http.MethodPost, "/api/downloads/1/cancel")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST cancel (not configured) status = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
}

// TestDownloadsStream_noHubConfigured_503 covers the s.sseHub == nil guard.
func TestDownloadsStream_noHubConfigured_503(t *testing.T) {
	h := New(downloadsTestDeps(t, &fakeDownloadsRunner{}))
	sessionCookie := loginAndGetCookie(t, h)

	rec := doRequest(t, h, sessionCookie, http.MethodGet, "/api/downloads/stream")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/downloads/stream (no hub) status = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
}

// TestDownloadsStream_clientDisconnectReturnsPromptly asserts the SSE stream
// handler returns promptly when the CLIENT disconnects (the reverse of
// TestDownloadsStream_hubCloseReturnsPromptly, which disconnects the hub).
// This exercises the r.Context().Done() case of the handler's select loop.
func TestDownloadsStream_clientDisconnectReturnsPromptly(t *testing.T) {
	deps := downloadsTestDeps(t, &fakeDownloadsRunner{})
	hub := sse.NewHub()
	deps.SSEHub = hub
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/downloads/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.AddCookie(sessionCookie)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}

	readErr := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		readErr <- err
	}()

	// Give the handler a moment to reach its select loop before the client
	// disconnects, so this exercises r.Context().Done() rather than racing
	// Subscribe.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-readErr:
		// The client-initiated cancel tore the connection down; the server
		// handler's r.Context().Done() case fired and it returned.
	case <-time.After(2 * time.Second):
		t.Fatal("client read did not unblock within 2s of context cancel")
	}
	resp.Body.Close()

	// The hub must have no lingering subscriber: publishing now must not
	// find (and thus not deliver to) the disconnected client's channel.
	// There is no direct subscriber-count accessor, so this is asserted
	// indirectly by confirming Publish after disconnect doesn't block or
	// panic — a leaked, never-unsubscribed channel would still be safe here
	// since Publish is non-blocking, so this is a smoke check that the
	// handler path completed rather than a strict leak assertion.
	hub.Publish("noop", "{}")
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
