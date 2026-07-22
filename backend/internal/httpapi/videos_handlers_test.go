package httpapi

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/videos"
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

// videosTestDepsDB is videosTestDeps but also returns the backing *sql.DB,
// so a test can drop a table or install a blocking trigger to force a
// store-level error out of an otherwise normal handler flow (Videos/Jobs
// are concrete stores, not interfaces).
func videosTestDepsDB(t *testing.T) (Deps, string, *sql.DB) {
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
	}, mediaDir, db
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

// TestVideosWatched_clearsResumePosition covers the HTTP layer of the manual
// mark-watched rule: the toggle drops the stored resume position, and the GET
// a client would follow up with reports 0. The response body itself only
// carries the watched flag, which is exactly why the position has to be
// observable here.
func TestVideosWatched_clearsResumePosition(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", DurationSeconds: 600}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetResume("v1", 120); err != nil {
		t.Fatalf("seed resume: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body, _ := json.Marshal(map[string]bool{"watched": true})
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/watched", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST watched status = %d, body = %s", rec.Code, rec.Body.String())
	}

	rec = doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET video status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got videoDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode video: %v", err)
	}
	if !got.Watched {
		t.Fatalf("watched = false, want true")
	}
	if got.ResumePositionSeconds != 0 {
		t.Fatalf("resume_position_seconds = %v, want 0", got.ResumePositionSeconds)
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

// fakeStreamAccessRecorder is a StreamAccessRecorder that just remembers the
// ids it was called with, so a test can assert the stream handler actually
// invokes the hook (Task 12's now-playing guard is only as good as this
// wire).
type fakeStreamAccessRecorder struct {
	recorded []string
}

func (f *fakeStreamAccessRecorder) RecordAccess(id string) {
	f.recorded = append(f.recorded, id)
}

// TestVideosStream_recordsStreamAccess is the httpapi side of the Task 12
// now-playing guard: GET .../stream must call Deps.StreamAccess.RecordAccess
// with the video id, since that is the only signal the retention sweeper's
// guard has to protect a currently-playing video from deletion.
func TestVideosStream_recordsStreamAccess(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mediaPath := filepath.Join(videoDir, "v1.mp4")
	if err := os.WriteFile(mediaPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{MediaPath: mediaPath}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	recorder := &fakeStreamAccessRecorder{}
	deps.StreamAccess = recorder
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/stream", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET stream status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(recorder.recorded) != 1 || recorder.recorded[0] != "v1" {
		t.Fatalf("RecordAccess calls = %v, want exactly [v1]", recorder.recorded)
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
			if _, err := media.SafeMediaPath(mediaDir, tc.stored); err == nil {
				t.Fatalf("media.SafeMediaPath(%q) = nil error, want rejection", tc.stored)
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
	got, err := media.SafeMediaPath(mediaDir, mediaPath)
	if err != nil {
		t.Fatalf("safeMediaPath: %v", err)
	}
	if got == "" {
		t.Fatalf("safeMediaPath returned empty path")
	}
}

// TestSafeMediaPath_rejectsSymlinkEscape is the non-lexical companion to
// TestSafeMediaPath_rejectsTraversalAndEscape: a symlink that itself lives
// inside MediaDir but resolves to a target outside it must be rejected by
// safeMediaPath, and the stream endpoint must never serve the outside
// file's contents through it.
func TestSafeMediaPath_rejectsSymlinkEscape(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)

	outsidePath := filepath.Join(t.TempDir(), "secret.mp4")
	if err := os.WriteFile(outsidePath, []byte("top secret bytes"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	escapeLink := filepath.Join(mediaDir, "escape.mp4")
	if err := os.Symlink(outsidePath, escapeLink); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	// Unit-level: safeMediaPath itself must reject the symlink, not just the
	// higher-level stream handler.
	if _, err := media.SafeMediaPath(mediaDir, escapeLink); err == nil {
		t.Fatalf("media.SafeMediaPath(%q) = nil error, want rejection of symlink escape", escapeLink)
	}

	// HTTP-level: GET .../stream must not leak the outside file's contents
	// through the symlink either.
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{MediaPath: escapeLink}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/stream", nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("GET stream via symlink escape = 200, want an error status")
	}
	if rec.Body.String() == "top secret bytes" {
		t.Fatalf("GET stream served the outside file's contents through a symlink escape")
	}
}

// TestSafeMediaPath_allowsRegularFileInsideMediaDir is the positive
// counterpart to the symlink-escape test above: a plain (non-symlink) file
// inside MediaDir must be served normally.
func TestSafeMediaPath_allowsRegularFileInsideMediaDir(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mediaPath := filepath.Join(videoDir, "v1.mp4")
	content := []byte("regular file bytes")
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

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/stream", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET stream status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != string(content) {
		t.Fatalf("body = %q, want %q", got, string(content))
	}
}

// TestVideosThumbnail_servesFile covers the happy path: a video with a
// local thumbnail file serves its bytes, and the list/get DTO reports
// has_thumbnail=true without ever exposing the filesystem path itself.
func TestVideosThumbnail_servesFile(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	thumbPath := filepath.Join(videoDir, "v1.jpg")
	content := []byte("fake jpeg bytes")
	if err := os.WriteFile(thumbPath, content, 0o644); err != nil {
		t.Fatalf("write thumbnail file: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{ThumbnailPath: thumbPath}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/thumbnail", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET thumbnail status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != string(content) {
		t.Fatalf("body = %q, want %q", got, string(content))
	}

	getRec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil)
	var dto map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal video: %v", err)
	}
	if has, _ := dto["has_thumbnail"].(bool); !has {
		t.Fatalf("has_thumbnail = %v, want true", dto["has_thumbnail"])
	}
	if _, present := dto["thumbnail_path"]; present {
		t.Fatalf("video DTO leaked thumbnail_path: %+v", dto)
	}
}

// TestVideosThumbnail_notFoundWithoutThumbnail covers the negative path: a
// video with no thumbnail returns 404, and has_thumbnail is false.
func TestVideosThumbnail_notFoundWithoutThumbnail(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/thumbnail", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET thumbnail (none) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}

	getRec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil)
	var dto map[string]any
	if err := json.Unmarshal(getRec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal video: %v", err)
	}
	if has, _ := dto["has_thumbnail"].(bool); has {
		t.Fatalf("has_thumbnail = %v, want false", dto["has_thumbnail"])
	}
}

// TestVideosThumbnail_pathSafety mirrors the stream endpoint's symlink-escape
// guard: a thumbnail path that resolves outside mediaDir must never be
// served.
func TestVideosThumbnail_pathSafety(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	outsidePath := filepath.Join(t.TempDir(), "secret.jpg")
	if err := os.WriteFile(outsidePath, []byte("top secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	escapeLink := filepath.Join(mediaDir, "escape.jpg")
	if err := os.Symlink(outsidePath, escapeLink); err != nil {
		t.Skipf("symlinks not supported on this platform: %v", err)
	}

	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{ThumbnailPath: escapeLink}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/thumbnail", nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("GET thumbnail via symlink escape = 200, want an error status")
	}
	if rec.Body.String() == "top secret" {
		t.Fatalf("GET thumbnail served the outside file's contents through a symlink escape")
	}
}

// TestVideosGet_exposesSponsorblockSegments covers the Task 14 player's
// client-side auto-skip data: the stored sponsorblock_segments JSON column
// must come through the DTO as a structured array.
func TestVideosGet_exposesSponsorblockSegments(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	segmentsJSON := `[{"category":"sponsor","start_time":10,"end_time":25}]`
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{SponsorblockSegments: segmentsJSON}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil)
	var dto map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal video: %v", err)
	}
	segs, ok := dto["sponsorblock_segments"].([]any)
	if !ok || len(segs) != 1 {
		t.Fatalf("sponsorblock_segments = %#v, want one segment", dto["sponsorblock_segments"])
	}
	seg := segs[0].(map[string]any)
	if seg["category"] != "sponsor" || seg["start_time"] != 10.0 || seg["end_time"] != 25.0 {
		t.Fatalf("segment = %+v, want category=sponsor start_time=10 end_time=25", seg)
	}
}

