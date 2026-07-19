package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/vark/internal/auth"
	"github.com/trick77/vark/internal/channels"
	"github.com/trick77/vark/internal/jobs"
	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/videos"
	"github.com/trick77/vark/internal/ytdlp"
)

// testResolver is a ChannelResolver whose ResolveChannel behavior is
// scripted per test, so these tests never shell out to yt-dlp.
type testResolver struct {
	ucid  string
	name  string
	err   error
	calls int
}

func (r *testResolver) ResolveChannel(ctx context.Context, url string) (string, string, error) {
	r.calls++
	if r.err != nil {
		return "", "", r.err
	}
	return r.ucid, r.name, nil
}

// channelsTestDeps builds Deps wired for the channels API: dev auth plus a
// real channels store and the given fake resolver.
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
	h := newChannelsTestServer(t, &testResolver{ucid: "UCxyz", name: "My Channel"})
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
	h := newChannelsTestServer(t, &testResolver{ucid: "UCtrackonly", name: "Track Only"})
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
	resolver := &testResolver{ucid: "UCxyz", name: "x"}
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
	h := newChannelsTestServer(t, &testResolver{ucid: "UChandle", name: "Handle Channel"})
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

// TestChannelsPut_notSubscribed_400 asserts updating config on a channel
// that is tracked but not subscribed is a clean 400, not a silent no-op.
func TestChannelsPut_notSubscribed_400(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{ucid: "UCnotsub", name: "Not Sub"})
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
	h := newChannelsTestServer(t, &testResolver{ucid: "UClifecycle", name: "Lifecycle"})
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

// TestChannels_requireAuth asserts every channels route is behind
// requireAuth.
func TestChannels_requireAuth(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{})

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/channels", bytes.NewReader([]byte("{}"))),
		httptest.NewRequest(http.MethodGet, "/api/channels", nil),
		httptest.NewRequest(http.MethodPut, "/api/channels/UC1", bytes.NewReader([]byte("{}"))),
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

// TestDownloadsPost_autoTracksChannel asserts adding a video via
// POST /api/downloads auto-tracks its channel using already-fetched
// metadata, without an extra resolve call.
func TestDownloadsPost_autoTracksChannel(t *testing.T) {
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
	if !strings.Contains(list, "UCauto") {
		t.Fatalf("channel not auto-tracked: %s", list)
	}
}
