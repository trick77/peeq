package httpapi

import (
	"net/http"
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

// TestVideosSubtitles_servesVTT covers the happy path: the stored transcript is
// served back byte-for-byte as text/vtt.
//
// Byte-for-byte is the requirement, not a nicety: the <track> element, the
// transcript panel's own parser and the user-facing .vtt download all read this
// body, so anything that reformatted it would break one of the three.
func TestVideosSubtitles_servesVTT(t *testing.T) {
	deps, _ := videosTestDeps(t)
	content := "WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhello"
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetTranscript("v1", videos.TranscriptSourceDownload, content); err != nil {
		t.Fatalf("store transcript: %v", err)
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
	if got := rec.Body.String(); got != content {
		t.Fatalf("body = %q, want %q", got, content)
	}
}

// TestVideosSubtitles_notFoundWithoutTranscript covers the negative path: a
// video with nothing stored returns 404, and the UI hides the transcript panel.
func TestVideosSubtitles_notFoundWithoutTranscript(t *testing.T) {
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

// TestVideosSubtitles_ignoresSubtitlePathEntirely is what replaced this
// endpoint's two path-safety tests.
//
// It used to resolve subtitle_path through media.SafeMediaPath and serve the
// file, so a symlink escape or a literal "../" traversal stored in that column
// was a real route to a file outside the media dir, and each was pinned by its
// own test. Since migration 0023 the endpoint reads the row and never touches
// the filesystem, so the whole class is gone rather than guarded — which is
// what this pins: a hostile path with no stored transcript is simply a 404.
func TestVideosSubtitles_ignoresSubtitlePathEntirely(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetSubtitle("v1", "../../../../etc/passwd", "en"); err != nil {
		t.Fatalf("set subtitle path: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/subtitles", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET subtitles status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}