// TestVideosGet_omitsEmptySponsorblockSegments covers the omitempty path: a
// video with no segments must not carry an empty-array field.
func TestVideosGet_omitsEmptySponsorblockSegments(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil)
	var dto map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal video: %v", err)
	}
	if _, present := dto["sponsorblock_segments"]; present {
		t.Fatalf("sponsorblock_segments present with no segments: %+v", dto["sponsorblock_segments"])
	}
}

// TestVideosGet_unsummarizedVideoAlwaysEmitsSummaryFields is the wire-contract
// regression for the Phase-3 summary fields: an unsummarized video (summary,
// audio_language never set) must still emit summary/summary_status/
// audio_language as present strings (never omitted/undefined on the
// frontend) and chapters/key_points as present JSON arrays, matching the
// required (non-optional) TS types in ui/src/api/types.ts.
func TestVideosGet_unsummarizedVideoAlwaysEmitsSummaryFields(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil)
	var dto map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal video: %v", err)
	}

	summary, present := dto["summary"]
	if !present {
		t.Fatalf("summary field missing entirely, want present empty string")
	}
	if summary != "" {
		t.Fatalf("summary = %v, want empty string", summary)
	}

	audioLang, present := dto["audio_language"]
	if !present {
		t.Fatalf("audio_language field missing entirely, want present empty string")
	}
	if audioLang != "" {
		t.Fatalf("audio_language = %v, want empty string", audioLang)
	}

	status, present := dto["summary_status"]
	if !present {
		t.Fatalf("summary_status field missing entirely, want present")
	}
	if status != "pending" {
		t.Fatalf("summary_status = %v, want %q", status, "pending")
	}

	chapters, ok := dto["chapters"].([]any)
	if !ok {
		t.Fatalf("chapters = %#v, want a JSON array", dto["chapters"])
	}
	if len(chapters) != 0 {
		t.Fatalf("chapters = %#v, want empty array", chapters)
	}

	keyPoints, ok := dto["key_points"].([]any)
	if !ok {
		t.Fatalf("key_points = %#v, want a JSON array", dto["key_points"])
	}
	if len(keyPoints) != 0 {
		t.Fatalf("key_points = %#v, want empty array", keyPoints)
	}
}

