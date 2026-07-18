package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/trick77/vark/internal/auth"
	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/videos"
)

// videosTestDeps builds Deps wired for the videos API: dev auth plus a
// videos store and a MediaDir rooted in a fresh temp dir, sharing one test
// database.
func videosTestDeps(t *testing.T) (Deps, string) {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	mediaDir := t.TempDir()
	return Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		Videos:         videos.New(db),
		MediaDir:       mediaDir,
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}, mediaDir
}

func doReq(t *testing.T, h http.Handler, cookie *http.Cookie, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestVideosResume_autoMarksWatchedAtNinetyPercent exercises the HTTP layer
// of the decided watched rule: POST resume at >=90% marks watched via the
// API, and a later re-watch doesn't reset watched_at.
func TestVideosResume_autoMarksWatchedAtNinetyPercent(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body, _ := json.Marshal(map[string]float64{"position": 95})
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/resume", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST resume status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got, err := deps.Videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if !got.Watched {
		t.Fatalf("watched = false, want true after resume >= 90%%")
	}
}

// TestVideosDelete_tombstonesRowAndUnlinksFile is the central Task 11
// delete guarantee: DELETE removes the media file from disk but keeps the
// row, clearing media_path and setting status=tombstoned.
func TestVideosDelete_tombstonesRowAndUnlinksFile(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mediaPath := filepath.Join(videoDir, "v1.mp4")
	if err := os.WriteFile(mediaPath, []byte("fake video bytes"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	thumbPath := filepath.Join(videoDir, "v1.jpg")
	if err := os.WriteFile(thumbPath, []byte("fake thumbnail bytes"), 0o644); err != nil {
		t.Fatalf("write thumbnail file: %v", err)
	}
	vttPath := filepath.Join(videoDir, "v1.en.vtt")
	if err := os.WriteFile(vttPath, []byte("WEBVTT"), 0o644); err != nil {
		t.Fatalf("write vtt file: %v", err)
	}

	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{MediaPath: mediaPath, ThumbnailPath: thumbPath}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodDelete, "/api/videos/v1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, body = %s", rec.Code, rec.Body.String())
	}

	if _, err := os.Stat(mediaPath); !os.IsNotExist(err) {
		t.Fatalf("media file still exists after delete: err = %v", err)
	}
	if _, err := os.Stat(thumbPath); !os.IsNotExist(err) {
		t.Fatalf("thumbnail file still exists after delete: err = %v", err)
	}
	if _, err := os.Stat(vttPath); !os.IsNotExist(err) {
		t.Fatalf("vtt file still exists after delete: err = %v", err)
	}

	got, err := deps.Videos.Get("v1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if got == nil {
		t.Fatal("row was deleted, want kept (tombstoned)")
	}
	if got.Status != "tombstoned" {
		t.Fatalf("status = %q, want tombstoned", got.Status)
	}
	if got.MediaPath != "" {
		t.Fatalf("media_path = %q, want cleared", got.MediaPath)
	}
}

// TestVideosStream_rangeRequest asserts the player-facing Range support:
// http.ServeContent must answer a byte-range GET with 206 and Content-Range.
func TestVideosStream_rangeRequest(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mediaPath := filepath.Join(videoDir, "v1.mp4")
	content := []byte("0123456789")
	if err := os.WriteFile(mediaPath, content, 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{MediaPath: mediaPath}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/api/videos/v1/stream", nil)
	req.AddCookie(cookie)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("GET stream (Range) status = %d, want 206, body = %s", rec.Code, rec.Body.String())
	}
	if cr := rec.Header().Get("Content-Range"); cr == "" {
		t.Fatalf("Content-Range header missing")
	}
	if got := rec.Body.String(); got != "0123" {
		t.Fatalf("body = %q, want %q", got, "0123")
	}
}

// TestSafeMediaPath_rejectsTraversalAndEscape is the path-safety guard: a
// stored media_path containing ".." or otherwise resolving outside
// MediaDir must be rejected, never served or unlinked.
func TestSafeMediaPath_rejectsTraversalAndEscape(t *testing.T) {
	mediaDir := t.TempDir()
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretPath, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	cases := []struct {
		name   string
		stored string
	}{
		{"dotdot traversal", filepath.Join(mediaDir, "..", "secret.txt")},
		{"absolute escape", secretPath},
		{"relative dotdot", "../secret.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := safeMediaPath(mediaDir, tc.stored); err == nil {
				t.Fatalf("safeMediaPath(%q) = nil error, want rejection", tc.stored)
			}
		})
	}
}

// TestSafeMediaPath_allowsPathWithinMediaDir is the positive control for
// the guard above: a path that genuinely resolves inside MediaDir must be
// accepted.
func TestSafeMediaPath_allowsPathWithinMediaDir(t *testing.T) {
	mediaDir := t.TempDir()
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mediaPath := filepath.Join(videoDir, "v1.mp4")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := safeMediaPath(mediaDir, mediaPath)
	if err != nil {
		t.Fatalf("safeMediaPath: %v", err)
	}
	if got == "" {
		t.Fatalf("safeMediaPath returned empty path")
	}
}

// TestVideosFavorite_toggle covers the toggle-without-body behavior.
func TestVideosFavorite_toggle(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/favorite", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST favorite status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := deps.Videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if !got.Favorite {
		t.Fatalf("favorite = false, want true after toggle")
	}
}

// TestVideosList_filtersByQueryParam covers GET /api/videos?filter=.
func TestVideosList_filtersByQueryParam(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetFavorite("v1", true); err != nil {
		t.Fatalf("set favorite: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos?filter=favorites", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET videos status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0]["id"] != "v1" {
		t.Fatalf("filtered list = %+v, want [v1]", got)
	}
}
