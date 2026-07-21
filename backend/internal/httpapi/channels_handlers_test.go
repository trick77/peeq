package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// testResolver is a ChannelResolver whose ResolveChannel behavior is
// scripted per test, so these tests never shell out to yt-dlp.
type testResolver struct {
	info  ytdlp.ChannelInfo
	err   error
	calls int
}

func (r *testResolver) ResolveChannel(ctx context.Context, url string) (ytdlp.ChannelInfo, error) {
	r.calls++
	if r.err != nil {
		return ytdlp.ChannelInfo{}, r.err
	}
	return r.info, nil
}

// channelsTestDeps builds Deps wired for the channels API: dev auth plus
// real channels, videos, and ledger stores sharing ONE *sql.DB (so a video
// seeded through one store is visible through another), and the given fake
// resolver.
func channelsTestDeps(t *testing.T, resolver ChannelResolver) Deps {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	return Deps{
		AuthService:     auth.NewService(nil, sessions, users),
		AuthMiddleware:  auth.NewMiddleware(sessions, users),
		Settings:        settings.New(db),
		Channels:        channels.New(db),
		Videos:          videos.New(db),
		Ledger:          channelvideos.New(db),
		ChannelResolver: resolver,
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
}

func newChannelsTestServer(t *testing.T, resolver ChannelResolver) http.Handler {
	t.Helper()
	return New(channelsTestDeps(t, resolver))
}

func postJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.AddCookie(loginAndGetCookie(t, h))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// postJSONWithCookie is like postJSON but reuses an existing session cookie,
// so a test can chain multiple authenticated requests against one handler.
func postJSONWithCookie(t *testing.T, h http.Handler, cookie *http.Cookie, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func putJSONWithCookie(t *testing.T, h http.Handler, cookie *http.Cookie, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(b))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getJSON(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(loginAndGetCookie(t, h))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body = %s", path, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// TestChannelsPost_tracksAndSubscribes asserts POST /api/channels tracks the
// resolved channel and, when subscribe:true is passed, also subscribes it.
func TestChannelsPost_tracksAndSubscribes(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCxyz", Name: "My Channel"}})
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@x", "subscribe": true})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	// It is now tracked AND subscribed.
	list := getJSON(t, h, "/api/channels?filter=subscribed")
	if !strings.Contains(list, "UCxyz") {
		t.Fatalf("subscribed list missing channel: %s", list)
	}
}

// TestChannelsPost_noCookie_409 asserts a ResolveChannel failure with
// ErrNoCookie is surfaced as 409, not a silent failure or 500.
func TestChannelsPost_noCookie_409(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{err: ytdlp.ErrNoCookie})
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@x"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
}

// TestChannelsPost_trackOnly_notSubscribed asserts subscribe:false (or
// omitted) only tracks the channel — it does not appear in the subscribed
// filter, only in tracked/all.
func TestChannelsPost_trackOnly_notSubscribed(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCtrackonly", Name: "Track Only"}})
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@trackonly"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	subscribed := getJSON(t, h, "/api/channels?filter=subscribed")
	if strings.Contains(subscribed, "UCtrackonly") {
		t.Fatalf("track-only channel must not appear in subscribed filter: %s", subscribed)
	}
	tracked := getJSON(t, h, "/api/channels?filter=tracked")
	if !strings.Contains(tracked, "UCtrackonly") {
		t.Fatalf("tracked filter missing channel: %s", tracked)
	}
}

// TestChannelsPost_notAChannelURL_400 asserts a video url is rejected with
// 400 before ever reaching the resolver.
func TestChannelsPost_notAChannelURL_400(t *testing.T) {
	resolver := &testResolver{info: ytdlp.ChannelInfo{UCID: "UCxyz", Name: "x"}}
	h := newChannelsTestServer(t, resolver)
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
	if resolver.calls != 0 {
		t.Fatalf("resolver calls = %d, want 0 (non-channel url must be rejected before resolve)", resolver.calls)
	}
}

// TestChannelsPost_multiSegmentHandle_trimmed is the Task 2 review
// hardening: a pasted /@Handle/videos url must store the bare @Handle, not
// the trailing path segment.
func TestChannelsPost_multiSegmentHandle_trimmed(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UChandle", Name: "Handle Channel"}})
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@Handle/videos"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	list := getJSON(t, h, "/api/channels?filter=all")
	if !strings.Contains(list, `"handle":"@Handle"`) {
		t.Fatalf("handle not trimmed to @Handle: %s", list)
	}
	if strings.Contains(list, "videos") {
		t.Fatalf("handle leaked trailing path segment: %s", list)
	}
}

// TestChannelsList_unconfigured_503 covers the fail-safe default: no
// Channels store wired means the endpoint reports unavailable rather than
// the empty list a nil-safe-but-silent implementation would return —
// mirroring how every other optional dependency in this package (worker,
// ytdlp, sseHub) reports 503 when unconfigured.
func TestChannelsList_unconfigured_503(t *testing.T) {
	h := New(testDeps(t))
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodGet, "/api/channels", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /api/channels (unconfigured) status = %d, want 503, body = %s", rec.Code, rec.Body.String())
	}
}

// TestChannelsList_invalidFilter_400 asserts an unrecognized ?filter= value
// is rejected with 400 before ever reaching the store, distinguishing a bad
// request from a genuine store error (which would be a 500).
func TestChannelsList_invalidFilter_400(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/channels?filter=bogus", nil)
	req.AddCookie(loginAndGetCookie(t, h))
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("GET /api/channels?filter=bogus status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsPost_reAddSubscribed_reportsTrue asserts the response reports
// the real post-condition rather than echoing req.Subscribe. Re-adding an
// already-subscribed channel with subscribe=false is idempotent and leaves
// the subscription intact, so claiming "subscribed": false would tell the
// UI to print "not subscribed" about a channel that still gets scanned.
func TestChannelsPost_reAddSubscribed_reportsTrue(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCreadd", Name: "Re Add"}})
	cookie := loginAndGetCookie(t, h)
	if rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "https://www.youtube.com/@readd", "subscribe": true}); rr.Code != http.StatusCreated {
		t.Fatalf("first add status = %d body=%s", rr.Code, rr.Body.String())
	}

	rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "https://www.youtube.com/@readd", "subscribe": false})
	if rr.Code != http.StatusCreated {
		t.Fatalf("re-add status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Subscribed bool `json:"subscribed"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if !got.Subscribed {
		t.Fatalf("re-adding a subscribed channel reported subscribed=false; body=%s", rr.Body.String())
	}
	// And the subscription really did survive the re-add.
	if list := getJSON(t, h, "/api/channels?filter=subscribed"); !strings.Contains(list, "UCreadd") {
		t.Fatalf("subscription lost on re-add: %s", list)
	}
}

// TestChannelsList_autodownloadFilter asserts the "autodownload" filter is
// accepted by the handler and narrows to subscribed-with-autodownload-on
// channels only.
func TestChannelsList_autodownloadFilter(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCauto", Name: "Auto"}})
	cookie := loginAndGetCookie(t, h)
	if rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "https://www.youtube.com/@auto", "subscribe": true}); rr.Code != http.StatusCreated {
		t.Fatalf("track status = %d body=%s", rr.Code, rr.Body.String())
	}

	// Subscribed but autodownload still off: must not be listed.
	if list := getJSON(t, h, "/api/channels?filter=autodownload"); strings.Contains(list, "UCauto") {
		t.Fatalf("autodownload filter must exclude a channel with autodownload off: %s", list)
	}

	if rr := putJSONWithCookie(t, h, cookie, "/api/channels/UCauto", map[string]any{"autodownload": true}); rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", rr.Code, rr.Body.String())
	}
	if list := getJSON(t, h, "/api/channels?filter=autodownload"); !strings.Contains(list, "UCauto") {
		t.Fatalf("autodownload filter missing channel: %s", list)
	}
}

// TestChannelsPut_notSubscribed_400 asserts updating config on a channel
// that is tracked but not subscribed is a clean 400, not a silent no-op.
func TestChannelsPut_notSubscribed_400(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCnotsub", Name: "Not Sub"}})
	cookie := loginAndGetCookie(t, h)
	if rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "https://www.youtube.com/@notsub"}); rr.Code != http.StatusCreated {
		t.Fatalf("track status = %d body=%s", rr.Code, rr.Body.String())
	}
	autodl := true
	rr := putJSONWithCookie(t, h, cookie, "/api/channels/UCnotsub", map[string]any{"autodownload": autodl})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsPutSubscribeUnsubscribe covers the full config + subscribe/
