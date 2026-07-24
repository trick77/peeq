package httpapi

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/sharelink"
	"github.com/trick77/peeq/internal/videos"
)

// shareTestDeps is videosTestDepsDB plus a wired ShareLinks store — the share
// endpoints need it, and it shares the same DB so the video FK is satisfied.
func shareTestDeps(t *testing.T) (Deps, string, *sql.DB) {
	t.Helper()
	deps, mediaDir, db := videosTestDepsDB(t)
	deps.ShareLinks = sharelink.New(db)
	deps.PublicURL = "https://peeq.example"
	return deps, mediaDir, db
}

// getPublic issues an unauthenticated GET (no session cookie) — the whole
// point of the public share routes.
func getPublic(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// createShare drives the owner POST and returns the minted token + URL.
func createShare(t *testing.T, h http.Handler, cookie *http.Cookie, id, ttl string) shareStatusResponse {
	t.Helper()
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/"+id+"/share", []byte(`{"ttl":"`+ttl+`"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST share status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp shareStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode share response: %v", err)
	}
	return resp
}

func TestShare_ownerCreateStatusDelete(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "Hello", ChannelName: "Chan"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// Create.
	created := createShare(t, h, cookie, "v1", "7d")
	if !created.Shared || created.Token == "" {
		t.Fatalf("create returned %+v, want shared with a token", created)
	}
	if !strings.HasPrefix(created.URL, "https://peeq.example/s/") {
		t.Fatalf("share URL = %q, want it built from PublicURL", created.URL)
	}
	if created.ExpiresAt == "" {
		t.Fatal("7d ttl should set an expiry")
	}

	// Status reflects the live link.
	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/share", nil)
	var status shareStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if !status.Shared || status.Token != created.Token {
		t.Fatalf("status = %+v, want the same live token", status)
	}

	// Delete → not shared, and the token stops resolving publicly.
	if rec := doReq(t, h, cookie, http.MethodDelete, "/api/videos/v1/share", nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE share status = %d", rec.Code)
	}
	if rec := getPublic(t, h, "/api/s/"+created.Token); rec.Code != http.StatusNotFound {
		t.Fatalf("public GET after revoke = %d, want 404", rec.Code)
	}
}

func TestShare_createBadTTL(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/share", []byte(`{"ttl":"1y"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST bad ttl = %d, want 400", rec.Code)
	}
}

func TestShare_ownerRoutesRequireAuth(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	h := New(deps)
	// No cookie: the owner share routes must reject.
	req := httptest.NewRequest(http.MethodGet, "/api/videos/v1/share", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET owner share = %d, want 401", rec.Code)
	}
}

