package httpapi

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/videos"
)

// assertRevalidates is the whole contract of the caching work in one helper: a
// route that hands the browser bytes must answer with a quoted ETag, and must
// answer the follow-up request carrying that ETag with a bodyless 304.
//
// It takes a request factory rather than a path so it can be reused by the
// session-gated routes (which need a cookie) and the public share routes (which
// must not have one).
func assertRevalidates(t *testing.T, h http.Handler, name string, newReq func() *http.Request) string {
	t.Helper()

	first := httptest.NewRecorder()
	h.ServeHTTP(first, newReq())
	if first.Code != http.StatusOK {
		t.Fatalf("%s: first GET = %d, want 200, body = %s", name, first.Code, first.Body.String())
	}
	tag := first.Header().Get("ETag")
	if tag == "" {
		t.Fatalf("%s: no ETag — the browser has nothing to revalidate against", name)
	}
	// Unquoted is not a style nit: net/http's scanETag ignores such a value
	// outright, so the 304 would silently never happen.
	if !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) {
		t.Fatalf("%s: ETag %q is not quoted — net/http ignores it", name, tag)
	}
	if first.Body.Len() == 0 {
		t.Fatalf("%s: first GET returned an empty body", name)
	}

	second := httptest.NewRecorder()
	req := newReq()
	req.Header.Set("If-None-Match", tag)
	h.ServeHTTP(second, req)
	if second.Code != http.StatusNotModified {
		t.Fatalf("%s: conditional GET = %d, want 304, body = %s", name, second.Code, second.Body.String())
	}
	if second.Body.Len() != 0 {
		t.Fatalf("%s: 304 carried a %d-byte body", name, second.Body.Len())
	}
	return tag
}

// TestVideoThumbnail_revalidates covers the route that had no Cache-Control at
// all: every card on a Library page used to re-download its poster on every
// load.
func TestVideoThumbnail_revalidates(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetThumbnail("v1", "image/jpeg", []byte("fake jpeg bytes")); err != nil {
		t.Fatalf("store thumbnail: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	get := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/videos/v1/thumbnail", nil)
		req.AddCookie(cookie)
		return req
	}
	assertRevalidates(t, h, "video thumbnail", get)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/thumbnail", nil)
	if cc := rec.Header().Get("Cache-Control"); cc != cacheImageHour {
		t.Fatalf("Cache-Control = %q, want %q", cc, cacheImageHour)
	}
}

