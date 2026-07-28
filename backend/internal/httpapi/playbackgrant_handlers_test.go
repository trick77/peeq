package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/playbackgrant"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/videos"
)

// grantTestDeps is videosTestDepsDB plus a wired PlaybackGrants store over the
// same DB, so the playback_grants foreign key is satisfied.
func grantTestDeps(t *testing.T) (Deps, string, *sql.DB) {
	t.Helper()
	deps, mediaDir, db := videosTestDepsDB(t)
	deps.PlaybackGrants = playbackgrant.New(db)
	return deps, mediaDir, db
}

// seedPlayableVideo writes a real media file under mediaDir and marks the video
// downloaded, so the stream routes have something to serve.
func seedPlayableVideo(t *testing.T, deps Deps, mediaDir, id, content string) {
	t.Helper()
	videoDir := filepath.Join(mediaDir, "chan1", id)
	if err := os.MkdirAll(videoDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mediaPath := filepath.Join(videoDir, id+".mp4")
	if err := os.WriteFile(mediaPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write media file: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: id, URL: "u", ChannelID: "chan1"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetDownloaded(id, videos.DownloadedResult{MediaPath: mediaPath}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
}

// enableDirectStream flips the opt-in setting on, the way the Settings page does.
func enableDirectStream(t *testing.T, deps Deps, on bool) {
	t.Helper()
	if err := deps.Settings.Update(context.Background(), settings.Patch{DirectStreamEnabled: &on}); err != nil {
		t.Fatalf("set direct_stream_enabled=%v: %v", on, err)
	}
}

// createGrant drives the owner POST and returns the minted URL.
func createGrant(t *testing.T, h http.Handler, cookie *http.Cookie, id string) playbackGrantResponse {
	t.Helper()
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/"+id+"/playback-grant", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST playback-grant status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp playbackGrantResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode grant response: %v", err)
	}
	return resp
}

// The point of the whole feature: a request carrying NO session cookie — what an
// AirPlay receiver sends — gets the media, and gets it with range support.
func TestGrantStream_servesWithoutASession(t *testing.T) {
	deps, mediaDir, _ := grantTestDeps(t)
	seedPlayableVideo(t, deps, mediaDir, "v1", "0123456789")
	enableDirectStream(t, deps, true)

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	grant := createGrant(t, h, cookie, "v1")

	rec := getPublic(t, h, grant.URL)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookieless GET %s = %d, want 200", grant.URL, rec.Code)
	}
	if rec.Body.String() != "0123456789" {
		t.Fatalf("body = %q, want the media file", rec.Body.String())
	}
	if ar := rec.Header().Get("Accept-Ranges"); ar != "bytes" {
		t.Fatalf("Accept-Ranges = %q, want bytes (AirPlay seeks)", ar)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "video/mp4" {
		t.Fatalf("Content-Type = %q, want video/mp4 (AirPlay receivers are strict)", ct)
	}
	// The on-disk filename is "<videoID>.mp4", so any Content-Disposition here
	// would put the YouTube id on the wire.
	if cd := rec.Header().Get("Content-Disposition"); cd != "" {
		t.Fatalf("Content-Disposition = %q, want none — it would leak the video id", cd)
	}
}

// AirPlay seeks by re-requesting byte ranges, so 206 is not optional.
func TestGrantStream_rangeRequest(t *testing.T) {
	deps, mediaDir, _ := grantTestDeps(t)
	seedPlayableVideo(t, deps, mediaDir, "v1", "0123456789")
	enableDirectStream(t, deps, true)

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	grant := createGrant(t, h, cookie, "v1")

	req := httptest.NewRequest(http.MethodGet, grant.URL, nil)
	req.Header.Set("Range", "bytes=0-3")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("GET grant stream (Range) = %d, want 206, body = %s", rec.Code, rec.Body.String())
	}
	if cr := rec.Header().Get("Content-Range"); cr == "" {
		t.Fatal("Content-Range header missing")
	}
	if got := rec.Body.String(); got != "0123" {
		t.Fatalf("body = %q, want %q", got, "0123")
	}
}

// The setting is the kill switch: it is re-read per request, so turning it off
// must strand grants that were already minted and handed to a device.
func TestGrantStream_settingOffRevokesLiveGrants(t *testing.T) {
	deps, mediaDir, _ := grantTestDeps(t)
	seedPlayableVideo(t, deps, mediaDir, "v1", "0123456789")
	enableDirectStream(t, deps, true)

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	grant := createGrant(t, h, cookie, "v1")

	if rec := getPublic(t, h, grant.URL); rec.Code != http.StatusOK {
		t.Fatalf("precondition: GET = %d, want 200", rec.Code)
	}

	enableDirectStream(t, deps, false)

	if rec := getPublic(t, h, grant.URL); rec.Code != http.StatusNotFound {
		t.Fatalf("GET after turning the setting off = %d, want 404", rec.Code)
	}
}