// unsubscribe lifecycle for a tracked channel.
func TestChannelsPutSubscribeUnsubscribe(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UClifecycle", Name: "Lifecycle"}})
	cookie := loginAndGetCookie(t, h)
	if rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "https://www.youtube.com/@lifecycle"}); rr.Code != http.StatusCreated {
		t.Fatalf("track status = %d body=%s", rr.Code, rr.Body.String())
	}

	// Subscribe.
	rr := postJSONWithCookie(t, h, cookie, "/api/channels/UClifecycle/subscribe", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("subscribe status = %d body=%s", rr.Code, rr.Body.String())
	}
	subscribed := getJSON(t, h, "/api/channels?filter=subscribed")
	if !strings.Contains(subscribed, "UClifecycle") {
		t.Fatalf("subscribed list missing channel: %s", subscribed)
	}

	// Now PUT config should succeed.
	rr = putJSONWithCookie(t, h, cookie, "/api/channels/UClifecycle", map[string]any{"autodownload": true, "format_override": "bestvideo+bestaudio"})
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	updated := getJSON(t, h, "/api/channels?filter=subscribed")
	if !strings.Contains(updated, `"autodownload":true`) || !strings.Contains(updated, "bestvideo+bestaudio") {
		t.Fatalf("config update not reflected: %s", updated)
	}

	// Unsubscribe.
	rr = postJSONWithCookie(t, h, cookie, "/api/channels/UClifecycle/unsubscribe", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("unsubscribe status = %d body=%s", rr.Code, rr.Body.String())
	}
	afterUnsub := getJSON(t, h, "/api/channels?filter=subscribed")
	if strings.Contains(afterUnsub, "UClifecycle") {
		t.Fatalf("channel still subscribed after unsubscribe: %s", afterUnsub)
	}
	tracked := getJSON(t, h, "/api/channels?filter=tracked")
	if !strings.Contains(tracked, "UClifecycle") {
		t.Fatalf("channel should remain tracked after unsubscribe: %s", tracked)
	}
}

// TestChannelsPut_partialUpdate_preservesOtherField asserts a PUT that sets
// only one of autodownload/format_override leaves the other column exactly
// as it was, in both directions. Before the fix this was implemented by
// reading the row and merging in Go; the atomic COALESCE update must
// preserve the same observable behaviour for a single, uncontested request.
func TestChannelsPut_partialUpdate_preservesOtherField(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCpartial", Name: "Partial"}})
	cookie := loginAndGetCookie(t, h)
	if rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "https://www.youtube.com/@partial", "subscribe": true}); rr.Code != http.StatusCreated {
		t.Fatalf("track+subscribe status = %d body=%s", rr.Code, rr.Body.String())
	}

	// Seed both fields with a full PUT.
	rr := putJSONWithCookie(t, h, cookie, "/api/channels/UCpartial", map[string]any{"autodownload": true, "format_override": "bestvideo+bestaudio"})
	if rr.Code != http.StatusOK {
		t.Fatalf("seed PUT status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}

	// Partial PUT: only autodownload. format_override must survive untouched.
	rr = putJSONWithCookie(t, h, cookie, "/api/channels/UCpartial", map[string]any{"autodownload": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT (autodownload only) status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"autodownload":false`) || !strings.Contains(rr.Body.String(), `"format_override":"bestvideo+bestaudio"`) {
		t.Fatalf("PUT (autodownload only) response = %s, want format_override preserved", rr.Body.String())
	}

	// Partial PUT: only format_override. autodownload must survive untouched.
	rr = putJSONWithCookie(t, h, cookie, "/api/channels/UCpartial", map[string]any{"format_override": "worst"})
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT (format_override only) status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"autodownload":false`) || !strings.Contains(rr.Body.String(), `"format_override":"worst"`) {
		t.Fatalf("PUT (format_override only) response = %s, want autodownload preserved", rr.Body.String())
	}
}

// TestChannels_requireAuth asserts every channels route is behind
// requireAuth.
func TestChannels_requireAuth(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{})

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/channels", bytes.NewReader([]byte("{}"))),
		httptest.NewRequest(http.MethodGet, "/api/channels", nil),
		httptest.NewRequest(http.MethodPut, "/api/channels/UC1", bytes.NewReader([]byte("{}"))),
		httptest.NewRequest(http.MethodDelete, "/api/channels/UC1", nil),
		httptest.NewRequest(http.MethodPost, "/api/channels/UC1/subscribe", nil),
		httptest.NewRequest(http.MethodPost, "/api/channels/UC1/unsubscribe", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", req.Method, req.URL.Path, rec.Code)
		}
	}
}

// TestChannelDetail_untrackedChannel_200 asserts a channel that exists only
// because the user downloaded one of its videos by URL still has a page.
// This is the whole point of splitting the cache from the tracking list.
func TestChannelDetail_untrackedChannel_200(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	h := New(deps)
	seedVideoRow(t, deps, "v1", "UCloose", "Deep Field Radio")

	body := getJSON(t, h, "/api/channels/UCloose")
	if !strings.Contains(body, `"tracked":false`) {
		t.Fatalf("want tracked:false, got %s", body)
	}
	if !strings.Contains(body, "Deep Field Radio") {
		t.Fatalf("want the channel name from its videos, got %s", body)
	}
}

// TestChannelDetail_stats asserts the four header numbers count only
// downloaded videos — a queued or errored row is not on disk and must not be
// claimed as archived.
func TestChannelDetail_stats(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	h := New(deps)
	seedVideoRowFull(t, deps, seedVideo{ID: "v1", ChannelID: "UCa", ChannelName: "A", Status: "downloaded", Duration: 600, Bytes: 1000})
	seedVideoRowFull(t, deps, seedVideo{ID: "v2", ChannelID: "UCa", ChannelName: "A", Status: "downloaded", Duration: 300, Bytes: 2000})
	seedVideoRowFull(t, deps, seedVideo{ID: "v3", ChannelID: "UCa", ChannelName: "A", Status: "queued", Duration: 999, Bytes: 9999})

	body := getJSON(t, h, "/api/channels/UCa")
	if !strings.Contains(body, `"archived_count":2`) {
		t.Fatalf("want archived_count 2, got %s", body)
	}
	if !strings.Contains(body, `"runtime_seconds":900`) {
		t.Fatalf("want runtime_seconds 900, got %s", body)
	}
	if !strings.Contains(body, `"disk_bytes":3000`) {
		t.Fatalf("want disk_bytes 3000, got %s", body)
	}
}

// TestChannelDetail_unknownChannel_404 asserts an id matching neither a
// cached channel nor any video is a 404, not an empty page.
func TestChannelDetail_unknownChannel_404(t *testing.T) {
	h := New(channelsTestDeps(t, &testResolver{}))
	req := httptest.NewRequest(http.MethodGet, "/api/channels/UCnope", nil)
	req.AddCookie(loginAndGetCookie(t, h))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestChannelDetail_untrackedChannel_onlyQueuedVideos_200 asserts the page
// exists for a channel whose videos have not finished downloading. The stats
// count only downloaded videos, so archived_count is 0 here — but the channel
// is plainly real, and 404ing it would make the page flap to 200 the moment
// the download completed.
func TestChannelDetail_untrackedChannel_onlyQueuedVideos_200(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	h := New(deps)
	seedVideoRowFull(t, deps, seedVideo{ID: "v1", ChannelID: "UCq", ChannelName: "Queued Channel", Status: "queued"})

	body := getJSON(t, h, "/api/channels/UCq")
	if !strings.Contains(body, `"tracked":false`) {
		t.Fatalf("want tracked:false, got %s", body)
	}
	if !strings.Contains(body, `"archived_count":0`) {
		t.Fatalf("want archived_count 0, got %s", body)
	}
	if !strings.Contains(body, "Queued Channel") {
		t.Fatalf("want the channel name from its videos, got %s", body)
	}
}

// TestChannelDetail_subscribed_includesSchedule asserts the page can tell the
// user when peeq last checked and when it will check next.
func TestChannelDetail_subscribed_includesSchedule(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCs", Name: "Subbed"}})
	h := New(deps)
	rec := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@s", "subscribe": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup: status %d body %s", rec.Code, rec.Body.String())
	}

	body := getJSON(t, h, "/api/channels/UCs")
	for _, want := range []string{`"tracked":true`, `"subscribed":true`, `"next_scan_at"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("want %s in %s", want, body)
		}
	}
}

type seedVideo struct {
	ID          string
	ChannelID   string
	ChannelName string
	Status      string
	Duration    int
	Bytes       int64
	PublishedAt string
}

func seedVideoRowFull(t *testing.T, deps Deps, v seedVideo) {
	t.Helper()
	_, err := deps.Channels.DB().Exec(`
INSERT INTO videos (id, url, title, channel_id, channel_name, duration_seconds, filesize_bytes, status, published_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, "https://y/"+v.ID, "title "+v.ID, v.ChannelID, v.ChannelName,
		v.Duration, v.Bytes, v.Status, v.PublishedAt)
	if err != nil {
		t.Fatalf("seed video %s: %v", v.ID, err)
	}
}

