package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	// Segments are seeded so the nested third-party shape the checks below have
	// to tolerate is actually present in the payload under test — without them
	// the "a segment has a category" reasoning would describe a body that has no
	// segments in it.
	if err := deps.Videos.SetSponsorblockSegments("v1", `[{"category":"sponsor","start_time":30,"end_time":75.5}]`); err != nil {
		t.Fatalf("set segments: %v", err)
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
	// Owner-only fields must never appear in the public payload. "id" and "url"
	// are in here for a second reason on top of being owner-shaped: see
	// TestShare_publicVideoNeverLeaksVideoID.
	//
	// Checked TWO ways. Top-level keys catch the field being added to the DTO;
	// the quoted-substring scan additionally catches it appearing NESTED, inside
	// chapters/key_points/segments, or under another name. Only "category" is
	// exempt from the substring scan: the payload nests a third-party shape whose
	// "category" means the SponsorBlock segment's kind, nothing to do with the
	// owner's category, so there it is a top-level-key check only.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &fields); err != nil {
		t.Fatalf("decode public DTO: %v", err)
	}
	for _, leak := range []string{"watched", "favorite", "category", "url", "status", "id", "media_path", "position_seconds"} {
		if _, ok := fields[leak]; ok {
			t.Fatalf("public DTO leaked owner field %q: %s", leak, body)
		}
	}
	for _, leak := range []string{"\"watched\"", "\"favorite\"", "\"url\"", "\"status\"", "\"id\"", "\"position_seconds\""} {
		if strings.Contains(body, leak) {
			t.Fatalf("public DTO leaked owner field %s anywhere in the body: %s", leak, body)
		}
	}
	// media_path is additionally checked as a raw substring: it is the one field
	// whose VALUE leaking anywhere in the body (nested, or under another name)
	// would be as bad as the key itself.
	if strings.Contains(body, "media_path") {
		t.Fatalf("public DTO mentions media_path: %s", body)
	}
}

