package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/trick77/vark/internal/auth"
	"github.com/trick77/vark/internal/jobs"
	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/videos"
	"github.com/trick77/vark/internal/ytdlp"
)

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
		Availability:    "available",
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

// TestDownloads_requireAuth asserts every downloads route is behind
// requireAuth.
func TestDownloads_requireAuth(t *testing.T) {
	h := New(downloadsTestDeps(t, &fakeDownloadsRunner{}))

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/downloads", bytes.NewReader([]byte("{}"))),
		httptest.NewRequest(http.MethodGet, "/api/downloads", nil),
		httptest.NewRequest(http.MethodPost, "/api/downloads/1/cancel", nil),
		httptest.NewRequest(http.MethodGet, "/api/downloads/stream", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", req.Method, req.URL.Path, rec.Code)
		}
	}
}