func TestShare_publicVideoMetadata(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "https://youtu.be/v1", Title: "Threat Assessment", ChannelName: "Lex Clips"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetSummary("v1", "A short summary.", "[]", `[{"ts":1,"text":"point"}]`); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")

	rec := getPublic(t, h, "/api/s/"+created.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("public GET = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Threat Assessment") || !strings.Contains(body, "Lex Clips") {
		t.Fatalf("public DTO missing title/channel: %s", body)
	}
	// Owner-only fields must never appear in the public payload.
	for _, leak := range []string{"media_path", "\"watched\"", "\"favorite\"", "\"category\"", "\"url\"", "\"status\""} {
		if strings.Contains(body, leak) {
			t.Fatalf("public DTO leaked owner field %q: %s", leak, body)
		}
	}
}

func TestShare_publicUnknownToken(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	h := New(deps)
	for _, path := range []string{
		"/api/s/nope",
		"/api/s/nope/stream",
		"/api/s/nope/thumbnail",
		"/api/s/nope/subtitles",
	} {
		if rec := getPublic(t, h, path); rec.Code != http.StatusNotFound {
			t.Fatalf("public GET %s = %d, want 404", path, rec.Code)
		}
	}
}

func TestShare_publicStreamIsWatchOnly(t *testing.T) {
	deps, mediaDir, db := shareTestDeps(t)
	// A real media file + a video row pointing at it.
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mediaPath := filepath.Join(videoDir, "v1.mp4")
	if err := os.WriteFile(mediaPath, []byte("FAKEMP4DATA"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	// media_path has no simple public setter — write it straight to the row.
	if _, err := db.Exec(`UPDATE videos SET media_path = ? WHERE id = ?`, mediaPath, "v1"); err != nil {
		t.Fatalf("set media_path: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")

	// Stream serves the bytes...
	rec := getPublic(t, h, "/api/s/"+created.Token+"/stream")
	if rec.Code != http.StatusOK {
		t.Fatalf("public stream = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "FAKEMP4DATA" {
		t.Fatalf("stream body = %q", rec.Body.String())
	}
	// ...but ?download=1 is ignored — no attachment disposition.
	rec = getPublic(t, h, "/api/s/"+created.Token+"/stream?download=1")
	if cd := rec.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("public stream honored ?download (Content-Disposition = %q); shares are watch-only", cd)
	}
}

func TestShare_statusNotShared(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/share", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	var status shareStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status.Shared || status.Token != "" {
		t.Fatalf("unshared video status = %+v, want shared:false", status)
	}
}

func TestShare_createWithEmptyBodyNeverExpires(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// No ttl in the body → the default (never expires).
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/share", []byte(`{}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST empty body = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp shareStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Shared || resp.ExpiresAt != "" {
		t.Fatalf("empty-body share = %+v, want shared with no expiry", resp)
	}
}

func TestShare_publicVideoCarriesExpiry(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "T", ChannelName: "C"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "7d")

	rec := getPublic(t, h, "/api/s/"+created.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("public GET = %d", rec.Code)
	}
	var dto publicVideoDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dto.ExpiresAt == "" {
		t.Fatal("public DTO should carry the link expiry for a time-limited share")
	}
}

func TestShare_publicThumbnailAndSubtitles(t *testing.T) {
	deps, mediaDir, db := shareTestDeps(t)
	videoDir := filepath.Join(mediaDir, "chan1", "v1")
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	thumbPath := filepath.Join(videoDir, "v1.jpg")
	if err := os.WriteFile(thumbPath, []byte("JPGDATA"), 0o644); err != nil {
		t.Fatalf("write thumb: %v", err)
	}
	vttPath := filepath.Join(videoDir, "v1.en.vtt")
	if err := os.WriteFile(vttPath, []byte("WEBVTT\n\n"), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := db.Exec(`UPDATE videos SET thumbnail_path = ? WHERE id = ?`, thumbPath, "v1"); err != nil {
		t.Fatalf("set thumbnail: %v", err)
	}
	if err := deps.Videos.SetSubtitle("v1", vttPath, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")

	if rec := getPublic(t, h, "/api/s/"+created.Token+"/thumbnail"); rec.Code != http.StatusOK || rec.Body.String() != "JPGDATA" {
		t.Fatalf("public thumbnail = %d %q", rec.Code, rec.Body.String())
	}
	rec := getPublic(t, h, "/api/s/"+created.Token+"/subtitles")
	if rec.Code != http.StatusOK {
		t.Fatalf("public subtitles = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/vtt; charset=utf-8" {
		t.Fatalf("subtitles Content-Type = %q, want text/vtt", ct)
	}
}

func TestShare_publicMediaMissingIsNotFound(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	// Video with no media/thumbnail/subtitle paths at all.
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")

	for _, suffix := range []string{"/stream", "/thumbnail", "/subtitles"} {
		if rec := getPublic(t, h, "/api/s/"+created.Token+suffix); rec.Code != http.StatusNotFound {
			t.Fatalf("public %s with no file = %d, want 404", suffix, rec.Code)
		}
	}
}

func TestShare_notConfigured(t *testing.T) {
	// Deps without a ShareLinks store: owner endpoints 503, public routes 404.
	deps, _, _ := videosTestDepsDB(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	if rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/share", []byte(`{"ttl":"7d"}`)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST share (unconfigured) = %d, want 503", rec.Code)
	}
	if rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/share", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET share (unconfigured) = %d, want 503", rec.Code)
	}
	if rec := doReq(t, h, cookie, http.MethodDelete, "/api/videos/v1/share", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE share (unconfigured) = %d, want 503", rec.Code)
	}
	if rec := getPublic(t, h, "/api/s/anytoken"); rec.Code != http.StatusNotFound {
		t.Fatalf("public GET (unconfigured) = %d, want 404", rec.Code)
	}
}

func TestShare_storeErrorSurfaces(t *testing.T) {
	deps, _, db := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")

	// Break the store out from under the live handlers.
	if _, err := db.Exec(`DROP TABLE share_links`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/share", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("owner GET after store break = %d, want 500", rec.Code)
	}
	if rec := doReq(t, h, cookie, http.MethodDelete, "/api/videos/v1/share", nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("owner DELETE after store break = %d, want 500", rec.Code)
	}
	if rec := getPublic(t, h, "/api/s/"+created.Token); rec.Code != http.StatusInternalServerError {
		t.Fatalf("public GET after store break = %d, want 500", rec.Code)
	}
}

func TestDeleteVideo_revokesShareLink(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "T"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")

	// The public link works while the video exists.
	if rec := getPublic(t, h, "/api/s/"+created.Token); rec.Code != http.StatusOK {
		t.Fatalf("public GET before delete = %d, want 200", rec.Code)
	}
	// Deleting (tombstoning) the video must revoke the share — otherwise its
	// title/summary/highlights keep being served after a "delete".
	if rec := doReq(t, h, cookie, http.MethodDelete, "/api/videos/v1", nil); rec.Code != http.StatusOK {
		t.Fatalf("DELETE video = %d, want 200", rec.Code)
	}
	if rec := getPublic(t, h, "/api/s/"+created.Token); rec.Code != http.StatusNotFound {
		t.Fatalf("share link still resolves after the video was deleted = %d, want 404", rec.Code)
	}
}