func seedVideoRow(t *testing.T, deps Deps, id, channelID, channelName string) {
	t.Helper()
	seedVideoRowFull(t, deps, seedVideo{ID: id, ChannelID: channelID, ChannelName: channelName, Status: "downloaded"})
}

// TestDownloadsPost_doesNotTrackChannel asserts adding a single video via
// POST /api/downloads leaves the Channels view untouched. Tracking is an
// explicit action on the Channels page; grabbing one video must not
// silently subscribe the user to its channel.
func TestDownloadsPost_doesNotTrackChannel(t *testing.T) {
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	runner := &fakeDownloadsRunner{meta: &ytdlp.Meta{
		ID:      "vid00000001",
		Title:   "Auto Tracked Video",
		Channel: "Auto",
		// Video ids must be exactly 11 chars per ytdlp.Canonicalize; a
		// real channel_id from yt-dlp always has a "UC" prefix.
		ChannelID: "UCauto000000000000000000",
	}}
	deps := Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		Jobs:           jobs.New(db),
		Videos:         videos.New(db),
		Channels:       channels.New(db),
		Runner:         runner,
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)

	rr := postDownload(t, h, sessionCookie, "https://youtu.be/vid00000001")
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	list := getJSON(t, h, "/api/channels?filter=tracked")
	if strings.Contains(list, "UCauto") {
		t.Fatalf("channel was tracked by a plain video download: %s", list)
	}
}

// pendingTestHarness wires the pending API's full dependency set (channels,
// ledger, videos, jobs) against one shared db, and embeds http.Handler so it
// can be passed directly to the getJSON/postJSON request helpers.
type pendingTestHarness struct {
	http.Handler
	channels *channels.Store
	ledger   *channelvideos.Store
	videos   *videos.Store
	jobs     *jobs.Store
}

// seedChannel tracks id as a bare channel row, satisfying channel_videos'
// foreign key on channel_id before a ledger entry referencing it is
// inserted.
func (h *pendingTestHarness) seedChannel(id string) {
	if err := h.channels.Upsert(channels.Channel{ID: id, Name: id}); err != nil {
		panic(err)
	}
}

// newPendingTestServer builds a pendingTestHarness with dev auth plus real
// channels, ledger, videos, and jobs stores — everything the pending API
// needs, wired against one db so a promoted download is visible across all
// of them.
func newPendingTestServer(t *testing.T) *pendingTestHarness {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	channelsStore := channels.New(db)
	ledgerStore := channelvideos.New(db)
	videosStore := videos.New(db)
	jobsStore := jobs.New(db)
	deps := Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		Channels:       channelsStore,
		Ledger:         ledgerStore,
		Videos:         videosStore,
		Jobs:           jobsStore,
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
	return &pendingTestHarness{
		Handler:  New(deps),
		channels: channelsStore,
		ledger:   ledgerStore,
		videos:   videosStore,
		jobs:     jobsStore,
	}
}

// recordingWorker is a DownloadsWorker that records which job ids Cancel was
// called with, so the delete-channel test can assert a mid-download job was
// cancelled before the rows were removed. The mutex keeps -race quiet even
// though the handler calls Cancel synchronously.
type recordingWorker struct {
	mu       sync.Mutex
	canceled map[int64]bool
}

func newRecordingWorker() *recordingWorker {
	return &recordingWorker{canceled: map[int64]bool{}}
}

func (w *recordingWorker) Cancel(id int64) bool {
	w.mu.Lock()
	w.canceled[id] = true
	w.mu.Unlock()
	return true
}
func (w *recordingWorker) Resume()       {}
func (w *recordingWorker) Paused() bool  { return false }
func (w *recordingWorker) LowDisk() bool { return false }

// channelsDeleteHarness wires the delete-channel API's full dependency set
// (channels, videos, jobs) against one shared db plus a recording fake worker,
// and embeds http.Handler so it can drive requests directly.
type channelsDeleteHarness struct {
	http.Handler
	channels *channels.Store
	videos   *videos.Store
	jobs     *jobs.Store
	worker   *recordingWorker
}

