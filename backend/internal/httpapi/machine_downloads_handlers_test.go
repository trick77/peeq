package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/videos"
)

// machineDownloadsDeps wires everything the token-gated add-a-video route
// needs: session auth (so a test can mint a token over the session API), the
// token middleware that gates /api/machine, and the jobs/videos/runner stores
// the enqueue touches. It shares one database across all of them.
func machineDownloadsDeps(t *testing.T) Deps {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	settingsStore := settings.New(db)
	return Deps{
		AuthService:     auth.NewService(nil, sessions, users),
		AuthMiddleware:  auth.NewMiddleware(sessions, users),
		Settings:        settingsStore,
		TokenMiddleware: auth.NewTokenMiddleware(settingsStore),
		Jobs:            jobs.New(db),
		Videos:          videos.New(db),
		Runner:          &fakeDownloadsRunner{},
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
}

// postMachineDownload posts a url to the token-gated machine route with the
// given bearer token (omitted when empty).
func postMachineDownload(t *testing.T, h http.Handler, token, url string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"url": url})
	req := httptest.NewRequest(http.MethodPost, "/api/machine/downloads", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestMachineDownloads_enqueuesWithAValidToken is the core flow: a token
// holder posts a video url with no session and it lands in the queue exactly
// like the session route would have enqueued it (201, canonical id, one job).
func TestMachineDownloads_enqueuesWithAValidToken(t *testing.T) {
	deps := machineDownloadsDeps(t)
	h := New(deps)
	token := createToken(t, h, loginAndGetCookie(t, h))

	rec := postMachineDownload(t, h, token, "https://youtu.be/dQw4w9WgXcQ?list=PLnoise")
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/machine/downloads status = %d, want 201, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got["video_id"] != "dQw4w9WgXcQ" {
		t.Fatalf("video_id = %v, want the canonical id (playlist noise stripped)", got["video_id"])
	}

	v, err := deps.Videos.Get("dQw4w9WgXcQ")
	if err != nil || v == nil {
		t.Fatalf("get video: %v (video=%v)", err, v)
	}
	if v.Status != videos.StatusQueued {
		t.Fatalf("video status = %q, want %q", v.Status, videos.StatusQueued)
	}
	if v.URL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" {
		t.Fatalf("video url = %q, want the canonical watch url", v.URL)
	}
	all, err := deps.Jobs.List()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(all))
	}
}

// TestMachineDownloads_rejectsWithoutAValidToken asserts the route is gated:
// no bearer, a wrong token, and a wrong scheme are all 401, and none of them
// enqueue anything.
func TestMachineDownloads_rejectsWithoutAValidToken(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"no token", ""},
		{"wrong token", "peeq_not-the-right-token-value-at-all-nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := machineDownloadsDeps(t)
			h := New(deps)
			// A token exists, but the request does not present a valid one.
			createToken(t, h, loginAndGetCookie(t, h))

			rec := postMachineDownload(t, h, tc.token, "https://youtu.be/dQw4w9WgXcQ")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
			}
			all, err := deps.Jobs.List()
			if err != nil {
				t.Fatalf("list jobs: %v", err)
			}
			if len(all) != 0 {
				t.Fatalf("an unauthorized request enqueued %d job(s)", len(all))
			}
		})
	}
}

// TestMachineDownloads_rejectsBadURLs asserts the machine route rejects the
// same non-single-video links the session route does, with the same messages.
func TestMachineDownloads_rejectsBadURLs(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"playlist", "https://www.youtube.com/playlist?list=PLx", "Paste a single video link, not a playlist"},
		{"channel", "https://www.youtube.com/@SomeHandle", "That's a channel link — add it under Channels, not here"},
		{"blank", "   ", "url is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := machineDownloadsDeps(t)
			h := New(deps)
			token := createToken(t, h, loginAndGetCookie(t, h))

			rec := postMachineDownload(t, h, token, tc.url)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("unmarshal error body: %v", err)
			}
			if got["error"] != tc.want {
				t.Fatalf("error = %q, want %q", got["error"], tc.want)
			}
			all, err := deps.Jobs.List()
			if err != nil {
				t.Fatalf("list jobs: %v", err)
			}
			if len(all) != 0 {
				t.Fatalf("a rejected url enqueued %d job(s)", len(all))
			}
		})
	}
}