// TestVideoThumbnail_etagFollowsTheBytes asserts the validator is derived from
// the content and not from the row's timestamp: SQLite's datetime('now') has
// second resolution, so a poster replaced inside the same second must still get
// a new ETag or the browser keeps showing the old picture.
func TestVideoThumbnail_etagFollowsTheBytes(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetThumbnail("v1", "image/jpeg", []byte("first poster")); err != nil {
		t.Fatalf("store thumbnail: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	before := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/thumbnail", nil).Header().Get("ETag")
	if err := deps.Videos.SetThumbnail("v1", "image/jpeg", []byte("second poster")); err != nil {
		t.Fatalf("replace thumbnail: %v", err)
	}
	after := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/thumbnail", nil).Header().Get("ETag")

	if before == "" || after == "" {
		t.Fatalf("missing ETag: before = %q, after = %q", before, after)
	}
	if before == after {
		t.Fatalf("ETag %q survived a replaced poster — the validator is not the content", before)
	}
}

// TestChannelImages_revalidate covers the avatar and banner routes, which
// already cached for a day but had no validator, so a client could only find out
// the artwork changed by downloading it again.
func TestChannelImages_revalidate(t *testing.T) {
	deps, _ := channelImageTestDeps(t, &testResolver{})
	if err := deps.Channels.Upsert(channels.Channel{ID: "UCx", Name: "X"}); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := deps.Channels.SetImage("UCx", channels.ImageAvatar, "image/jpeg", []byte("avatar bytes")); err != nil {
		t.Fatalf("store avatar: %v", err)
	}
	if err := deps.Channels.SetImage("UCx", channels.ImageBanner, "image/jpeg", []byte("banner bytes")); err != nil {
		t.Fatalf("store banner: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	var tags []string
	for _, kind := range []string{"avatar", "banner"} {
		path := "/api/channels/UCx/" + kind
		tags = append(tags, assertRevalidates(t, h, kind, func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.AddCookie(cookie)
			return req
		}))
		rec := doReq(t, h, cookie, http.MethodGet, path, nil)
		if cc := rec.Header().Get("Cache-Control"); cc != cacheImageDay {
			t.Fatalf("%s Cache-Control = %q, want %q", kind, cc, cacheImageDay)
		}
	}
	// Two different images on one channel must not share a validator, or the
	// banner would be served out of the avatar's cache entry.
	if tags[0] == tags[1] {
		t.Fatalf("avatar and banner share the ETag %q", tags[0])
	}
}

// TestPendingThumbnail_revalidates covers the inbox poster.
func TestPendingThumbnail_revalidates(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if err := h.ledger.Insert(channelvideos.Entry{
		VideoID: "pc1", ChannelID: "UC1", Title: "A",
		URL: "https://www.youtube.com/watch?v=pc1", State: "pending",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	h.seedPendingThumbCache(t, "pc1")
	cookie := loginAndGetCookie(t, h)

	assertRevalidates(t, h, "pending thumbnail", func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/pending/pc1/thumbnail", nil)
		req.AddCookie(cookie)
		return req
	})

	if cc := h.getRaw(t, "/api/pending/pc1/thumbnail").Header().Get("Cache-Control"); cc != cacheImageDay {
		t.Fatalf("Cache-Control = %q, want %q", cc, cacheImageDay)
	}
}

// TestPendingThumbnail_missIsCached is what stops a repeated outbound fetch. The
// inbox asks for every poster unconditionally — that is how the lazy fetch from
// YouTube gets a chance to run — so a 404 with no Cache-Control means an item
// with no poster anywhere costs a request, and an attempt at YouTube, on every
// page load for as long as it sits in the inbox.
func TestPendingThumbnail_missIsCached(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	// A row that has left the inbox: a 404 that needs no network to reach.
	if err := h.ledger.Insert(channelvideos.Entry{
		VideoID: "pc2", ChannelID: "UC1", Title: "B",
		URL: "https://www.youtube.com/watch?v=pc2", State: "ignored",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for _, id := range []string{"pc2", "nosuchid"} {
		rec := h.getRaw(t, "/api/pending/"+id+"/thumbnail")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", id, rec.Code)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != cacheImageMissing {
			t.Fatalf("%s: 404 Cache-Control = %q, want %q", id, cc, cacheImageMissing)
		}
	}
}

// TestShareImages_revalidate covers the two public share routes. They are the
// only image responses that stay "public": the share link is handed to whoever
// the user sends it to, and an unfurler fetching the og:image IS a shared cache.
func TestShareImages_revalidate(t *testing.T) {
	deps, mediaDir := shareMetaDeps(t)
	writePNG(t, filepath.Join(mediaDir, "chan", "v1", "v1.png"), 640, 360)
	png, err := os.ReadFile(filepath.Join(mediaDir, "chan", "v1", "v1.png"))
	if err != nil {
		t.Fatalf("read seeded png: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{
		ID: "v1", URL: "u", Title: "Shared", ChannelName: "Chan", DurationSeconds: 60,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetThumbnail("v1", "image/png", png); err != nil {
		t.Fatalf("store thumbnail: %v", err)
	}
	h := New(deps)
	share := createShare(t, h, loginAndGetCookie(t, h), "v1", "never")

	for _, suffix := range []string{"/thumbnail", "/card.jpg"} {
		path := "/api/s/" + share.Token + suffix
		assertRevalidates(t, h, suffix, func() *http.Request {
			return httptest.NewRequest(http.MethodGet, path, nil)
		})
		cc := getPublic(t, h, path).Header().Get("Cache-Control")
		if cc != cacheImagePublicHour {
			t.Fatalf("%s Cache-Control = %q, want %q", suffix, cc, cacheImagePublicHour)
		}
	}
}

// TestShareCard_keepsItsContentType guards the switch from a raw Write to
// ServeContent: with no filename to infer from, an unset Content-Type would be
// sniffed, and an og:image served as anything but image/jpeg is one an unfurler
// may refuse.
func TestShareCard_keepsItsContentType(t *testing.T) {
	deps, _ := shareMetaDeps(t)
	if err := deps.Videos.Upsert(videos.Video{
		ID: "v1", URL: "u", Title: "Cardless", ChannelName: "Chan",
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	share := createShare(t, h, loginAndGetCookie(t, h), "v1", "never")

	rec := getPublic(t, h, "/api/s/"+share.Token+"/card.jpg")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("Content-Type = %q, want image/jpeg", ct)
	}
}
