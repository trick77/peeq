package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/playback"
	"github.com/trick77/peeq/internal/videos"
)

// playbackTestDeps is videosTestDeps plus the playback store, sharing the same
// database so the pointer and the videos it points at agree.
func playbackTestDeps(t *testing.T) Deps {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	return Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Videos:         videos.New(db),
		Playback:       playback.New(db),
		MediaDir:       t.TempDir(),
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
}

// seedPlayable upserts a video and marks it downloaded — PUT /api/playback
// refuses anything else.
func seedPlayable(t *testing.T, vs *videos.Store, id string) {
	t.Helper()
	if err := vs.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id}); err != nil {
		t.Fatalf("seed video %s: %v", id, err)
	}
	if err := vs.SetStatus(id, "downloaded", ""); err != nil {
		t.Fatalf("set status %s: %v", id, err)
	}
}

func getPlayback(t *testing.T, h http.Handler, cookie *http.Cookie) playback.State {
	t.Helper()
	rec := doReq(t, h, cookie, http.MethodGet, "/api/playback", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET playback status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got playback.State
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode playback state: %v", err)
	}
	return got
}

func TestPlayback_putThenGetRoundTrips(t *testing.T) {
	deps := playbackTestDeps(t)
	seedPlayable(t, deps.Videos, "v1")
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	if got := getPlayback(t, h, cookie); got.VideoID != "" {
		t.Fatalf("video_id = %q, want empty before anything is set", got.VideoID)
	}

	body, _ := json.Marshal(map[string]string{"video_id": "v1"})
	rec := doReq(t, h, cookie, http.MethodPut, "/api/playback", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT playback status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := getPlayback(t, h, cookie); got.VideoID != "v1" {
		t.Fatalf("video_id = %q, want v1", got.VideoID)
	}

	// An empty video_id is how the client says "nothing is playing".
	body, _ = json.Marshal(map[string]string{"video_id": ""})
	rec = doReq(t, h, cookie, http.MethodPut, "/api/playback", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT empty playback status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := getPlayback(t, h, cookie); got.VideoID != "" {
		t.Fatalf("video_id = %q, want empty after clear", got.VideoID)
	}
}

func TestPlaybackPut_rejectsUnplayableTarget(t *testing.T) {
	deps := playbackTestDeps(t)
	// Present but never downloaded — as dead a pointer as one at a video that
	// doesn't exist.
	if err := deps.Videos.Upsert(videos.Video{ID: "queued", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	for _, id := range []string{"nope", "queued"} {
		body, _ := json.Marshal(map[string]string{"video_id": id})
		rec := doReq(t, h, cookie, http.MethodPut, "/api/playback", body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("PUT playback %q status = %d, want 404, body = %s", id, rec.Code, rec.Body.String())
		}
	}
}

// TestPlayback_markWatchedClearsThePointer covers the clearing rule: the manual
// toggle zeroes the resume position, so a surviving pointer would have the rail
// reopen a finished video at 0:00.
func TestPlayback_markWatchedClearsThePointer(t *testing.T) {
	deps := playbackTestDeps(t)
	seedPlayable(t, deps.Videos, "v1")
	seedPlayable(t, deps.Videos, "v2")
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body, _ := json.Marshal(map[string]string{"video_id": "v1"})
	doReq(t, h, cookie, http.MethodPut, "/api/playback", body)

	// Marking a DIFFERENT video watched must leave the pointer alone: by then the
	// user may already be watching something else (ClearIfVideo, not Clear).
	body, _ = json.Marshal(map[string]bool{"watched": true})
	if rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v2/watched", body); rec.Code != http.StatusOK {
		t.Fatalf("POST watched v2 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := getPlayback(t, h, cookie); got.VideoID != "v1" {
		t.Fatalf("video_id = %q, want v1 left alone", got.VideoID)
	}

	// Un-watching means "I want this back", so it keeps the pointer too.
	body, _ = json.Marshal(map[string]bool{"watched": false})
	if rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/watched", body); rec.Code != http.StatusOK {
		t.Fatalf("POST unwatched v1 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := getPlayback(t, h, cookie); got.VideoID != "v1" {
		t.Fatalf("video_id = %q, want v1 kept on un-watch", got.VideoID)
	}

	body, _ = json.Marshal(map[string]bool{"watched": true})
	if rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/watched", body); rec.Code != http.StatusOK {
		t.Fatalf("POST watched v1 status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := getPlayback(t, h, cookie); got.VideoID != "" {
		t.Fatalf("video_id = %q, want empty after the pointed-at video was marked watched", got.VideoID)
	}
}

// TestPlayback_deleteClearsThePointer is the second clearing call site. Note it
// passes even without the explicit clear, because playback.Store.Get filters
// tombstoned rows — which is the point: both layers are asserted, here through
// the handler and in the store's own test by tombstoning behind its back.
func TestPlayback_deleteClearsThePointer(t *testing.T) {
	deps := playbackTestDeps(t)
	seedPlayable(t, deps.Videos, "v1")
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body, _ := json.Marshal(map[string]string{"video_id": "v1"})
	doReq(t, h, cookie, http.MethodPut, "/api/playback", body)

	if rec := doReq(t, h, cookie, http.MethodDelete, "/api/videos/v1", nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE video status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := getPlayback(t, h, cookie); got.VideoID != "" {
		t.Fatalf("video_id = %q, want empty after the pointed-at video was deleted", got.VideoID)
	}
}

// TestPlayback_nilStoreFailsOpen pins the deliberate deviation from this
// package's fail-closed-with-503 convention: the pointer is a convenience the
// rail falls back from silently, so a server without the store must not turn it
// into an error the SPA has to handle mid-playback.
func TestPlayback_nilStoreFailsOpen(t *testing.T) {
	deps := playbackTestDeps(t)
	seedPlayable(t, deps.Videos, "v1")
	deps.Playback = nil
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	if got := getPlayback(t, h, cookie); got.VideoID != "" {
		t.Fatalf("video_id = %q, want empty", got.VideoID)
	}
	body, _ := json.Marshal(map[string]string{"video_id": "v1"})
	rec := doReq(t, h, cookie, http.MethodPut, "/api/playback", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT playback status = %d, want a 200 no-op, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPlaybackPut_rejectsInvalidBody(t *testing.T) {
	deps := playbackTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodPut, "/api/playback", []byte("not json"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT playback status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPlayback_requiresAuth(t *testing.T) {
	h := New(playbackTestDeps(t))
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/playback", nil),
		httptest.NewRequest(http.MethodPut, "/api/playback", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", req.Method, req.URL.Path, rec.Code)
		}
	}
}