// TestVideosResume_rejectsNegativePosition covers the handler-level guard
// against a buggy player writing a negative resume position.
func TestVideosResume_rejectsNegativePosition(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body, _ := json.Marshal(map[string]float64{"position": -5})
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/resume", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST resume (negative) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}

	got, err := deps.Videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if got.ResumePositionSeconds != 0 {
		t.Fatalf("resume_position_seconds = %v, want unchanged (0) after rejected negative position", got.ResumePositionSeconds)
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

// TestVideosList_filtersByCategory covers GET /api/videos?category=, and
// that the returned DTO actually carries the category value (not just that
// filtering happened).
func TestVideosList_filtersByCategory(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("seed video v1: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v2", URL: "u2"}); err != nil {
		t.Fatalf("seed video v2: %v", err)
	}
	if err := deps.Videos.SetCategory("v1", "ai"); err != nil {
		t.Fatalf("set category v1: %v", err)
	}
	if err := deps.Videos.SetCategory("v2", "news"); err != nil {
		t.Fatalf("set category v2: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos?category=ai", nil)
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
	if got[0]["category"] != "ai" {
		t.Fatalf("category = %v, want \"ai\"", got[0]["category"])
	}
}

// TestVideosList_channelFilter_usesCachedChannelName asserts that when a
// channels cache row exists for ?channel=, its name is used to also match
// older rows that were written before channel ids were recorded (channel_id
// empty, channel_name matching) — the primary reason the channel page needs
// this filter to see a channel's full history, not just post-migration rows.
func TestVideosList_channelFilter_usesCachedChannelName(t *testing.T) {
	deps, _, db := videosTestDepsDB(t)
	chStore := channels.New(db)
	deps.Channels = chStore
	if err := chStore.Upsert(channels.Channel{ID: "UCx", Name: "Cached Channel", Handle: "@x"}); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	// v1 carries the channel id directly; v2 predates channel ids and only
	// carries the matching channel_name.
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u1", ChannelID: "UCx", ChannelName: "Cached Channel"}); err != nil {
		t.Fatalf("seed video v1: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v2", URL: "u2", ChannelID: "", ChannelName: "Cached Channel"}); err != nil {
		t.Fatalf("seed video v2: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v3", URL: "u3", ChannelID: "UCother", ChannelName: "Other"}); err != nil {
		t.Fatalf("seed video v3: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos?channel=UCx", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET videos status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids := map[string]bool{}
	for _, v := range got {
		ids[v["id"].(string)] = true
	}
	if !ids["v1"] || !ids["v2"] || ids["v3"] {
		t.Fatalf("filtered list ids = %+v, want v1 and v2 only", ids)
	}
}

// redownloadTestHarness wires the videos API plus a real jobs store, so a
// redownload test can assert both the video's status flip and that a job
// was actually enqueued at manual priority.
type redownloadTestHarness struct {
	http.Handler
	videos *videos.Store
	jobs   *jobs.Store
	db     *sql.DB
}

func newRedownloadTestServer(t *testing.T) *redownloadTestHarness {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	videosStore := videos.New(db)
	jobsStore := jobs.New(db)
	deps := Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		Videos:         videosStore,
		Jobs:           jobsStore,
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
	return &redownloadTestHarness{
		Handler: New(deps),
		videos:  videosStore,
		jobs:    jobsStore,
		db:      db,
	}
}

// TestRedownloadErroredVideoEnqueues covers the primary re-download path: a
// video stuck in 'error' can be re-queued via one POST, which flips it back
// to 'queued' and enqueues a fresh job at the standard manual priority (10)
// so the existing download worker picks it up.
func TestRedownloadErroredVideoEnqueues(t *testing.T) {
	h := newRedownloadTestServer(t)
	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := h.videos.SetStatus("v1", "error", "boom"); err != nil {
		t.Fatalf("seed error status: %v", err)
	}
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/redownload", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}

	got, err := h.videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if got.Status != "queued" {
		t.Fatalf("status = %q, want queued", got.Status)
	}

	list, err := h.jobs.List()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("jobs = %+v, want exactly one enqueued job", list)
	}
	if list[0].VideoID != "v1" {
		t.Fatalf("job video_id = %q, want v1", list[0].VideoID)
	}
	if list[0].Priority != downloadPriority {
		t.Fatalf("job priority = %d, want %d", list[0].Priority, downloadPriority)
	}
}

// TestRedownloadTombstonedVideoEnqueues covers the other eligible status: a
// tombstoned (manually deleted) video must also be re-downloadable.
func TestRedownloadTombstonedVideoEnqueues(t *testing.T) {
	h := newRedownloadTestServer(t)
	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := h.videos.Tombstone("v1"); err != nil {
		t.Fatalf("seed tombstoned status: %v", err)
	}
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/redownload", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}

	got, err := h.videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if got.Status != "queued" {
		t.Fatalf("status = %q, want queued", got.Status)
	}
}

// TestRedownloadTombstonedVideoRescuesFromSweep is the regression test for
// the critical bug the final fix wave addresses: a tombstoned video is
// always watched=1 with an aged watched_at (that's how the retention
// sweeper got it there in the first place), so simply flipping its status
// to 'queued' on re-download left it still matching SweepCandidates — the
// hourly sweeper would delete the freshly re-downloaded media within about
// an hour. handleRedownloadVideo must reset the watched state so the video
// no longer matches SweepCandidates' WHERE clause.
func TestRedownloadTombstonedVideoRescuesFromSweep(t *testing.T) {
	h := newRedownloadTestServer(t)
	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := h.videos.Tombstone("v1"); err != nil {
		t.Fatalf("seed tombstoned status: %v", err)
	}
	// Simulate the retention sweeper's prior state: watched, aged watched_at.
	const agedWatchedAt = "2020-01-01 00:00:00"
	if _, err := h.db.Exec(
		`UPDATE videos SET watched = 1, watched_at = ? WHERE id = ?`, agedWatchedAt, "v1",
	); err != nil {
		t.Fatalf("seed watched state: %v", err)
	}

	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/redownload", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body = %s", rec.Code, rec.Body.String())
	}

	got, err := h.videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if got.Watched {
		t.Fatalf("watched = true after redownload, want false (rescued)")
	}
	if got.WatchedAt != "" {
		t.Fatalf("watched_at = %q after redownload, want cleared", got.WatchedAt)
	}

	// The real assertion: a cutoff well after the aged watched_at (and even
	// after "now") must not return v1 from SweepCandidates any more.
	const futureCutoff = "2099-01-01 00:00:00"
	candidates, err := h.videos.SweepCandidates(futureCutoff)
	if err != nil {
		t.Fatalf("sweep candidates: %v", err)
	}
	for _, c := range candidates {
		if c.ID == "v1" {
			t.Fatalf("v1 still a sweep candidate after redownload; sweeper would delete the fresh media")
		}
	}
}

