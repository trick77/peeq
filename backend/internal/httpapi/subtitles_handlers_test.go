package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

// TestVideosSubtitles_servesVTT covers the happy path: a video with a local
// subtitle file serves its bytes with the VTT content type.
func TestVideosSubtitles_servesVTT(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vttPath := filepath.Join(videoDir, "v1.en.vtt")
	content := []byte("WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhello")
	if err := os.WriteFile(vttPath, content, 0o644); err != nil {
		t.Fatalf("write vtt file: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetSubtitle("v1", vttPath, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/subtitles", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET subtitles status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/vtt; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/vtt; charset=utf-8", ct)
	}
	if got := rec.Body.String(); got != string(content) {
		t.Fatalf("body = %q, want %q", got, string(content))
	}
}

// TestVideosSubtitles_notFoundWithoutSubtitle covers the negative path: a
// video with no subtitle_path returns 404.
func TestVideosSubtitles_notFoundWithoutSubtitle(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/subtitles", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET subtitles (none) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosSubtitles_pathSafety mirrors the thumbnail/stream endpoints'
// symlink-escape guard: a subtitle path that resolves outside mediaDir must
// never be served.
func TestVideosSubtitles_pathSafety(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	outsidePath := filepath.Join(t.TempDir(), "secret.vtt")
	if err := os.WriteFile(outsidePath, []byte("top secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	escapeLink := filepath.Join(mediaDir, "escape.vtt")
	if err := os.Symlink(outsidePath, escapeLink); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetSubtitle("v1", escapeLink, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/subtitles", nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("GET subtitles via symlink escape = 200, want an error status")
	}
	if rec.Body.String() == "top secret" {
		t.Fatalf("GET subtitles served the outside file's contents through a symlink escape")
	}
}

// TestVideosSubtitles_traversalRejected covers a literal ".." traversal
// attempt in subtitle_path, which media.SafeMediaPath must reject.
func TestVideosSubtitles_traversalRejected(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	outside := t.TempDir()
	secretPath := filepath.Join(outside, "secret.vtt")
	if err := os.WriteFile(secretPath, []byte("top secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	traversal := filepath.Join(mediaDir, "..", filepath.Base(outside), "secret.vtt")
	if err := deps.Videos.SetSubtitle("v1", traversal, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/subtitles", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET subtitles (traversal) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() == "top secret" {
		t.Fatalf("GET subtitles served the outside file's contents through traversal")
	}
}