func (h *channelsDeleteHarness) seedChannel(id string) {
	if err := h.channels.Upsert(channels.Channel{ID: id, Name: id}); err != nil {
		panic(err)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := h.channels.Track(id, now); err != nil {
		panic(err)
	}
}

// seedDownloadedVideo inserts a downloaded video row for the channel with the
// given local media path, so a delete has a file ref to unlink.
func (h *channelsDeleteHarness) seedDownloadedVideo(channelID, videoID, mediaPath string) {
	seedDownloadedVideo(h.videos, channelID, videoID, mediaPath)
}

// seedDownloadedVideo inserts a downloaded video row for the channel with the
// given local media path. Shared by any harness/deps that expose a
// *videos.Store, so a delete-vs-data-loss test always has a real video row.
func seedDownloadedVideo(store *videos.Store, channelID, videoID, mediaPath string) {
	if err := store.Upsert(videos.Video{ID: videoID, URL: "u", ChannelID: channelID}); err != nil {
		panic(err)
	}
	if err := store.SetDownloaded(videoID, videos.DownloadedResult{MediaPath: mediaPath}); err != nil {
		panic(err)
	}
}

func newChannelsDeleteServer(t *testing.T) *channelsDeleteHarness {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	channelsStore := channels.New(db)
	videosStore := videos.New(db)
	jobsStore := jobs.New(db)
	worker := newRecordingWorker()
	deps := Deps{
		AuthService:    auth.NewService(nil, sessions, users),
		AuthMiddleware: auth.NewMiddleware(sessions, users),
		Settings:       settings.New(db),
		Channels:       channelsStore,
		Videos:         videosStore,
		Jobs:           jobsStore,
		Worker:         worker,
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
	return &channelsDeleteHarness{
		Handler:  New(deps),
		channels: channelsStore,
		videos:   videosStore,
		jobs:     jobsStore,
		worker:   worker,
	}
}

func doDelete(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	req.AddCookie(loginAndGetCookie(t, h))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestChannelsDelete_cacheOnlyRow_404 asserts a channel that exists only as a
// metadata cache entry cannot be deleted. DeleteCascade destroys every video
// belonging to a channel, including favorited ones — reaching it for a
// channel the user never tracked would be data loss from a page they merely
// visited. A video is seeded up front so this test actually catches that
// data loss, rather than merely observing the 404 and the (possibly already
// emptied) channel row.
func TestChannelsDelete_cacheOnlyRow_404(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCx", Name: "X"}})
	h := New(deps)
	if err := deps.Channels.Upsert(channels.Channel{ID: "UCcache", Name: "Cache Only"}); err != nil {
		t.Fatalf("seed cache row: %v", err)
	}
	seedDownloadedVideo(deps.Videos, "UCcache", "v1", "/tmp/does-not-matter.mp4")

	req := httptest.NewRequest(http.MethodDelete, "/api/channels/UCcache", nil)
	req.AddCookie(loginAndGetCookie(t, h))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
	c, err := deps.Channels.Get("UCcache")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c == nil {
		t.Fatal("delete removed a cache-only channel row")
	}
	v, err := deps.Videos.Get("v1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v == nil {
		t.Fatal("delete destroyed the cache-only channel's video row")
	}
}

// TestChannelsSubscribe_cacheOnlyRow_404 asserts subscribing requires an
// explicitly tracked channel — a cache row is not enough, or visiting a
// channel page would make it subscribable without ever tracking it.
func TestChannelsSubscribe_cacheOnlyRow_404(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCx", Name: "X"}})
	h := New(deps)
	if err := deps.Channels.Upsert(channels.Channel{ID: "UCcache", Name: "Cache Only"}); err != nil {
		t.Fatalf("seed cache row: %v", err)
	}

	rec := postJSON(t, h, "/api/channels/UCcache/subscribe", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

// TestChannelsResubscribe_cacheOnlyRow_404 asserts resubscribe, like
// subscribe, requires an explicitly tracked channel. A cache row is written
// merely by visiting a channel page, so accepting one here would conjure a
// subscription for a channel the user never added.
func TestChannelsResubscribe_cacheOnlyRow_404(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCx", Name: "X"}})
	h := New(deps)
	if err := deps.Channels.Upsert(channels.Channel{ID: "UCcache", Name: "Cache Only"}); err != nil {
		t.Fatalf("seed cache row: %v", err)
	}

	rec := postJSON(t, h, "/api/channels/UCcache/resubscribe", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
	sub, err := deps.Channels.GetSubscription("UCcache")
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}
	if sub != nil {
		t.Fatal("resubscribe created a subscription for a cache-only channel")
	}
}

// TestChannelsDelete_cancelsAndCascades asserts DELETE /api/channels/{id}
// cancels any active job for the channel's videos (killing a live download)
// and then removes the channel and everything belonging to it.
func TestChannelsDelete_cancelsAndCascades(t *testing.T) {
	h := newChannelsDeleteServer(t)
	h.seedChannel("UC1")
	h.seedDownloadedVideo("UC1", "v1", "/tmp/does-not-matter.mp4")
	jid, _ := h.jobs.Enqueue("v1", 0) // pending job for a channel video

	rr := doDelete(t, h, "/api/channels/UC1")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !h.worker.canceled[jid] {
		t.Fatalf("worker.Cancel(%d) was not called", jid)
	}
	if c, _ := h.channels.Get("UC1"); c != nil {
		t.Fatal("channel still present after delete")
	}
}

// TestPending_listDownloadIgnore covers the full pending lifecycle: a scan's
// pending entries appear in GET /api/pending, downloading one promotes it
// to a queued videos row plus a manual-priority job and drops it from the
// pending list, and ignoring another also drops it from the pending list
// without ever creating a videos row.
func TestPending_listDownloadIgnore(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if err := h.ledger.Insert(channelvideos.Entry{VideoID: "p1", ChannelID: "UC1", Title: "A", URL: "https://www.youtube.com/watch?v=p1", DurationSeconds: 600, State: "pending"}); err != nil {
		t.Fatalf("insert p1: %v", err)
	}
	if err := h.ledger.Insert(channelvideos.Entry{VideoID: "p2", ChannelID: "UC1", Title: "B", URL: "https://www.youtube.com/watch?v=p2", DurationSeconds: 600, State: "pending"}); err != nil {
		t.Fatalf("insert p2: %v", err)
	}

	if body := getJSON(t, h, "/api/pending"); !strings.Contains(body, "p1") || !strings.Contains(body, "p2") {
		t.Fatalf("pending list = %s", body)
	}
	// Download p1 -> videos row queued + job at priority 10 + ledger no longer pending.
	if rr := postJSON(t, h, "/api/pending/p1/download", nil); rr.Code != http.StatusOK {
		t.Fatalf("download status = %d", rr.Code)
	}
	v, _ := h.videos.Get("p1")
	if v == nil || v.Status != "queued" {
		t.Fatalf("p1 video = %+v", v)
	}
	jl, _ := h.jobs.List()
	if len(jl) != 1 || jl[0].VideoID != "p1" || jl[0].Priority != 10 {
		t.Fatalf("jobs = %+v", jl)
	}
	// Ignore p2 -> gone from pending.
	if rr := postJSON(t, h, "/api/pending/p2/ignore", nil); rr.Code != http.StatusOK {
		t.Fatalf("ignore status = %d", rr.Code)
	}
	if body := getJSON(t, h, "/api/pending"); strings.Contains(body, "p2") || strings.Contains(body, "p1") {
		t.Fatalf("pending should be empty: %s", body)
	}
}

// TestPending_unconfigured_503 mirrors handleChannelsList's nil-503
// contract: with no Ledger store wired, list/download/ignore must all
// report unavailable rather than a silently-empty list or a 404.
func TestPending_unconfigured_503(t *testing.T) {
	h := New(testDeps(t))
	cookie := loginAndGetCookie(t, h)

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/pending", nil),
		httptest.NewRequest(http.MethodPost, "/api/pending/p1/download", nil),
		httptest.NewRequest(http.MethodPost, "/api/pending/p1/ignore", nil),
	} {
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s (unconfigured) status = %d, want 503, body = %s", req.Method, req.URL.Path, rec.Code, rec.Body.String())
		}
	}
}

// TestPendingDownload_notPending_404 asserts downloading a video id that
// isn't in the ledger at all (or already moved past 'pending') is a clean
// 404, not a silent success or a 500.
func TestPendingDownload_notPending_404(t *testing.T) {
	h := newPendingTestServer(t)
	rr := postJSON(t, h, "/api/pending/nope/download", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("download unknown id status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

// TestPendingDownload_alreadyDownloaded_noDuplicate proves FIX 4: a video that
// was already downloaded while still sitting on the Pending list must NOT be
// re-enqueued on Download-now. Instead it is cleared from Pending and reported
// as already downloaded, with no new job created.
func TestPendingDownload_alreadyDownloaded_noDuplicate(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	// A downloaded videos row for the same id that a pending ledger row points at.
	if err := h.videos.Upsert(videos.Video{ID: "p1", URL: "https://www.youtube.com/watch?v=p1", ChannelID: "UC1"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	if err := h.videos.SetDownloaded("p1", videos.DownloadedResult{MediaPath: "/tmp/p1.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := h.ledger.Insert(channelvideos.Entry{VideoID: "p1", ChannelID: "UC1", Title: "A", URL: "https://www.youtube.com/watch?v=p1", DurationSeconds: 600, State: "pending"}); err != nil {
		t.Fatalf("insert p1: %v", err)
	}

	rr := postJSON(t, h, "/api/pending/p1/download", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("download status = %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "already_downloaded") {
		t.Fatalf("body = %s, want already_downloaded", rr.Body.String())
	}
	// No new job may have been enqueued.
	if jl, _ := h.jobs.List(); len(jl) != 0 {
		t.Fatalf("jobs = %+v, want none (already-downloaded must not re-enqueue)", jl)
	}
	// The video row must remain 'downloaded' (not flipped back to queued).
	if v, _ := h.videos.Get("p1"); v == nil || v.Status != "downloaded" {
		t.Fatalf("video = %+v, want status downloaded", v)
	}
	// It must be cleared from Pending.
	if body := getJSON(t, h, "/api/pending"); strings.Contains(body, "p1") {
		t.Fatalf("p1 should be cleared from pending: %s", body)
	}
}

// TestPendingIgnore_notFound_404 asserts ignoring an unknown ledger id is a
// clean 404, not a silent success.
func TestPendingIgnore_notFound_404(t *testing.T) {
	h := newPendingTestServer(t)
	rr := postJSON(t, h, "/api/pending/nope/ignore", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("ignore unknown id status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelDetail_unresolvedChannel_triggersOneResolve asserts visiting an
// uncached channel schedules exactly one metadata fetch, and that a second
// visit does not fetch again. Without the second assertion a permanently
// unresolvable channel would hit YouTube on every page load.
func TestChannelDetail_unresolvedChannel_triggersOneResolve(t *testing.T) {
	resolver := &testResolver{info: ytdlp.ChannelInfo{UCID: "UCloose", Name: "Deep Field Radio"}}
	deps := channelsTestDeps(t, resolver)
	done := make(chan struct{}, 4)
	deps.OnChannelResolved = func(string) { done <- struct{}{} }
	h := New(deps)
	seedVideoRow(t, deps, "v1", "UCloose", "Deep Field Radio")

	getJSON(t, h, "/api/channels/UCloose")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("background resolve never completed")
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver.calls = %d, want 1", resolver.calls)
	}

	getJSON(t, h, "/api/channels/UCloose")
	select {
	case <-done:
		t.Fatal("second visit resolved again; resolved_at should suppress it")
	case <-time.After(300 * time.Millisecond):
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver.calls = %d after second visit, want still 1", resolver.calls)
	}
}

// TestChannelDetail_resolveFailure_stillMarksAttempted asserts a channel that
// cannot be resolved (stale cookie, deleted channel) is not retried on every
// visit.
func TestChannelDetail_resolveFailure_stillMarksAttempted(t *testing.T) {
	resolver := &testResolver{err: errors.New("boom")}
	deps := channelsTestDeps(t, resolver)
	done := make(chan struct{}, 4)
	deps.OnChannelResolved = func(string) { done <- struct{}{} }
	h := New(deps)
	seedVideoRow(t, deps, "v1", "UCbad", "Bad Channel")

	getJSON(t, h, "/api/channels/UCbad")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("background resolve never completed")
	}

	getJSON(t, h, "/api/channels/UCbad")
	time.Sleep(300 * time.Millisecond)
	if resolver.calls != 1 {
		t.Fatalf("resolver.calls = %d, want 1 — a failed resolve must not retry every visit", resolver.calls)
	}
}

// TestListVideos_channelParam_scopes asserts ?channel= narrows the library to
// one channel, which is what the Archive tab loads.
func TestListVideos_channelParam_scopes(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	h := New(deps)
	seedVideoRow(t, deps, "v1", "UCa", "Alpha")
	seedVideoRow(t, deps, "v2", "UCb", "Beta")

	body := getJSON(t, h, "/api/videos?channel=UCa")
	if !strings.Contains(body, `"v1"`) || strings.Contains(body, `"v2"`) {
		t.Fatalf("channel scoping wrong: %s", body)
	}
}

// seedChannelAndPending tracks a channel and inserts one pending ledger row
// for it directly, without going through the resolver/scan machinery.
func seedChannelAndPending(t *testing.T, deps Deps, channelID, videoID string) {
	t.Helper()
	if err := deps.Channels.Upsert(channels.Channel{ID: channelID, Name: channelID}); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	_, err := deps.Channels.DB().Exec(
		`INSERT INTO channel_videos (video_id, channel_id, title, url, state) VALUES (?, ?, ?, ?, 'pending')`,
		videoID, channelID, "title "+videoID, "https://y/"+videoID)
	if err != nil {
		t.Fatalf("seed pending: %v", err)
	}
}

// TestChannelScan_setsNextScanAt asserts "check now" is exactly one update:
// the scheduler polls ClaimDue(now), so moving next_scan_at into the past is
// the whole mechanism.
func TestChannelScan_setsNextScanAt(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCs", Name: "S"}})
	h := New(deps)
	if rec := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@s", "subscribe": true}); rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	// A fresh test DB has cookie_status='absent', which the scan gate treats
	// the same as an expired cookie. This test exercises the happy path, so
	// mark the cookie valid the way a real machine push would.
	if err := deps.Settings.SetCookie(context.Background(), "", "valid"); err != nil {
		t.Fatalf("seed valid cookie: %v", err)
	}
	future := time.Now().UTC().Add(6 * time.Hour).Format("2006-01-02 15:04:05")
	if err := deps.Channels.Backoff("UCs", future); err != nil {
		t.Fatalf("push scan into the future: %v", err)
	}

	rec := postJSON(t, h, "/api/channels/UCs/scan", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	sub, err := deps.Channels.GetSubscription("UCs")
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}
	if sub.NextScanAt >= future {
		t.Fatalf("next_scan_at = %q, want moved earlier than %q", sub.NextScanAt, future)
	}
}

// TestChannelScan_notSubscribed_400 asserts there is nothing to schedule for
// a channel with no subscription, rather than a silent success.
func TestChannelScan_notSubscribed_400(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCt", Name: "T"}})
	h := New(deps)
	if rec := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@t"}); rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d", rec.Code)
	}
	rec := postJSON(t, h, "/api/channels/UCt/scan", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", rec.Code, rec.Body.String())
	}
}

// TestChannelScan_cookieInvalid_blocked asserts the endpoint is honest when
// the scheduler's own cookie gate would silently swallow the scan: it
// reports 200 {"status":"blocked","reason":...} with a reason the user can
// read, rather than "scheduled" for a scan that will never actually run.
func TestChannelScan_cookieInvalid_blocked(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCs", Name: "S"}})
	h := New(deps)
	if rec := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@s", "subscribe": true}); rec.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body.String())
	}
	// Fresh test DB: cookie_status defaults to 'absent', so the gate must trip.
	before, err := deps.Channels.GetSubscription("UCs")
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}

	rec := postJSON(t, h, "/api/channels/UCs/scan", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body %s", err, rec.Body.String())
	}
	if got.Status != "blocked" || got.Reason == "" {
		t.Fatalf("body = %s, want status=blocked with a non-empty reason", rec.Body.String())
	}

	after, err := deps.Channels.GetSubscription("UCs")
	if err != nil {
		t.Fatalf("get subscription: %v", err)
	}
	if after.NextScanAt != before.NextScanAt {
		t.Fatalf("next_scan_at changed on a blocked scan: %q -> %q", before.NextScanAt, after.NextScanAt)
	}
}

// TestPendingList_channelParam_scopes asserts the New tab sees only its own
// channel's discoveries.
func TestPendingList_channelParam_scopes(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	h := New(deps)
	seedChannelAndPending(t, deps, "UCa", "va")
	seedChannelAndPending(t, deps, "UCb", "vb")

	body := getJSON(t, h, "/api/pending?channel=UCa")
	if !strings.Contains(body, "va") || strings.Contains(body, "vb") {
		t.Fatalf("pending scoping wrong: %s", body)
	}
}

// TestChannelHandleFromURL covers channelHandleFromURL's two "no handle
// found" branches directly, as a plain function test (no HTTP layer
// needed): a url with no /@ segment, and one whose /@ segment is empty.
func TestChannelHandleFromURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"no at-segment", "https://www.youtube.com/channel/UC123", ""},
		{"empty handle", "https://www.youtube.com/@", ""},
		{"empty handle before query", "https://www.youtube.com/@?foo=bar", ""},
		{"plain handle", "https://www.youtube.com/@Handle", "@Handle"},
		{"handle with trailing path", "https://www.youtube.com/@Handle/videos", "@Handle"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := channelHandleFromURL(c.url); got != c.want {
				t.Fatalf("channelHandleFromURL(%q) = %q, want %q", c.url, got, c.want)
			}
		})
	}
}

// TestChannelsPost_notConfigured_503 covers the s.channels == nil ||
// s.channelResolver == nil guard.
func TestChannelsPost_notConfigured_503(t *testing.T) {
	h := New(testDeps(t))
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@x"})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsPost_invalidBody_400 covers the JSON-decode / missing-url
// validation branch.
func TestChannelsPost_invalidBody_400(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{})
	cookie := loginAndGetCookie(t, h)

	req := httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader("not json"))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/channels (bad body) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}

	rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "   "})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/channels (blank url) status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsPost_resolveFails_502 covers a ResolveChannel failure that is