// Default-off: an install that never touches the setting must not have an
// auth-free media route at all.
func TestGrantStream_disabledByDefault(t *testing.T) {
	deps, mediaDir, db := grantTestDeps(t)
	seedPlayableVideo(t, deps, mediaDir, "v1", "0123456789")

	// Mint out of band — the owner endpoint would refuse while the setting is
	// off, and the point here is that even a valid token is inert.
	token, _, err := playbackgrant.New(db).Mint(context.Background(), "v1", playbackgrant.DefaultTTL)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	h := New(deps)
	if rec := getPublic(t, h, "/api/p/"+token+"/stream"); rec.Code != http.StatusNotFound {
		t.Fatalf("GET with the setting at its default = %d, want 404", rec.Code)
	}
}

// Every failure mode is the same bare 404, so a dead grant cannot be told apart
// from one that never existed — the contract resolveShare holds to.
func TestGrantStream_failuresAreIndistinguishable(t *testing.T) {
	deps, mediaDir, db := grantTestDeps(t)
	seedPlayableVideo(t, deps, mediaDir, "v1", "0123456789")
	enableDirectStream(t, deps, true)

	h := New(deps)

	expired, _, err := playbackgrant.New(db).Mint(context.Background(), "v1", playbackgrant.DefaultTTL)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := db.Exec(
		`UPDATE playback_grants SET expires_at = datetime('now', '-1 hour') WHERE token = ?`, expired); err != nil {
		t.Fatalf("age the grant: %v", err)
	}

	var bodies []string
	for _, path := range []string{
		"/api/p/" + expired + "/stream",
		"/api/p/never-existed/stream",
	} {
		rec := getPublic(t, h, path)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] {
		t.Fatalf("expired and unknown tokens returned different bodies: %q vs %q", bodies[0], bodies[1])
	}
}

// A grant names exactly one video. Holding one must not become a way to reach
// another — least of all by swapping in a YouTube id, which is what a peeq
// video id is.
func TestGrantStream_grantIsScopedToOneVideo(t *testing.T) {
	deps, mediaDir, _ := grantTestDeps(t)
	seedPlayableVideo(t, deps, mediaDir, "v1", "first")
	seedPlayableVideo(t, deps, mediaDir, "v2", "second")
	enableDirectStream(t, deps, true)

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	grant := createGrant(t, h, cookie, "v1")

	rec := getPublic(t, h, grant.URL)
	if rec.Body.String() != "first" {
		t.Fatalf("grant for v1 served %q", rec.Body.String())
	}
	// And there is no video-id-shaped public route to fall back on.
	if rec := getPublic(t, h, "/api/videos/v2/stream"); rec.Code == http.StatusOK {
		t.Fatal("the session-gated stream route answered a cookieless request")
	}
}

// Minting is owner-only. Without this, knowing a YouTube id would be enough to
// manufacture an auth-free URL for it.
func TestCreatePlaybackGrant_requiresASession(t *testing.T) {
	deps, mediaDir, _ := grantTestDeps(t)
	seedPlayableVideo(t, deps, mediaDir, "v1", "0123456789")
	enableDirectStream(t, deps, true)

	h := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/api/videos/v1/playback-grant", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("cookieless POST playback-grant = %d, want 401", rec.Code)
	}
}

// The owner gets 409 rather than 404 when the feature is off, so the UI can say
// "you have not enabled this" instead of "this video is missing".
func TestCreatePlaybackGrant_refusedWhenSettingOff(t *testing.T) {
	deps, mediaDir, _ := grantTestDeps(t)
	seedPlayableVideo(t, deps, mediaDir, "v1", "0123456789")

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodPost, "/api/videos/v1/playback-grant", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST playback-grant with the setting off = %d, want 409", rec.Code)
	}
}

// Without a store wired, the public route must reveal nothing at all.
func TestGrantStream_notConfiguredIs404(t *testing.T) {
	deps, mediaDir, _ := videosTestDepsDB(t)
	seedPlayableVideo(t, deps, mediaDir, "v1", "0123456789")
	enableDirectStream(t, deps, true)

	h := New(deps)
	if rec := getPublic(t, h, "/api/p/anything/stream"); rec.Code != http.StatusNotFound {
		t.Fatalf("GET with no grant store = %d, want 404", rec.Code)
	}
}

// The retention sweeper's now-playing guard has to see a grant stream, or it can
// delete the file out from under an Apple TV mid-playback.
func TestGrantStream_recordsStreamAccess(t *testing.T) {
	deps, mediaDir, _ := grantTestDeps(t)
	seedPlayableVideo(t, deps, mediaDir, "v1", "0123456789")
	enableDirectStream(t, deps, true)
	rec := &recordingStreamAccess{}
	deps.StreamAccess = rec

	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	grant := createGrant(t, h, cookie, "v1")
	getPublic(t, h, grant.URL)

	if len(rec.ids) != 1 || rec.ids[0] != "v1" {
		t.Fatalf("RecordAccess calls = %v, want [v1]", rec.ids)
	}
}

type recordingStreamAccess struct{ ids []string }

func (r *recordingStreamAccess) RecordAccess(id string) { r.ids = append(r.ids, id) }