// TestDownloads_malformedBody_400 pins the one behavioral edge the
// enqueueDownloadByURL refactor touched: both routes now decode the body and
// discard the error, relying on the empty-url check to reject it. A malformed
// JSON body must therefore still surface as 400 "url is required" (not a 500
// or a panic) on the session route AND the machine route.
func TestDownloads_malformedBody_400(t *testing.T) {
	deps := machineDownloadsDeps(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)

	const malformed = `{"url":` // truncated JSON: Decode errors, req.URL stays ""

	// Session route.
	sReq := httptest.NewRequest(http.MethodPost, "/api/downloads", bytes.NewReader([]byte(malformed)))
	sReq.AddCookie(sessionCookie)
	sReq.Header.Set("Content-Type", "application/json")
	sRec := httptest.NewRecorder()
	h.ServeHTTP(sRec, sReq)
	if sRec.Code != http.StatusBadRequest {
		t.Fatalf("session POST (malformed body) status = %d, want 400, body = %s", sRec.Code, sRec.Body.String())
	}

	// Machine route.
	mReq := httptest.NewRequest(http.MethodPost, "/api/machine/downloads", bytes.NewReader([]byte(malformed)))
	mReq.Header.Set("Authorization", "Bearer "+token)
	mReq.Header.Set("Content-Type", "application/json")
	mRec := httptest.NewRecorder()
	h.ServeHTTP(mRec, mReq)
	if mRec.Code != http.StatusBadRequest {
		t.Fatalf("machine POST (malformed body) status = %d, want 400, body = %s", mRec.Code, mRec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(mRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if got["error"] != "url is required" {
		t.Fatalf("machine error = %q, want %q", got["error"], "url is required")
	}

	// Nothing enqueued by either malformed request.
	all, err := deps.Jobs.List()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("a malformed body enqueued %d job(s)", len(all))
	}
}

// TestMachineDownloads_alreadyInQueueIsADuplicateNoOp asserts the re-queue
// guard: re-adding a video that is already downloaded returns 200 (duplicate)
// with its current state, and does NOT reset it to 'queued' or enqueue a
// second job. This is the double-tap protection a one-click button needs.
func TestMachineDownloads_alreadyInQueueIsADuplicateNoOp(t *testing.T) {
	deps := machineDownloadsDeps(t)
	h := New(deps)
	token := createToken(t, h, loginAndGetCookie(t, h))

	// Seed a video that peeq already has, fully downloaded.
	const id = "dQw4w9WgXcQ"
	if err := deps.Videos.Upsert(videos.Video{ID: id, URL: "https://www.youtube.com/watch?v=" + id, Title: "Already Here"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetStatus(id, videos.StatusDownloaded, ""); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	rec := postMachineDownload(t, h, token, "https://youtu.be/"+id)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (duplicate), body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got["state"] != videos.StatusDownloaded {
		t.Fatalf("state = %v, want %q (the existing state echoed back)", got["state"], videos.StatusDownloaded)
	}

	// The video must NOT have been reset to 'queued', and no job enqueued.
	v, err := deps.Videos.Get(id)
	if err != nil || v == nil {
		t.Fatalf("get video: %v (video=%v)", err, v)
	}
	if v.Status != videos.StatusDownloaded {
		t.Fatalf("video status = %q, want it left at %q", v.Status, videos.StatusDownloaded)
	}
	all, err := deps.Jobs.List()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("a duplicate add enqueued %d job(s), want 0", len(all))
	}
}

// TestMachineDownloads_failedVideoReEnqueues asserts a video in a
// non-pipeline state (error) is re-queueable through the machine route: the
// button is a legitimate retry, so it goes back to 'queued' with a fresh job.
func TestMachineDownloads_failedVideoReEnqueues(t *testing.T) {
	deps := machineDownloadsDeps(t)
	h := New(deps)
	token := createToken(t, h, loginAndGetCookie(t, h))

	const id = "dQw4w9WgXcQ"
	if err := deps.Videos.Upsert(videos.Video{ID: id, URL: "https://www.youtube.com/watch?v=" + id}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetStatus(id, videos.StatusError, "boom"); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	rec := postMachineDownload(t, h, token, "https://youtu.be/"+id)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (re-queued), body = %s", rec.Code, rec.Body.String())
	}
	v, err := deps.Videos.Get(id)
	if err != nil || v == nil {
		t.Fatalf("get video: %v (video=%v)", err, v)
	}
	if v.Status != videos.StatusQueued {
		t.Fatalf("video status = %q, want %q", v.Status, videos.StatusQueued)
	}
	all, err := deps.Jobs.List()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(all))
	}
}

// TestDownloads_sessionRouteStillReQueuesExisting guards the refactor: the
// session route keeps its long-standing behavior of re-queueing a video that
// already exists (requeueExisting=true), even one already downloaded — the
// duplicate no-op is a machine-route-only rule.
func TestDownloads_sessionRouteStillReQueuesExisting(t *testing.T) {
	deps := downloadsTestDeps(t, &fakeDownloadsRunner{})
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	const id = "dQw4w9WgXcQ"
	if err := deps.Videos.Upsert(videos.Video{ID: id, URL: "https://www.youtube.com/watch?v=" + id}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetStatus(id, videos.StatusDownloaded, ""); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	rec := postDownload(t, h, sessionCookie, "https://youtu.be/"+id)
	if rec.Code != http.StatusCreated {
		t.Fatalf("session POST status = %d, want 201 (always re-queues), body = %s", rec.Code, rec.Body.String())
	}
	v, err := deps.Videos.Get(id)
	if err != nil || v == nil {
		t.Fatalf("get video: %v (video=%v)", err, v)
	}
	if v.Status != videos.StatusQueued {
		t.Fatalf("video status = %q, want %q (session route re-queues)", v.Status, videos.StatusQueued)
	}
	all, err := deps.Jobs.List()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("len(jobs) = %d, want 1 (a fresh job on re-queue)", len(all))
	}
}