// NOT ytdlp.ErrNoCookie: it must surface as 502, distinct from the 409
// cookie-required case.
func TestChannelsPost_resolveFails_502(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{err: errors.New("yt-dlp: boom")})
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@x"})
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsPost_upsertStoreError_500 covers the s.channels.Upsert error
// branch: a broken channels table must surface as 500.
func TestChannelsPost_upsertStoreError_500(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCbroken", Name: "Broken"}})
	if _, err := deps.Channels.DB().Exec(`DROP TABLE channels`); err != nil {
		t.Fatalf("drop channels table: %v", err)
	}
	h := New(deps)
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@x"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsPost_subscribeStoreError_500 covers the s.channels.Subscribe
// error branch: Upsert succeeds (channels table intact) but the
// subscriptions table is broken.
func TestChannelsPost_subscribeStoreError_500(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCbroken", Name: "Broken"}})
	if _, err := deps.Channels.DB().Exec(`DROP TABLE subscriptions`); err != nil {
		t.Fatalf("drop subscriptions table: %v", err)
	}
	h := New(deps)
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@x", "subscribe": true})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsPost_subscribedCheckStoreError_500 covers the final
// channelSubscribed error branch: Upsert succeeds and subscribe:false skips
// Subscribe entirely, but the closing List("subscribed") call (which joins
// against subscriptions) fails because that table is broken.
func TestChannelsPost_subscribedCheckStoreError_500(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCbroken", Name: "Broken"}})
	if _, err := deps.Channels.DB().Exec(`DROP TABLE subscriptions`); err != nil {
		t.Fatalf("drop subscriptions table: %v", err)
	}
	h := New(deps)
	rr := postJSON(t, h, "/api/channels", map[string]any{"url": "https://www.youtube.com/@x"})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsList_defaultsToAllWhenFilterOmitted covers the ?filter=
// omitted-entirely path, distinct from an explicit ?filter=all.
func TestChannelsList_defaultsToAllWhenFilterOmitted(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCdefault", Name: "Default"}})
	cookie := loginAndGetCookie(t, h)
	if rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "https://www.youtube.com/@default"}); rr.Code != http.StatusCreated {
		t.Fatalf("track status = %d body=%s", rr.Code, rr.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/channels", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/channels (no filter) status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "UCdefault") {
		t.Fatalf("GET /api/channels (no filter) missing tracked channel: %s", rec.Body.String())
	}
}

// TestChannelsList_storeError_500 covers the s.channels.List error branch.
func TestChannelsList_storeError_500(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	if _, err := deps.Channels.DB().Exec(`DROP TABLE channels`); err != nil {
		t.Fatalf("drop channels table: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	req := httptest.NewRequest(http.MethodGet, "/api/channels", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/channels (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestChannelsPut_invalidBody_400 covers the JSON-decode error branch.
func TestChannelsPut_invalidBody_400(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{})
	cookie := loginAndGetCookie(t, h)
	req := httptest.NewRequest(http.MethodPut, "/api/channels/UC1", strings.NewReader("not json"))
	req.AddCookie(cookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/channels/UC1 (bad body) status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestChannelsPut_loadConfigStoreError_500 covers the s.channels.UpdateConfig
// error branch of handleChannelsPut when the subscriptions table itself is
// gone (as opposed to TestChannelsPut_updateConfigStoreError_500 below,
// which blocks the UPDATE with a trigger on an otherwise-intact table).
func TestChannelsPut_loadConfigStoreError_500(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	if _, err := deps.Channels.DB().Exec(`DROP TABLE subscriptions`); err != nil {
		t.Fatalf("drop subscriptions table: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rr := putJSONWithCookie(t, h, cookie, "/api/channels/UC1", map[string]any{"autodownload": true})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("PUT (store error) status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsPut_updateConfigStoreError_500 covers the
// s.channels.UpdateConfig error branch: the channel is genuinely subscribed
// (so the List lookup finds it), but the subscriptions table's update is
// blocked by a trigger.
func TestChannelsPut_updateConfigStoreError_500(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCput", Name: "Put"}})
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	if rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "https://www.youtube.com/@put", "subscribe": true}); rr.Code != http.StatusCreated {
		t.Fatalf("track+subscribe status = %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := deps.Channels.DB().Exec(`CREATE TRIGGER block_subscriptions_update BEFORE UPDATE ON subscriptions BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rr := putJSONWithCookie(t, h, cookie, "/api/channels/UCput", map[string]any{"autodownload": true})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("PUT (store error) status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsSubscribe_notConfigured_503 covers the s.channels == nil
// guard on handleChannelsSubscribe.
func TestChannelsSubscribe_notConfigured_503(t *testing.T) {
	h := New(testDeps(t))
	rr := postJSON(t, h, "/api/channels/UC1/subscribe", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsSubscribe_getStoreError_500 covers the s.channels.Get error
// branch of handleChannelsSubscribe.
func TestChannelsSubscribe_getStoreError_500(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	if _, err := deps.Channels.DB().Exec(`DROP TABLE channels`); err != nil {
		t.Fatalf("drop channels table: %v", err)
	}
	h := New(deps)
	rr := postJSON(t, h, "/api/channels/UC1/subscribe", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsSubscribe_unknownChannel_404 covers the c == nil branch: a
// channel that was never tracked can't be subscribed.
func TestChannelsSubscribe_unknownChannel_404(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{})
	rr := postJSON(t, h, "/api/channels/nope/subscribe", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsSubscribe_storeError_500 covers the s.channels.Subscribe
// error branch: the channel is tracked (Get succeeds) but the
// subscriptions table itself is broken.
func TestChannelsSubscribe_storeError_500(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCsub", Name: "Sub"}})
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	if rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "https://www.youtube.com/@sub"}); rr.Code != http.StatusCreated {
		t.Fatalf("track status = %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := deps.Channels.DB().Exec(`DROP TABLE subscriptions`); err != nil {
		t.Fatalf("drop subscriptions table: %v", err)
	}

	rr := postJSONWithCookie(t, h, cookie, "/api/channels/UCsub/subscribe", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsUnsubscribe_notConfigured_503 covers the s.channels == nil
// guard on handleChannelsUnsubscribe.
func TestChannelsUnsubscribe_notConfigured_503(t *testing.T) {
	h := New(testDeps(t))
	rr := postJSON(t, h, "/api/channels/UC1/unsubscribe", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsUnsubscribe_notSubscribed_404 covers the !ok branch: a
// tracked-but-never-subscribed channel can't be unsubscribed.
func TestChannelsUnsubscribe_notSubscribed_404(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCunsub", Name: "Unsub"}})
	cookie := loginAndGetCookie(t, h)
	if rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "https://www.youtube.com/@unsub"}); rr.Code != http.StatusCreated {
		t.Fatalf("track status = %d body=%s", rr.Code, rr.Body.String())
	}

	rr := postJSONWithCookie(t, h, cookie, "/api/channels/UCunsub/unsubscribe", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsUnsubscribe_storeError_500 covers the s.channels.Unsubscribe
// error branch.
func TestChannelsUnsubscribe_storeError_500(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{info: ytdlp.ChannelInfo{UCID: "UCunsub2", Name: "Unsub2"}})
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	if rr := postJSONWithCookie(t, h, cookie, "/api/channels", map[string]any{"url": "https://www.youtube.com/@unsub2", "subscribe": true}); rr.Code != http.StatusCreated {
		t.Fatalf("track+subscribe status = %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := deps.Channels.DB().Exec(`DROP TABLE subscriptions`); err != nil {
		t.Fatalf("drop subscriptions table: %v", err)
	}

	rr := postJSONWithCookie(t, h, cookie, "/api/channels/UCunsub2/unsubscribe", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsDelete_notConfigured_503 covers the s.channels == nil guard
// on handleChannelsDelete.
func TestChannelsDelete_notConfigured_503(t *testing.T) {
	h := New(testDeps(t))
	rr := doDelete(t, h, "/api/channels/UC1")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsDelete_videoRefsStoreError_500 covers the s.channels.VideoRefs
// error branch.
func TestChannelsDelete_videoRefsStoreError_500(t *testing.T) {
	h := newChannelsDeleteServer(t)
	h.seedChannel("UC1")
	if _, err := h.channels.DB().Exec(`DROP TABLE videos`); err != nil {
		t.Fatalf("drop videos table: %v", err)
	}
	rr := doDelete(t, h, "/api/channels/UC1")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestChannelsDelete_cascadeStoreError_500 covers the
// s.channels.DeleteCascade error branch: VideoRefs succeeds (videos table
// intact) but the channels table itself is gone, so the cascade's final
// DELETE FROM channels fails.
func TestChannelsDelete_cascadeStoreError_500(t *testing.T) {
	h := newChannelsDeleteServer(t)
	h.seedChannel("UC1")
	if _, err := h.channels.DB().Exec(`DROP TABLE channels`); err != nil {
		t.Fatalf("drop channels table: %v", err)
	}
	rr := doDelete(t, h, "/api/channels/UC1")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestPendingList_storeError_500 covers the s.ledger.ListPending error
// branch.
func TestPendingList_storeError_500(t *testing.T) {
	h := newPendingTestServer(t)
	if _, err := h.channels.DB().Exec(`DROP TABLE channel_videos`); err != nil {
		t.Fatalf("drop channel_videos table: %v", err)
	}
	rr := postJSON(t, h, "/api/pending/x/ignore", nil) // sanity: ledger still reachable enough to 404
	if rr.Code != http.StatusInternalServerError && rr.Code != http.StatusNotFound {
		t.Fatalf("sanity ignore status = %d", rr.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/pending", nil)
	req.AddCookie(loginAndGetCookie(t, h))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /api/pending (store error) status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestPendingDownload_alreadyDownloaded_setStateStoreError_500 covers the
// ledger.SetState error branch inside the already-downloaded short-circuit:
// a trigger blocks the channel_videos state column update.
func TestPendingDownload_alreadyDownloaded_setStateStoreError_500(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if err := h.videos.Upsert(videos.Video{ID: "p1", URL: "https://www.youtube.com/watch?v=p1", ChannelID: "UC1"}); err != nil {
		t.Fatalf("upsert video: %v", err)
	}
	if err := h.videos.SetDownloaded("p1", videos.DownloadedResult{MediaPath: "/tmp/p1.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := h.ledger.Insert(channelvideos.Entry{VideoID: "p1", ChannelID: "UC1", Title: "A", URL: "https://www.youtube.com/watch?v=p1", DurationSeconds: 600, State: "pending"}); err != nil {
		t.Fatalf("insert p1: %v", err)
	}
	if _, err := h.channels.DB().Exec(`CREATE TRIGGER block_cv_state BEFORE UPDATE OF state ON channel_videos BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rr := postJSON(t, h, "/api/pending/p1/download", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestPendingDownload_upsertVideoStoreError_500 covers the s.videos.Upsert
// error branch of the main (not-already-downloaded) download path.
func TestPendingDownload_upsertVideoStoreError_500(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if err := h.ledger.Insert(channelvideos.Entry{VideoID: "p1", ChannelID: "UC1", Title: "A", URL: "https://www.youtube.com/watch?v=p1", DurationSeconds: 600, State: "pending"}); err != nil {
		t.Fatalf("insert p1: %v", err)
	}
	if _, err := h.channels.DB().Exec(`DROP TABLE videos`); err != nil {
		t.Fatalf("drop videos table: %v", err)
	}

	rr := postJSON(t, h, "/api/pending/p1/download", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestPendingDownload_setStatusStoreError_500 covers the s.videos.SetStatus
// error branch: Upsert succeeds (videos table intact) but a trigger blocks
// the status column update.
func TestPendingDownload_setStatusStoreError_500(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if err := h.ledger.Insert(channelvideos.Entry{VideoID: "p1", ChannelID: "UC1", Title: "A", URL: "https://www.youtube.com/watch?v=p1", DurationSeconds: 600, State: "pending"}); err != nil {
		t.Fatalf("insert p1: %v", err)
	}
	if _, err := h.channels.DB().Exec(`CREATE TRIGGER block_videos_status BEFORE UPDATE OF status ON videos BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rr := postJSON(t, h, "/api/pending/p1/download", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestPendingDownload_enqueueStoreError_500 covers the s.jobs.Enqueue error
// branch: Upsert and SetStatus both succeed but the queue table is broken.
func TestPendingDownload_enqueueStoreError_500(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if err := h.ledger.Insert(channelvideos.Entry{VideoID: "p1", ChannelID: "UC1", Title: "A", URL: "https://www.youtube.com/watch?v=p1", DurationSeconds: 600, State: "pending"}); err != nil {
		t.Fatalf("insert p1: %v", err)
	}
	if _, err := h.channels.DB().Exec(`DROP TABLE download_jobs`); err != nil {
		t.Fatalf("drop download_jobs table: %v", err)
	}

	rr := postJSON(t, h, "/api/pending/p1/download", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// TestPendingDownload_finalSetStateStoreError_500 covers the closing
// ledger.SetState error branch of the main download path: Upsert,
// SetStatus, and Enqueue all succeed, but the channel_videos state update
// itself is blocked.
func TestPendingDownload_finalSetStateStoreError_500(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if err := h.ledger.Insert(channelvideos.Entry{VideoID: "p1", ChannelID: "UC1", Title: "A", URL: "https://www.youtube.com/watch?v=p1", DurationSeconds: 600, State: "pending"}); err != nil {
		t.Fatalf("insert p1: %v", err)
	}
	if _, err := h.channels.DB().Exec(`CREATE TRIGGER block_cv_state BEFORE UPDATE OF state ON channel_videos BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rr := postJSON(t, h, "/api/pending/p1/download", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
	// The video itself must have been queued even though flipping the
	// ledger row's state failed — this is the handler's actual sequencing,
	// documented rather than asserted as a promise.
	v, err := h.videos.Get("p1")
	if err != nil || v == nil {
		t.Fatalf("get video: %v", err)
	}
}

// TestPendingIgnore_storeError_500 covers the s.ledger.SetState error
// branch of handlePendingIgnore.
func TestPendingIgnore_storeError_500(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if err := h.ledger.Insert(channelvideos.Entry{VideoID: "p1", ChannelID: "UC1", Title: "A", URL: "https://www.youtube.com/watch?v=p1", DurationSeconds: 600, State: "pending"}); err != nil {
		t.Fatalf("insert p1: %v", err)
	}
	if _, err := h.channels.DB().Exec(`CREATE TRIGGER block_cv_state BEFORE UPDATE OF state ON channel_videos BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	rr := postJSON(t, h, "/api/pending/p1/ignore", nil)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body=%s", rr.Code, rr.Body.String())
	}
}

// channelsListTestHarness wires the channels API against a store whose raw
// db handle is also exposed, so dormancy tests can seed channel_videos
// discovered_at timestamps directly — the channels API itself has no writer
// for that table.
type channelsListTestHarness struct {
	http.Handler
	channels *channels.Store
}

func newChannelsListTestServer(t *testing.T, resolver ChannelResolver) *channelsListTestHarness {
	t.Helper()
	db := openTestDB(t)
	sessions := auth.NewSessionStore(db, false)
	users := auth.NewUserStore(db)
	channelsStore := channels.New(db)
	deps := Deps{
		AuthService:     auth.NewService(nil, sessions, users),
		AuthMiddleware:  auth.NewMiddleware(sessions, users),
		Settings:        settings.New(db),
		Channels:        channelsStore,
		ChannelResolver: resolver,
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
	return &channelsListTestHarness{Handler: New(deps), channels: channelsStore}
}

// mustExecDB runs a statement against the raw db handle, failing the test on
// error. Mirrors internal/channels/store_test.go's mustExec — duplicated
// here because that helper is unexported in a different package.
func mustExecDB(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// TestChannelsList_includesDormantFields asserts GET /api/channels reports
// dormant:true plus a populated last_video_at for a subscribed channel whose
// most recent discovery is far outside DormantAfter, and dormant:false for a
// healthy one. The seeded timestamps are deliberately years (not months)
// apart from "now" so the test can never flake against the real DormantAfter
// boundary regardless of what day it runs.
func TestChannelsList_includesDormantFields(t *testing.T) {
	h := newChannelsListTestServer(t, &testResolver{})
	if err := h.channels.Upsert(channels.Channel{ID: "UCdormant", Name: "Dormant"}); err != nil {
		t.Fatal(err)
	}
	// channels rows are a metadata cache now; tracking is explicit.
	if err := h.channels.Track("UCdormant", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := h.channels.Subscribe("UCdormant", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := h.channels.Upsert(channels.Channel{ID: "UChealthy", Name: "Healthy"}); err != nil {
		t.Fatal(err)
	}
	// channels rows are a metadata cache now; tracking is explicit.
	if err := h.channels.Track("UChealthy", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := h.channels.Subscribe("UChealthy", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	db := h.channels.DB()
	mustExecDB(t, db, `INSERT INTO channel_videos (video_id, channel_id, state, discovered_at) VALUES ('v1','UCdormant','seen','2020-01-01 00:00:00')`)
	mustExecDB(t, db, `INSERT INTO channel_videos (video_id, channel_id, state, discovered_at) VALUES ('v2','UChealthy','seen', datetime('now'))`)

	body := getJSON(t, h, "/api/channels?filter=subscribed")
	var items []struct {
		ID          string `json:"id"`
		Dormant     bool   `json:"dormant"`
		LastVideoAt string `json:"last_video_at"`
	}
	if err := json.Unmarshal([]byte(body), &items); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	byID := map[string]bool{}
	lastVideoAt := map[string]string{}
	for _, it := range items {
		byID[it.ID] = it.Dormant
		lastVideoAt[it.ID] = it.LastVideoAt
	}
	if dormant, ok := byID["UCdormant"]; !ok || !dormant {
		t.Fatalf("UCdormant dormant = %v (present=%v), want true: %s", dormant, ok, body)
	}
	if lastVideoAt["UCdormant"] == "" {
		t.Fatalf("UCdormant last_video_at empty, want populated: %s", body)
	}
	if dormant, ok := byID["UChealthy"]; !ok || dormant {
		t.Fatalf("UChealthy dormant = %v (present=%v), want false: %s", dormant, ok, body)
	}
}

// TestAutoUnsubscribedList_returnsRecordedChannels asserts GET
// /api/channels/auto-unsubscribed returns a channel peeq auto-unsubscribed,
// with its reason and timestamp.
func TestAutoUnsubscribedList_returnsRecordedChannels(t *testing.T) {
	h := newChannelsListTestServer(t, &testResolver{})
	if err := h.channels.Upsert(channels.Channel{ID: "UCdead", Name: "Dead Channel"}); err != nil {
		t.Fatal(err)
	}
	// channels rows are a metadata cache now; tracking is explicit.
	if err := h.channels.Track("UCdead", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := h.channels.Subscribe("UCdead", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := h.channels.AutoUnsubscribe("UCdead", channels.ReasonDeleted, "2026-07-20 12:00:00"); err != nil {
		t.Fatal(err)
	}

	body := getJSON(t, h, "/api/channels/auto-unsubscribed")
	if !strings.Contains(body, "UCdead") || !strings.Contains(body, channels.ReasonDeleted) || !strings.Contains(body, "2026-07-20 12:00:00") {
		t.Fatalf("auto-unsubscribed list = %s", body)
	}
}

// TestDismissDormant_removesChannelFromDormantSet asserts a POST to
// dismiss-dormant makes the channel stop reporting dormant:true.
func TestDismissDormant_removesChannelFromDormantSet(t *testing.T) {
	h := newChannelsListTestServer(t, &testResolver{})
	if err := h.channels.Upsert(channels.Channel{ID: "UCdormant", Name: "Dormant"}); err != nil {
		t.Fatal(err)
	}
	// channels rows are a metadata cache now; tracking is explicit.
	if err := h.channels.Track("UCdormant", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := h.channels.Subscribe("UCdormant", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	mustExecDB(t, h.channels.DB(), `INSERT INTO channel_videos (video_id, channel_id, state, discovered_at) VALUES ('v1','UCdormant','seen','2020-01-01 00:00:00')`)

	before := getJSON(t, h, "/api/channels?filter=subscribed")
	if !strings.Contains(before, `"dormant":true`) {
		t.Fatalf("precondition: channel not reported dormant: %s", before)
	}

	rr := postJSON(t, h, "/api/channels/UCdormant/dismiss-dormant", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("dismiss-dormant status = %d body=%s", rr.Code, rr.Body.String())
	}

	after := getJSON(t, h, "/api/channels?filter=subscribed")
	if strings.Contains(after, `"dormant":true`) {
		t.Fatalf("channel still reported dormant after dismiss: %s", after)
	}
}

// TestDismissDormant_unknownChannel_404 asserts dismissing a channel with no
// subscription (unknown, or tracked-but-unsubscribed) is a clean 404 rather
// than the silent no-op DismissDormant used to be (Task 2 review finding).
func TestDismissDormant_unknownChannel_404(t *testing.T) {
	h := newChannelsListTestServer(t, &testResolver{})
	rr := postJSON(t, h, "/api/channels/nope/dismiss-dormant", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("dismiss-dormant unknown channel status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

// TestResubscribe_restoresSubscriptionAndClearsRecord asserts POST
// .../resubscribe re-subscribes an auto-unsubscribed channel AND clears its
// auto-unsubscribe record.
func TestResubscribe_restoresSubscriptionAndClearsRecord(t *testing.T) {
	h := newChannelsListTestServer(t, &testResolver{})
	if err := h.channels.Upsert(channels.Channel{ID: "UCdead", Name: "Dead"}); err != nil {
		t.Fatal(err)
	}
	// channels rows are a metadata cache now; tracking is explicit.
	if err := h.channels.Track("UCdead", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := h.channels.Subscribe("UCdead", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := h.channels.AutoUnsubscribe("UCdead", channels.ReasonDeleted, "2026-07-20 12:00:00"); err != nil {
		t.Fatal(err)
	}

	rr := postJSON(t, h, "/api/channels/UCdead/resubscribe", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("resubscribe status = %d body=%s", rr.Code, rr.Body.String())
	}

	subscribed := getJSON(t, h, "/api/channels?filter=subscribed")
	if !strings.Contains(subscribed, "UCdead") {
		t.Fatalf("channel not resubscribed: %s", subscribed)
	}
	auList := getJSON(t, h, "/api/channels/auto-unsubscribed")
	if strings.Contains(auList, "UCdead") {
		t.Fatalf("channel still in auto-unsubscribed list after resubscribe: %s", auList)
	}
}

// TestResubscribe_afterSecondDeath_recordsAgain covers the end-to-end
// resubscribe-then-die-again flow through the HTTP layer: a channel that was
// auto-unsubscribed, brought back via POST /resubscribe, and then dies a
// second time must show up in /api/channels/auto-unsubscribed with the new
// reason/timestamp.
//
// It does NOT exercise AutoUnsubscribe's ON CONFLICT DO UPDATE branch,
// despite the resemblance: the /resubscribe handler calls
// ClearAutoUnsubscribe before Subscribe, so the second AutoUnsubscribe call
// here inserts into an empty auto_unsubscribes slot and no PRIMARY KEY
// conflict ever occurs. The ON CONFLICT branch is instead pinned at the
// store level, via plain Subscribe (which does NOT clear the record) in
// TestAutoUnsubscribe_secondDeathAfterManualResubscribe_updatesRecord in
// internal/channels/staleness_test.go — that is the test that would fail if
// the ON CONFLICT clause were removed. Do not delete that test on the
// assumption this one already covers it.
func TestResubscribe_afterSecondDeath_recordsAgain(t *testing.T) {
	h := newChannelsListTestServer(t, &testResolver{})
	if err := h.channels.Upsert(channels.Channel{ID: "UCdead", Name: "Dead"}); err != nil {
		t.Fatal(err)
	}
	// channels rows are a metadata cache now; tracking is explicit.
	if err := h.channels.Track("UCdead", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := h.channels.Subscribe("UCdead", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := h.channels.AutoUnsubscribe("UCdead", channels.ReasonDeleted, "2026-07-20 12:00:00"); err != nil {
		t.Fatal(err)
	}
	if rr := postJSON(t, h, "/api/channels/UCdead/resubscribe", nil); rr.Code != http.StatusOK {
		t.Fatalf("resubscribe status = %d body=%s", rr.Code, rr.Body.String())
	}

	const secondDeath = "2026-08-20 12:00:00"
	if err := h.channels.AutoUnsubscribe("UCdead", channels.ReasonDeleted, secondDeath); err != nil {
		t.Fatal(err)
	}

	body := getJSON(t, h, "/api/channels/auto-unsubscribed")
	if !strings.Contains(body, "UCdead") || !strings.Contains(body, secondDeath) {
		t.Fatalf("auto-unsubscribed list missing re-recorded death: %s", body)
	}
}

// TestResubscribe_longDeadChannel_notImmediatelyDormant asserts that
// resubscribing a channel that was dead (and therefore auto-unsubscribed)
// long enough ago that its last known video predates DormantAfter does NOT
// show up as dormant:true right after the resubscribe. Without the handler
// dismissing dormancy on the fresh subscription row, auto-unsubscribe's
// DELETE FROM subscriptions takes dormant_dismissed_at with it, so a
// restored long-dead channel would land instantly in the dormant-review
// band — telling the user to unsubscribe from the channel they just
// restored. The seeded discovery date is years (not months) in the past so
// this can never flake against the real DormantAfter boundary.
func TestResubscribe_longDeadChannel_notImmediatelyDormant(t *testing.T) {
	h := newChannelsListTestServer(t, &testResolver{})
	if err := h.channels.Upsert(channels.Channel{ID: "UCdead", Name: "Long Dead"}); err != nil {
		t.Fatal(err)
	}
	// channels rows are a metadata cache now; tracking is explicit.
	if err := h.channels.Track("UCdead", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := h.channels.Subscribe("UCdead", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	mustExecDB(t, h.channels.DB(), `INSERT INTO channel_videos (video_id, channel_id, state, discovered_at) VALUES ('v1','UCdead','seen','2020-01-01 00:00:00')`)
	if err := h.channels.AutoUnsubscribe("UCdead", channels.ReasonDeleted, "2026-07-20 12:00:00"); err != nil {
		t.Fatal(err)
	}

	rr := postJSON(t, h, "/api/channels/UCdead/resubscribe", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("resubscribe status = %d body=%s", rr.Code, rr.Body.String())
	}

	body := getJSON(t, h, "/api/channels?filter=subscribed")
	var items []struct {
		ID      string `json:"id"`
		Dormant bool   `json:"dormant"`
	}
	if err := json.Unmarshal([]byte(body), &items); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	for _, it := range items {
		if it.ID == "UCdead" {
			if it.Dormant {
				t.Fatalf("resubscribed channel reported dormant immediately: %s", body)
			}
			return
		}
	}
	t.Fatalf("resubscribed channel missing from subscribed list: %s", body)
}

// TestStalenessRoutes_requireAuth asserts every dormancy/auto-unsubscribe
// route added in this task is behind requireAuth, exactly like the existing
// channels routes. peeq has exactly one route that bypasses OIDC (PUT
// /api/machine/cookie); this test guards against a new one slipping in.
func TestStalenessRoutes_requireAuth(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{})

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/channels/auto-unsubscribed", nil),
		httptest.NewRequest(http.MethodPost, "/api/channels/UC1/dismiss-dormant", nil),
		httptest.NewRequest(http.MethodPost, "/api/channels/UC1/resubscribe", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status = %d, want 401", req.Method, req.URL.Path, rec.Code)
		}
	}
}
