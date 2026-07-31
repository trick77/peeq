package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
		if cc != "public, max-age=300" {
			t.Fatalf("%s Cache-Control = %q, want the share image ceiling", suffix, cc)
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

// TestShareImages_cacheWindowClampsToTheLink is the security half of the share
// routes' caching: Resolve refuses an expired token, but a copy already sitting
// in a browser or an intermediary is past refusing. A link with less life left
// than the ceiling must hand out a correspondingly shorter window, so the
// picture cannot outlive the link that authorized it.
func TestShareImages_cacheWindowClampsToTheLink(t *testing.T) {
	deps, mediaDir := shareMetaDeps(t)
	writePNG(t, filepath.Join(mediaDir, "chan", "v1", "v1.png"), 64, 36)
	png, err := os.ReadFile(filepath.Join(mediaDir, "chan", "v1", "v1.png"))
	if err != nil {
		t.Fatalf("read seeded png: %v", err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "Shared"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetThumbnail("v1", "image/png", png); err != nil {
		t.Fatalf("store thumbnail: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// A link that never expires gets the plain ceiling.
	never := createShare(t, h, cookie, "v1", "never")
	if cc := getPublic(t, h, "/api/s/"+never.Token+"/thumbnail").Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Fatalf("never-expiring Cache-Control = %q, want the ceiling", cc)
	}

	// Now one whose remaining life is shorter than the ceiling. Re-sharing keeps
	// the token and only re-stamps the expiry, so this is the same link.
	if _, err := deps.ShareLinks.Upsert(t.Context(), "v1", 30*time.Second); err != nil {
		t.Fatalf("re-stamp expiry: %v", err)
	}
	for _, suffix := range []string{"/thumbnail", "/card.jpg"} {
		cc := getPublic(t, h, "/api/s/"+never.Token+suffix).Header().Get("Cache-Control")
		seconds := maxAgeOf(t, cc)
		if seconds <= 0 || seconds > 30 {
			t.Fatalf("%s max-age = %d (%q), want it clamped into (0, 30]", suffix, seconds, cc)
		}
	}
}

// maxAgeOf pulls the seconds out of a Cache-Control header.
func maxAgeOf(t *testing.T, cacheControl string) int {
	t.Helper()
	_, after, found := strings.Cut(cacheControl, "max-age=")
	if !found {
		t.Fatalf("no max-age in %q", cacheControl)
	}
	n, err := strconv.Atoi(strings.TrimSpace(after))
	if err != nil {
		t.Fatalf("unparsable max-age in %q: %v", cacheControl, err)
	}
	return n
}

// TestImageCache_versionedRequestIsImmutable is the other half of the caching
// work: a URL carrying a version can never mean different bytes later, so the
// response stops asking the browser to revalidate at all.
func TestImageCache_versionedRequestIsImmutable(t *testing.T) {
	deps, _ := shareMetaDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetThumbnail("v1", "image/jpeg", []byte("poster")); err != nil {
		t.Fatalf("store thumbnail: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	share := createShare(t, h, cookie, "v1", "never")

	owned := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/thumbnail?v=1700000000", nil)
	if cc := owned.Header().Get("Cache-Control"); cc != cacheImageVersioned {
		t.Fatalf("versioned Cache-Control = %q, want %q", cc, cacheImageVersioned)
	}
	// The stamp is never matched against the row — a client holding an old one
	// gets current bytes, which is exactly what it would have got anyway.
	if body := owned.Body.String(); body != "poster" {
		t.Fatalf("versioned body = %q, want the current bytes", body)
	}

	// The share route is the deliberate exception. A version says the bytes
	// cannot change; it says nothing about whether the reader is still allowed
	// to see them, so the link's clamped window outranks it. Granting an
	// immutable year here would undo shareImageCacheControl entirely.
	shared := getPublic(t, h, "/api/s/"+share.Token+"/thumbnail?v=1700000000")
	if cc := shared.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Fatalf("share versioned Cache-Control = %q, want the clamped window", cc)
	}

	// An empty v= is not a version. Treating it as one would hand a year-long
	// immutable window to a URL that never changes.
	blank := doReq(t, h, cookie, http.MethodGet, "/api/videos/v1/thumbnail?v=", nil)
	if cc := blank.Header().Get("Cache-Control"); cc != cacheImageHour {
		t.Fatalf("empty v= Cache-Control = %q, want the plain window %q", cc, cacheImageHour)
	}
}

// TestVideoDTO_carriesThumbnailVersion asserts the stamp reaches the client at
// all — without it every URL stays bare and the immutable path above is dead
// code — and that it moves when the poster is replaced.
func TestVideoDTO_carriesThumbnailVersion(t *testing.T) {
	deps, _ := videosTestDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	// Upsert leaves the row at 'new', which the Library list filters out.
	if err := deps.Videos.SetStatus("v1", "downloaded", ""); err != nil {
		t.Fatalf("mark downloaded: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// A video with no poster: has_thumbnail false and no version to speak of.
	var bare map[string]any
	if err := json.Unmarshal(doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil).Body.Bytes(), &bare); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if has, _ := bare["has_thumbnail"].(bool); has {
		t.Fatalf("has_thumbnail = true with no poster stored")
	}
	if _, present := bare["thumbnail_version"]; present {
		t.Fatalf("thumbnail_version present with no poster: %+v", bare)
	}

	if err := deps.Videos.SetThumbnail("v1", "image/jpeg", []byte("poster")); err != nil {
		t.Fatalf("store thumbnail: %v", err)
	}
	var withPoster map[string]any
	if err := json.Unmarshal(doReq(t, h, cookie, http.MethodGet, "/api/videos/v1", nil).Body.Bytes(), &withPoster); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if has, _ := withPoster["has_thumbnail"].(bool); !has {
		t.Fatalf("has_thumbnail = false with a poster stored")
	}
	// One subquery answers both questions, so this is also the guard against
	// the presence flag and the version ever disagreeing.
	version, _ := withPoster["thumbnail_version"].(string)
	if version == "" {
		t.Fatalf("no thumbnail_version alongside has_thumbnail: %+v", withPoster)
	}

	// And the list DTO agrees with the detail one.
	var list []map[string]any
	if err := json.Unmarshal(doReq(t, h, cookie, http.MethodGet, "/api/videos", nil).Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("video list = %d rows, want 1", len(list))
	}
	if list[0]["thumbnail_version"] != version {
		t.Fatalf("list thumbnail_version = %v, want %q", list[0]["thumbnail_version"], version)
	}
}

// TestChannelDTOs_carryImageVersions covers the artwork stamps on both the list
// and the detail shape, and that the two images do not share one.
func TestChannelDTOs_carryImageVersions(t *testing.T) {
	deps, _ := channelImageTestDeps(t, &testResolver{})
	if err := deps.Channels.Upsert(channels.Channel{ID: "UCx", Name: "X"}); err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	if err := deps.Channels.MarkAdded("UCx", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("mark added: %v", err)
	}
	if err := deps.Channels.SetImage("UCx", channels.ImageAvatar, "image/jpeg", []byte("a")); err != nil {
		t.Fatalf("store avatar: %v", err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	var detail map[string]any
	if err := json.Unmarshal(doReq(t, h, cookie, http.MethodGet, "/api/channels/UCx", nil).Body.Bytes(), &detail); err != nil {
		t.Fatalf("unmarshal detail: %v", err)
	}
	if v, _ := detail["avatar_version"].(string); v == "" {
		t.Fatalf("no avatar_version on the detail DTO: %+v", detail)
	}
	// No banner stored: no version, and has_banner already says so.
	if _, present := detail["banner_version"]; present {
		t.Fatalf("banner_version present with no banner: %+v", detail)
	}
	if has, _ := detail["has_banner"].(bool); has {
		t.Fatalf("has_banner = true with no banner stored")
	}

	var list []map[string]any
	if err := json.Unmarshal(doReq(t, h, cookie, http.MethodGet, "/api/channels", nil).Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("channel list = %d rows, want 1", len(list))
	}
	if list[0]["avatar_version"] != detail["avatar_version"] {
		t.Fatalf("list avatar_version = %v, detail = %v", list[0]["avatar_version"], detail["avatar_version"])
	}
}

// TestPendingDTO_carriesThumbnailVersion covers the inbox list, which did not
// read its thumbnail table at all before this.
func TestPendingDTO_carriesThumbnailVersion(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	for _, id := range []string{"pv1", "pv2"} {
		if err := h.ledger.Insert(channelvideos.Entry{
			VideoID: id, ChannelID: "UC1", Title: id,
			URL: "https://www.youtube.com/watch?v=" + id, State: "pending",
		}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	// Only one has a cached poster. The other must still be listed, and must
	// still be asked for: nothing cached is not the same as nothing to fetch.
	h.seedPendingThumbCache(t, "pv1")

	var items []map[string]any
	if err := json.Unmarshal([]byte(getJSON(t, h, "/api/pending")), &items); err != nil {
		t.Fatalf("unmarshal pending: %v", err)
	}
	byID := map[string]map[string]any{}
	for _, it := range items {
		byID[it["video_id"].(string)] = it
	}
	if v, _ := byID["pv1"]["thumbnail_version"].(string); v == "" {
		t.Fatalf("no thumbnail_version on the cached item: %+v", byID["pv1"])
	}
	if _, present := byID["pv2"]["thumbnail_version"]; present {
		t.Fatalf("thumbnail_version present with nothing cached: %+v", byID["pv2"])
	}
}