// TestShare_publicVideoNeverLeaksVideoID is the guard behind publicVideoDTO's
// omission of the id and url. peeq's video id IS the YouTube id, so leaking it
// anywhere on a public route would name the source video to the recipient and
// hand the chromeless page an identifier it could aim at the session-gated
// /api/videos/{id}/... routes. The share token is the only public identifier.
//
// It checks the bodies AND the headers of every public route, because the way
// this would realistically regress is someone adding a Content-Disposition to
// serveMediaFile: the subtitle file on disk is "<videoID>.<lang>.vtt", so an
// attachment filename would put the id straight on the wire.
func TestShare_publicVideoNeverLeaksVideoID(t *testing.T) {
	deps, mediaDir, db := shareTestDeps(t)
	// A realistic 11-char YouTube id, so a substring check means something.
	const id = "dQw4w9WgXcQ"

	videoDir := filepath.Join(mediaDir, "chan1", id)
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mediaPath := filepath.Join(videoDir, id+".mp4")
	if err := os.WriteFile(mediaPath, []byte("fake-media"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	thumbPath := filepath.Join(videoDir, id+".jpg")
	if err := os.WriteFile(thumbPath, []byte("fake-jpeg"), 0o644); err != nil {
		t.Fatalf("write thumb: %v", err)
	}
	subPath := filepath.Join(videoDir, id+".en.vtt")
	if err := os.WriteFile(subPath, []byte("WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nhello\n"), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{
		ID:          id,
		URL:         "https://www.youtube.com/watch?v=" + id,
		Title:       "Never Gonna Explain It",
		ChannelName: "Chan",
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if _, err := db.Exec(`UPDATE videos SET media_path = ?, thumbnail_path = ? WHERE id = ?`, mediaPath, thumbPath, id); err != nil {
		t.Fatalf("set paths: %v", err)
	}
	if err := deps.Videos.SetSubtitle(id, subPath, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, id, "never")

	for _, path := range []string{
		"/api/s/" + created.Token,
		"/api/s/" + created.Token + "/stream",
		"/api/s/" + created.Token + "/thumbnail",
		"/api/s/" + created.Token + "/subtitles",
	} {
		rec := getPublic(t, h, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("public GET %s = %d, body = %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), id) {
			t.Fatalf("public GET %s leaked the video id in its body: %s", path, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "youtube.com") {
			t.Fatalf("public GET %s leaked the source URL in its body: %s", path, rec.Body.String())
		}
		for name, values := range rec.Header() {
			for _, v := range values {
				if strings.Contains(v, id) {
					t.Fatalf("public GET %s leaked the video id in header %s: %s", path, name, v)
				}
			}
		}
	}
}

// TestShare_publicVideoCarriesChapters pins chapters INTO the public payload —
// the share page renders them, and they were on the wire long before it did.
func TestShare_publicVideoCarriesChapters(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "Chaptered", ChannelName: "Chan"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	chapters := `[{"ts":0,"title":"Cold open","source":"yt-dlp"},{"ts":95,"title":"The argument","source":"mimo"}]`
	if err := deps.Videos.SetSummary("v1", "A short summary.", chapters, "[]"); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")

	rec := getPublic(t, h, "/api/s/"+created.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("public GET = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got publicVideoDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode public DTO: %v", err)
	}
	if string(got.Chapters) != chapters {
		t.Fatalf("public chapters = %s, want %s", got.Chapters, chapters)
	}
}

// TestShare_publicVideoCarriesSponsorblockSegments pins the segment list INTO
// the public payload. Without it the shared player has no way to skip an ad the
// owner's player skips, which is the whole point of showing someone a video.
func TestShare_publicVideoCarriesSponsorblockSegments(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "Sponsored", ChannelName: "Chan"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	segs := `[{"category":"sponsor","start_time":30,"end_time":75.5},{"category":"intro","start_time":0,"end_time":12}]`
	if err := deps.Videos.SetSponsorblockSegments("v1", segs); err != nil {
		t.Fatalf("set segments: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")

	rec := getPublic(t, h, "/api/s/"+created.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("public GET = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got publicVideoDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode public DTO: %v", err)
	}
	if len(got.SponsorblockSegments) != 2 {
		t.Fatalf("public segments = %+v, want 2", got.SponsorblockSegments)
	}
	if got.SponsorblockSegments[0].Category != "sponsor" || got.SponsorblockSegments[0].EndTime != 75.5 {
		t.Fatalf("first segment = %+v", got.SponsorblockSegments[0])
	}
	// The non-skipped category rides along too — the scrubber draws it as a
	// "marked" band and plays through it.
	if got.SponsorblockSegments[1].Category != "intro" {
		t.Fatalf("second segment = %+v", got.SponsorblockSegments[1])
	}
}

// TestShare_publicVideoOmitsEmptySponsorblock keeps the field off the wire
// entirely for the common case, rather than shipping an empty array.
func TestShare_publicVideoOmitsEmptySponsorblock(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "Clean", ChannelName: "Chan"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")

	rec := getPublic(t, h, "/api/s/"+created.Token)
	if strings.Contains(rec.Body.String(), "sponsorblock_segments") {
		t.Fatalf("empty segment list still on the wire: %s", rec.Body.String())
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

// failingShareLinks is a ShareLinkStore whose chosen method returns err; the
// rest delegate to the real store underneath, so a test breaks exactly one call
// and everything around it still behaves. That is what the interface bought:
// these branches used to be reachable only by dropping share_links out from
// under a live store, which broke every query at once and coupled the test to
// the schema.
type failingShareLinks struct {
	real       ShareLinkStore
	upsert     error
	resolve    error
	getByVideo error
	deleteByID error
}

func (f *failingShareLinks) Upsert(ctx context.Context, videoID string, ttl time.Duration) (sharelink.Link, error) {
	if f.upsert != nil {
		return sharelink.Link{}, f.upsert
	}
	return f.real.Upsert(ctx, videoID, ttl)
}

func (f *failingShareLinks) Resolve(ctx context.Context, token string) (string, bool, error) {
	if f.resolve != nil {
		return "", false, f.resolve
	}
	return f.real.Resolve(ctx, token)
}

func (f *failingShareLinks) GetByVideo(ctx context.Context, videoID string) (*sharelink.Link, error) {
	if f.getByVideo != nil {
		return nil, f.getByVideo
	}
	return f.real.GetByVideo(ctx, videoID)
}

func (f *failingShareLinks) DeleteByVideo(ctx context.Context, videoID string) error {
	if f.deleteByID != nil {
		return f.deleteByID
	}
	return f.real.DeleteByVideo(ctx, videoID)
}

// TestShare_storeErrorSurfaces pins that a BROKEN store is a 500 on every route
// that touches it — distinct from a MISSING one, which is a 503 on the owner
// routes and a 404 on the public one (TestShare_unconfigured).
//
// One subtest per failing method, which the drop-a-table version could not do:
// dropping the table failed Resolve, GetByVideo and DeleteByVideo together, so
// a handler calling the wrong one still went green.
func TestShare_storeErrorSurfaces(t *testing.T) {
	setup := func(t *testing.T) (Deps, *failingShareLinks) {
		t.Helper()
		deps, _, _ := shareTestDeps(t)
		if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
			t.Fatalf("seed video: %v", err)
		}
		return deps, &failingShareLinks{real: deps.ShareLinks}
	}

	t.Run("ownerGet_getByVideoFails", func(t *testing.T) {
		deps, fake := setup(t)
		fake.getByVideo = errors.New("boom")
		deps.ShareLinks = fake
		h := New(deps)
		cookie := loginAndGetCookie(t, h)
		if rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/share", nil); rec.Code != http.StatusInternalServerError {
			t.Fatalf("owner GET with failing GetByVideo = %d, want 500", rec.Code)
		}
	})

	t.Run("ownerDelete_deleteFails", func(t *testing.T) {
		deps, fake := setup(t)
		fake.deleteByID = errors.New("boom")
		deps.ShareLinks = fake
		h := New(deps)
		cookie := loginAndGetCookie(t, h)
		if rec := doReq(t, h, cookie, http.MethodDelete, "/api/videos/v1/share", nil); rec.Code != http.StatusInternalServerError {
			t.Fatalf("owner DELETE with failing DeleteByVideo = %d, want 500", rec.Code)
		}
	})

	t.Run("publicGet_resolveFails", func(t *testing.T) {
		deps, fake := setup(t)
		fake.resolve = errors.New("boom")
		deps.ShareLinks = fake
		h := New(deps)
		if rec := getPublic(t, h, "/api/s/sometoken"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("public GET with failing Resolve = %d, want 500", rec.Code)
		}
	})
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

func TestShare_relativeURLWithoutPublicURL(t *testing.T) {
	deps, _, db := videosTestDepsDB(t)
	deps.ShareLinks = sharelink.New(db)
	deps.PublicURL = "" // no external base configured (dev default)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")
	if !strings.HasPrefix(created.URL, "/s/") {
		t.Fatalf("URL without PublicURL = %q, want a relative /s/ path", created.URL)
	}
}

func TestShare_createStoreErrorIs500(t *testing.T) {
	deps, _, _ := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	deps.ShareLinks = &failingShareLinks{real: deps.ShareLinks, upsert: errors.New("boom")}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/share", []byte(`{"ttl":"7d"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST create with broken store = %d, want 500", rec.Code)
	}
}

func TestShare_publicStreamPathSafety(t *testing.T) {
	deps, mediaDir, db := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	// A media_path that escapes mediaDir must be rejected by SafeMediaPath.
	outside := filepath.Join(t.TempDir(), "secret.mp4")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	traversal := filepath.Join(mediaDir, "..", filepath.Base(filepath.Dir(outside)), "secret.mp4")
	if _, err := db.Exec(`UPDATE videos SET media_path = ? WHERE id = ?`, traversal, "v1"); err != nil {
		t.Fatalf("set media_path: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")

	rec := getPublic(t, h, "/api/s/"+created.Token+"/stream")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("public stream via traversal = %d, want 404", rec.Code)
	}
	if rec.Body.String() == "secret" {
		t.Fatal("public stream served an out-of-tree file")
	}
}

func TestShare_publicStreamMissingFile(t *testing.T) {
	deps, mediaDir, db := shareTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	// A path that is safe (under mediaDir) but points at a file that isn't
	// there: SafeMediaPath passes, os.Open fails.
	if err := os.MkdirAll(filepath.Join(mediaDir, "chan1"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	missing := filepath.Join(mediaDir, "chan1", "gone.mp4")
	if _, err := db.Exec(`UPDATE videos SET media_path = ? WHERE id = ?`, missing, "v1"); err != nil {
		t.Fatalf("set media_path: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	created := createShare(t, h, cookie, "v1", "never")

	if rec := getPublic(t, h, "/api/s/"+created.Token+"/stream"); rec.Code != http.StatusNotFound {
		t.Fatalf("public stream of a missing file = %d, want 404", rec.Code)
	}
}