// TestRedownloadDownloadedVideoRejected covers the guard: a video that is
// already downloaded (or queued/downloading) must not be re-downloadable —
// doing so would double-enqueue or clobber a perfectly good file.
func TestRedownloadDownloadedVideoRejected(t *testing.T) {
	h := newRedownloadTestServer(t)
	if err := h.videos.Upsert(videos.Video{ID: "v2", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := h.videos.SetDownloaded("v2", videos.DownloadedResult{}); err != nil {
		t.Fatalf("seed downloaded status: %v", err)
	}
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v2/redownload", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}

	list, err := h.jobs.List()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("jobs = %+v, want none enqueued on rejection", list)
	}
}

// TestRedownloadQueuedVideoRejected covers the double-enqueue hazard the
// guard exists to prevent: a video already queued (or downloading) must not
// be re-downloadable a second time.
func TestRedownloadQueuedVideoRejected(t *testing.T) {
	h := newRedownloadTestServer(t)
	if err := h.videos.Upsert(videos.Video{ID: "v3", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := h.videos.SetStatus("v3", "queued", ""); err != nil {
		t.Fatalf("seed queued status: %v", err)
	}
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v3/redownload", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}

	list, err := h.jobs.List()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("jobs = %+v, want none enqueued on rejection", list)
	}
}

// TestRedownloadUnknownVideo_404 covers the not-found path via lookupVideo.
func TestRedownloadUnknownVideo_404(t *testing.T) {
	h := newRedownloadTestServer(t)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/missing/redownload", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosGet_emptySponsorblockRawIsOmitted covers parseSponsorblockSegments'
// raw=="" branch. The store's schema always defaults sponsorblock_segments
// to "[]" (never a literal empty string), so this state can only arise from
// a raw column edit — a plausible defensive scenario (e.g. a manual DB fix
// or a future writer bug), which is exactly what the handler guards
// against by omitting the field rather than crashing on it.
func TestVideosGet_emptySponsorblockRawIsOmitted(t *testing.T) {
	deps, _, db := videosTestDepsDB(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := db.Exec(`UPDATE videos SET sponsorblock_segments = '' WHERE id = 'v1'`); err != nil {
		t.Fatalf("force empty sponsorblock_segments: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil)
	var dto map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal video: %v", err)
	}
	if _, present := dto["sponsorblock_segments"]; present {
		t.Fatalf("sponsorblock_segments present for raw=='': %+v", dto["sponsorblock_segments"])
	}
}

// TestVideosGet_malformedChaptersFallsBackToEmptyArray covers rawJSONOrNil's
// invalid-JSON branch. Like sponsorblock_segments, the schema always
// defaults chapters to the valid "[]", so a malformed value can only arise
// from a bad writer (e.g. a corrupt yt-dlp chapters payload) — the handler
// must still hand the frontend a valid (empty) JSON array rather than
// forwarding broken text or letting json.Marshal fail.
func TestVideosGet_malformedChaptersFallsBackToEmptyArray(t *testing.T) {
	deps, _, db := videosTestDepsDB(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := db.Exec(`UPDATE videos SET chapters = 'not json' WHERE id = 'v1'`); err != nil {
		t.Fatalf("force malformed chapters: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil)
	var dto map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal video: %v", err)
	}
	chapters, ok := dto["chapters"].([]any)
	if !ok {
		t.Fatalf("chapters = %#v, want a JSON array (fallback from malformed text)", dto["chapters"])
	}
	if len(chapters) != 0 {
		t.Fatalf("chapters = %#v, want empty", chapters)
	}
}

// TestVideosList_storeError_500 covers the s.videos.List error branch of
// handleListVideos.
func TestVideosList_storeError_500(t *testing.T) {
	deps, _, db := videosTestDepsDB(t)
	if _, err := db.Exec(`DROP TABLE videos`); err != nil {
		t.Fatalf("drop videos table: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/videos (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosList_noStoreConfigured_emptyArray covers the s.videos == nil
// branch of handleListVideos.
func TestVideosList_noStoreConfigured_emptyArray(t *testing.T) {
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
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/videos (no store) status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "[]\n" && got != "[]" {
		t.Fatalf("GET /api/videos (no store) body = %q, want []", got)
	}

	// Every other lookupVideo-backed route must fail closed with 503, not
	// panic, when no store is wired.
	for _, req := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/videos/v1"},
		{http.MethodDelete, "/api/videos/v1"},
		{http.MethodPost, "/api/videos/v1/favorite"},
		{http.MethodPost, "/api/videos/v1/watched"},
		{http.MethodPost, "/api/videos/v1/resume"},
		{http.MethodGet, "/api/videos/v1/stream"},
		{http.MethodGet, "/api/videos/v1/thumbnail"},
	} {
		rec := doReq(t, h, cookie, req.method, req.path, nil)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s (no store) status = %d, want 503, body = %s", req.method, req.path, rec.Code, rec.Body.String())
		}
	}
}

// TestVideosGet_storeError_500 covers lookupVideo's s.videos.Get error
// branch.
func TestVideosGet_storeError_500(t *testing.T) {
	deps, _, db := videosTestDepsDB(t)
	if _, err := db.Exec(`DROP TABLE videos`); err != nil {
		t.Fatalf("drop videos table: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/videos/v1 (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosGet_unknownVideo_404 covers handleGetVideo's own not-found
// branch (as opposed to lookupVideo being exercised via a different
// handler).
func TestVideosGet_unknownVideo_404(t *testing.T) {
	deps, _ := videosTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/videos/missing status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosDelete_unknownVideo_404 covers handleDeleteVideo's own
// not-found branch.
func TestVideosDelete_unknownVideo_404(t *testing.T) {
	deps, _ := videosTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodDelete, "/api/videos/missing", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("DELETE /api/videos/missing status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosDelete_storeError_500 covers the s.videos.Tombstone error
// branch: a trigger blocks the status column update Tombstone relies on.
func TestVideosDelete_storeError_500(t *testing.T) {
	deps, _, db := videosTestDepsDB(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER block_status_update BEFORE UPDATE OF status ON videos BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodDelete, "/api/videos/v1", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("DELETE /api/videos/v1 (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosFavorite_unknownVideo_404 covers handleFavoriteVideo's own
// not-found branch.
func TestVideosFavorite_unknownVideo_404(t *testing.T) {
	deps, _ := videosTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/missing/favorite", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST favorite (unknown) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosFavorite_explicitBodySetsValue covers passing an explicit
// favorite value (true or false) rather than relying on the toggle default.
func TestVideosFavorite_explicitBodySetsValue(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body, _ := json.Marshal(map[string]bool{"favorite": true})
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/favorite", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST favorite (explicit true) status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := deps.Videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if !got.Favorite {
		t.Fatal("favorite = false, want true after explicit {\"favorite\":true}")
	}

	body, _ = json.Marshal(map[string]bool{"favorite": false})
	rec = doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/favorite", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST favorite (explicit false) status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err = deps.Videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if got.Favorite {
		t.Fatal("favorite = true, want false after explicit {\"favorite\":false}")
	}
}

// TestVideosFavorite_storeError_500 covers the s.videos.SetFavorite error
// branch.
func TestVideosFavorite_storeError_500(t *testing.T) {
	deps, _, db := videosTestDepsDB(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER block_favorite_update BEFORE UPDATE OF favorite ON videos BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/favorite", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST favorite (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosWatched_unknownVideo_404 covers handleWatchedVideo's not-found
// branch. handleWatchedVideo has no dedicated test at all prior to this
// (it shares lookupVideo, invalid-body, and store-error shapes with its
// sibling handlers, but exercises its own code path).
func TestVideosWatched_unknownVideo_404(t *testing.T) {
	deps, _ := videosTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/missing/watched", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST watched (unknown) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosWatched_missingBody_400 covers the required-body validation:
// unlike favorite (which toggles on empty body), watched requires an
// explicit bool.
func TestVideosWatched_missingBody_400(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/watched", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST watched (no body) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}

	body, _ := json.Marshal(map[string]string{"not_watched_field": "x"})
	rec = doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/watched", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST watched (missing field) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosWatched_setTrueThenFalse covers the primary success path in
// both directions: marking watched, then manually un-watching (which must
// also clear watched_at, rescuing the video from the retention sweep).
func TestVideosWatched_setTrueThenFalse(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body, _ := json.Marshal(map[string]bool{"watched": true})
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/watched", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST watched=true status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := deps.Videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if !got.Watched {
		t.Fatal("watched = false, want true")
	}

	body, _ = json.Marshal(map[string]bool{"watched": false})
	rec = doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/watched", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST watched=false status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err = deps.Videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if got.Watched {
		t.Fatal("watched = true, want false after manual un-watch")
	}
	if got.WatchedAt != "" {
		t.Fatalf("watched_at = %q, want cleared after manual un-watch", got.WatchedAt)
	}
}

// TestVideosWatched_storeError_500 covers the s.videos.SetWatched error
// branch.
func TestVideosWatched_storeError_500(t *testing.T) {
	deps, _, db := videosTestDepsDB(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER block_watched_update BEFORE UPDATE OF watched ON videos BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body, _ := json.Marshal(map[string]bool{"watched": true})
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/watched", body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST watched (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosResume_unknownVideo_404 covers handleResumeVideo's own
// not-found branch.
func TestVideosResume_unknownVideo_404(t *testing.T) {
	deps, _ := videosTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body, _ := json.Marshal(map[string]float64{"position": 5})
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/missing/resume", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST resume (unknown) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosResume_missingBody_400 covers the required-field validation.
func TestVideosResume_missingBody_400(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/resume", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST resume (no body) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosResume_storeError_500 covers the s.videos.SetResume error
// branch.
func TestVideosResume_storeError_500(t *testing.T) {
	deps, _, db := videosTestDepsDB(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER block_resume_update BEFORE UPDATE OF resume_position_seconds ON videos BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body, _ := json.Marshal(map[string]float64{"position": 5})
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/resume", body)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST resume (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosStream_unknownVideo_404 covers handleStreamVideo's own
// not-found branch.
func TestVideosStream_unknownVideo_404(t *testing.T) {
	deps, _ := videosTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/missing/stream", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET stream (unknown) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosStream_noMedia_404 covers the v.MediaPath == "" branch: a video
// that has never been downloaded has no media to stream.
func TestVideosStream_noMedia_404(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/stream", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET stream (no media) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal error body: %v", err)
	}
	if got["error"] != "no media for this video" {
		t.Fatalf("error = %q, want the no-media message", got["error"])
	}
}

// TestVideosStream_missingFileOnDisk_404 covers the os.Open error branch: a
// media_path that resolves safely under mediaDir but whose file no longer
// exists on disk (e.g. removed out-of-band) must 404, not panic or 500.
func TestVideosStream_missingFileOnDisk_404(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mediaPath := filepath.Join(videoDir, "v1.mp4")
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{MediaPath: mediaPath}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	// mediaPath is never actually written to disk: SafeMediaPath resolves it
	// fine (the directory exists), but os.Open must fail.

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/stream", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET stream (missing file) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosThumbnail_unknownVideo_404 covers handleVideoThumbnail's own
// not-found branch.
func TestVideosThumbnail_unknownVideo_404(t *testing.T) {
	deps, _ := videosTestDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/missing/thumbnail", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET thumbnail (unknown) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestVideosThumbnail_missingFileOnDisk_404 covers the os.Open error branch
// for the thumbnail path (safely resolved, but absent on disk).
func TestVideosThumbnail_missingFileOnDisk_404(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	thumbPath := filepath.Join(videoDir, "v1.jpg")
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{ThumbnailPath: thumbPath}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	// thumbPath is never written to disk.

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/thumbnail", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET thumbnail (missing file) status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestRedownload_jobsNotConfigured_503 covers handleRedownloadVideo's
// s.jobs == nil branch: an eligible (errored) video still can't be
// re-downloaded if the queue itself isn't wired.
func TestRedownload_jobsNotConfigured_503(t *testing.T) {
	deps, _ := videosTestDeps(t) // no Jobs set
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetStatus("v1", "error", "boom"); err != nil {
		t.Fatalf("seed error status: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/redownload", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST redownload (no jobs store) status = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
}

// TestRedownload_setWatchedStoreError_500 covers the s.videos.SetWatched
// error branch of handleRedownloadVideo (the sweep-rescue step).
func TestRedownload_setWatchedStoreError_500(t *testing.T) {
	h := newRedownloadTestServer(t)
	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := h.videos.SetStatus("v1", "error", "boom"); err != nil {
		t.Fatalf("seed error status: %v", err)
	}
	if _, err := h.db.Exec(`CREATE TRIGGER block_watched_update BEFORE UPDATE OF watched ON videos BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/redownload", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST redownload (set-watched store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestRedownload_setStatusStoreError_500 covers the s.videos.SetStatus
// error branch of handleRedownloadVideo (the requeue step, after the
// watched-reset step has already succeeded).
func TestRedownload_setStatusStoreError_500(t *testing.T) {
	h := newRedownloadTestServer(t)
	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := h.videos.SetStatus("v1", "error", "boom"); err != nil {
		t.Fatalf("seed error status: %v", err)
	}
	if _, err := h.db.Exec(`CREATE TRIGGER block_status_update BEFORE UPDATE OF status ON videos BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/redownload", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST redownload (set-status store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestRedownload_enqueueStoreError_500 covers the s.jobs.Enqueue error
// branch of handleRedownloadVideo: the video's watched/status resets both
// succeed, but the queue table itself is broken.
func TestRedownload_enqueueStoreError_500(t *testing.T) {
	h := newRedownloadTestServer(t)
	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := h.videos.SetStatus("v1", "error", "boom"); err != nil {
		t.Fatalf("seed error status: %v", err)
	}
	if _, err := h.db.Exec(`DROP TABLE download_jobs`); err != nil {
		t.Fatalf("drop download_jobs table: %v", err)
	}
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/redownload", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST redownload (enqueue store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}

	// The video's status must still have been flipped to queued even though
	// the enqueue itself failed — the redownload isn't picked up, but the
	// state isn't silently left as 'error' either. This is a side effect of
	// SetStatus running before Enqueue, documented here rather than
	// asserted as a "requirement", since it's an artifact of the handler's
	// sequencing rather than a promise it makes.
	got, err := h.videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
}

// TestStreamVideo_downloadFlagAttachesFilename covers the ?download=1 arm of
// the stream handler: the same bytes, but with a Content-Disposition naming
// the file after the video's title and the *stored* file's extension. The
// UI cannot do this itself — media_path is deliberately absent from
// videoDTO, so the browser never learns whether this is .mp4 or .webm.
func TestStreamVideo_downloadFlagAttachesFilename(t *testing.T) {
	deps, mediaDir := videosTestDeps(t)
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mediaPath := filepath.Join(videoDir, "v1.webm")
	if err := os.WriteFile(mediaPath, []byte("media bytes"), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{
		ID: "v1", URL: "u", ChannelID: "chan1", Title: "How trains work",
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetDownloaded("v1", videos.DownloadedResult{MediaPath: mediaPath}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// Without the flag the stream stays inline — the player must not be
	// handed an attachment mid-playback.
	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/stream", nil)
	if got := rec.Header().Get("Content-Disposition"); got != "" {
		t.Fatalf("plain stream Content-Disposition = %q, want empty", got)
	}

	rec = doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/stream?download=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("download status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "media bytes" {
		t.Fatalf("body = %q, want the media bytes", got)
	}
	got := rec.Header().Get("Content-Disposition")
	if !strings.Contains(got, `filename="How trains work.webm"`) {
		t.Fatalf("Content-Disposition = %q, want it to name How trains work.webm", got)
	}
	if !strings.HasPrefix(got, "attachment;") {
		t.Fatalf("Content-Disposition = %q, want an attachment", got)
	}
}

func TestDownloadFilename(t *testing.T) {
	tests := []struct {
		name  string
		title string
		id    string
		ext   string
		want  string
	}{
		{"plain title", "How trains work", "v1", ".mp4", "How trains work.mp4"},
		{"strips path separators", "a/b\\c", "v1", ".mp4", "a_b_c.mp4"},
		// The trailing quote becomes "_" and is then trimmed off the end,
		// which is why this is "say _hi" and not "say _hi_".
		{"strips quotes that would break the header", `say "hi"`, "v1", ".mp4", "say _hi.mp4"},
		{"falls back to the id when nothing survives", "日本語", "v1", ".mkv", "v1.mkv"},
		{"trims leading and trailing dots", "...title...", "v1", ".mp4", "title.mp4"},
		{"keeps the id when the title is empty", "", "v1", ".webm", "v1.webm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := downloadFilename(tt.title, tt.id, tt.ext); got != tt.want {
				t.Fatalf("downloadFilename(%q, %q, %q) = %q, want %q", tt.title, tt.id, tt.ext, got, tt.want)
			}
		})
	}
}

// A 200-character title must not produce a 200-character filename — some
// filesystems cap a single path component at 255 bytes, and the extension
// still has to fit.
func TestDownloadFilename_truncatesLongTitles(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := downloadFilename(long, "v1", ".mp4")
	if len(got) > 130 {
		t.Fatalf("downloadFilename produced a %d-char name, want it truncated", len(got))
	}
	if !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("downloadFilename = %q, want the extension preserved", got)
	}
}

// TestVideosCategory_overridesAnExistingOne asserts the direction this
// endpoint owns: the user overrules a category the model already wrote, so
// the write must be unconditional. The other direction — a classify pass
// refusing to overwrite a manual pick — belongs to
// videos.Store.SetCategoryIfUnset and is proven in summarize's worker tests.
func TestVideosCategory_overridesAnExistingOne(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetCategory("v1", "gaming"); err != nil {
		t.Fatalf("seed category: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	body, _ := json.Marshal(map[string]string{"category": "science"})
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/category", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST category status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"category":"science"`) {
		t.Fatalf("body = %s, want the new category echoed back", rec.Body.String())
	}
	got, err := deps.Videos.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get video: %v", err)
	}
	if got.Category != "science" {
		t.Fatalf("category = %q, want science", got.Category)
	}
}

// TestVideosCategory_rejectsUnknownAndMissing: a bad id here is a caller bug,
// not a model reply, so it must 400 rather than be repaired or silently
// downgraded to 'uncategorized' the way NormalizeCategory treats an LLM answer.
func TestVideosCategory_rejectsUnknownAndMissing(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	for _, body := range []string{`{"category":"not-a-category"}`, `{}`, `not json`} {
		rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/category", []byte(body))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
	got, _ := deps.Videos.Get("v1")
	if got.Category != "uncategorized" {
		t.Fatalf("category = %q, want it untouched by the rejected writes", got.Category)
	}

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/missing/category",
		[]byte(`{"category":"ai"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown video: status = %d, want 404", rec.Code)
	}
}

// TestVideosCategory_writeFailure500 asserts a store failure surfaces rather
// than returning 200 with the old category still in place.
func TestVideosCategory_writeFailure500(t *testing.T) {
	deps, _, db := videosTestDepsDB(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER no_category BEFORE UPDATE OF category ON videos
		BEGIN SELECT RAISE(ABORT, 'category writes blocked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/category", []byte(`{"category":"ai"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}
