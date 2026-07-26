package httpapi

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

// testShell is the SPA shell the share page is served from — the same shape as
// the tracked dist placeholder and a real Vite build (both carry </title>).
var testShell = []byte("<!doctype html>\n<html><head><title>peeq</title></head><body><div id=\"root\"></div></body></html>")

// shareMetaDeps is shareTestDeps plus the SPA wiring the /s/{token} route needs:
// a Static handler (its presence is what registers the route) and the shell.
func shareMetaDeps(t *testing.T) (Deps, string) {
	t.Helper()
	deps, mediaDir, _ := shareTestDeps(t)
	deps.Shell = testShell
	deps.Static = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(testShell)
	})
	return deps, mediaDir
}

func TestShareShell_liveTokenGetsOpenGraphTags(t *testing.T) {
	// Given a shared video.
	deps, _ := shareMetaDeps(t)
	if err := deps.Videos.Upsert(videos.Video{
		ID: "v1", URL: "u", Title: "Chasing the Aurora", ChannelName: "Nature Nerd", DurationSeconds: 754,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	if err := deps.Videos.SetSummary("v1", "## Overview\n\nA **long** look at the northern lights.", "", ""); err != nil {
		t.Fatalf("seed summary: %v", err)
	}
	h := New(deps)
	share := createShare(t, h, loginAndGetCookie(t, h), "v1", "7d")
	token := share.Token

	// When a crawler fetches the share page (no session).
	rec := getPublic(t, h, "/s/"+token)

	// Then the shell comes back with per-video OG/Twitter tags injected.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("Content-Type = %q, want html", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<meta property="og:site_name" content="peeq">`,
		`<meta property="og:title" content="Chasing the Aurora">`,
		`<meta name="twitter:card" content="summary_large_image">`,
		`<meta property="og:image" content="https://peeq.example/api/s/` + token + `/card.jpg">`,
		`<meta property="og:url" content="https://peeq.example/s/` + token + `">`,
		`<meta property="og:image:width" content="1200">`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %s\n---\n%s", want, body)
		}
	}
	// And the description carries channel, runtime and a de-marked-down summary.
	if !strings.Contains(body, `content="Nature Nerd · 13 min · Overview A long look at the northern lights."`) {
		t.Fatalf("description not as expected:\n%s", body)
	}
	// And the app still boots: the shell's own markup is intact.
	if !strings.Contains(body, `<div id="root">`) {
		t.Fatal("shell body was not preserved")
	}
	// And the shell is never cached, or clients pin a stale bundle.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
}

func TestShareShell_deadTokenServesPlainShell(t *testing.T) {
	// Given a token that was never minted.
	deps, _ := shareMetaDeps(t)
	h := New(deps)

	// When the share page is fetched.
	rec := getPublic(t, h, "/s/nosuchtoken")

	// Then the page still loads (the SPA renders its own dead-end) and leaks
	// nothing: a revoked link must look exactly like one that never existed.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "og:") {
		t.Fatalf("dead token got meta tags:\n%s", rec.Body.String())
	}
}

func TestShareShell_withoutAShellDelegatesToTheSPA(t *testing.T) {
	// Given a server with no embedded shell (an unbuilt frontend).
	deps, _ := shareMetaDeps(t)
	deps.Shell = nil
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "T", ChannelName: "C"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	share := createShare(t, h, loginAndGetCookie(t, h), "v1", "never")

	// When the share page is fetched, the SPA still serves it — the unfurl
	// degrades, the page does not.
	rec := getPublic(t, h, "/s/"+share.Token)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="root"`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestShareShell_neverNamesTheVideoID(t *testing.T) {
	// Given a shared video whose id IS its YouTube id.
	deps, _ := shareMetaDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "dQw4w9WgXcQ", URL: "u", Title: "T", ChannelName: "C"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	share := createShare(t, h, loginAndGetCookie(t, h), "dQw4w9WgXcQ", "never")

	// When the unfurl HTML is fetched.
	body := getPublic(t, h, "/s/"+share.Token).Body.String()

	// Then the token is the only public identifier in it.
	if strings.Contains(body, "dQw4w9WgXcQ") {
		t.Fatalf("share page HTML leaks the video id:\n%s", body)
	}
}

func TestShareCard_rendersJPEGFromTheThumbnail(t *testing.T) {
	// Given a shared video with a thumbnail on disk.
	deps, mediaDir := shareMetaDeps(t)
	rel := filepath.Join("chan", "v1", "v1.png")
	writePNG(t, filepath.Join(mediaDir, rel), 640, 360)
	if err := deps.Videos.Upsert(videos.Video{
		ID: "v1", URL: "u", Title: "Thumbed", ChannelName: "Chan", ThumbnailPath: rel, DurationSeconds: 60,
	}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	share := createShare(t, h, loginAndGetCookie(t, h), "v1", "never")

	// When an unfurler fetches the og:image.
	rec := getPublic(t, h, "/api/s/"+share.Token+"/card.jpg")

	// Then it gets a cacheable 1200x1200 JPEG.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=3600") {
		t.Fatalf("Cache-Control = %q, want it cacheable", cc)
	}
	img, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 1200 || b.Dy() != 1200 {
		t.Fatalf("card is %v, want 1200x1200", b)
	}
	// And the thumbnail is what got drawn into it: the seeded green shows up.
	if !hasGreenish(img) {
		t.Fatal("card carries none of the thumbnail's color — the image was not drawn")
	}
}

// hasGreenish reports whether the card contains pixels from the seeded green
// thumbnail (JPEG is lossy, so this samples for a clearly green pixel rather
// than an exact value).
func hasGreenish(img image.Image) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += 5 {
		for x := b.Min.X; x < b.Max.X; x += 5 {
			r, g, bl, _ := img.At(x, y).RGBA()
			if g>>8 > 160 && r>>8 < 90 && bl>>8 < 90 {
				return true
			}
		}
	}
	return false
}

func TestShareCard_missingThumbnailStillRenders(t *testing.T) {
	// Given a shared video with no thumbnail file.
	deps, _ := shareMetaDeps(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "Bare", ChannelName: "Chan"}); err != nil {
		t.Fatalf("seed video: %v", err)
	}
	h := New(deps)
	share := createShare(t, h, loginAndGetCookie(t, h), "v1", "never")

	// When the card is fetched, the text-only fallback is served rather than an
	// error — a broken og:image is worse than a plain card.
	rec := getPublic(t, h, "/api/s/"+share.Token+"/card.jpg")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestShareCard_deadTokenIs404(t *testing.T) {
	deps, _ := shareMetaDeps(t)
	if rec := getPublic(t, New(deps), "/api/s/nope/card.jpg"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestExternalBase_fallsBackToTheRequestOrigin(t *testing.T) {
	s := &server{}
	req := httptest.NewRequest(http.MethodGet, "/s/tok", nil)
	req.Host = "peeq.local:8080"
	if got := s.externalBase(req); got != "http://peeq.local:8080" {
		t.Fatalf("base = %q", got)
	}
	// A reverse proxy terminating TLS is honored.
	req.Header.Set("X-Forwarded-Proto", "https")
	if got := s.externalBase(req); got != "https://peeq.local:8080" {
		t.Fatalf("proxied base = %q", got)
	}
	// The configured public URL wins, trailing slash and all.
	s.publicURL = "https://peeq.example/"
	if got := s.externalBase(req); got != "https://peeq.example" {
		t.Fatalf("configured base = %q", got)
	}
}

func TestBuildMeta_escapesTitlesThatCouldBreakOutOfTheAttribute(t *testing.T) {
	got := buildMeta("video.other", `Ship "it" <now> & later`, "d", "i", "u", 1200, 1200)
	if strings.Contains(got, `content="Ship "it"`) {
		t.Fatalf("title was not escaped: %s", got)
	}
	if !strings.Contains(got, "&#34;it&#34;") || !strings.Contains(got, "&lt;now&gt;") {
		t.Fatalf("expected escaped entities, got: %s", got)
	}
}

func TestShareDescription_and_duration(t *testing.T) {
	cases := []struct {
		name string
		v    videos.Video
		want string
	}{
		{"channel only", videos.Video{ChannelName: "Chan"}, "Chan"},
		{"rounds up to a whole minute", videos.Video{ChannelName: "Chan", DurationSeconds: 40}, "Chan · 1 min"},
		{"hours", videos.Video{ChannelName: "Chan", DurationSeconds: 4320}, "Chan · 1 h 12 min"},
		{"whole hours drop the minutes", videos.Video{ChannelName: "Chan", DurationSeconds: 7200}, "Chan · 2 h"},
		{"an unfinished summary is not used", videos.Video{ChannelName: "Chan", Summary: "half", SummaryStatus: "pending"}, "Chan"},
		{"a finished summary is appended, de-marked-down", videos.Video{ChannelName: "Chan", Summary: "- **Point** one", SummaryStatus: "done"}, "Chan · Point one"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shareDescription(&c.v); got != c.want {
				t.Fatalf("shareDescription = %q, want %q", got, c.want)
			}
		})
	}
}

func TestShareDescription_clampsALongSummary(t *testing.T) {
	v := videos.Video{ChannelName: "Chan", SummaryStatus: "done", Summary: strings.Repeat("word ", 200)}
	got := shareDescription(&v)
	if len([]rune(got)) > 220 {
		t.Fatalf("description is %d runes, want it clamped", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("clamped description should end in an ellipsis: %q", got)
	}
}

func TestShareCardSubtitle_staysProvenanceEvenWithASummary(t *testing.T) {
	v := videos.Video{ChannelName: "Chan", DurationSeconds: 600, Summary: "long text", SummaryStatus: "done"}
	if got := shareCardSubtitle(&v); got != "Chan · 10 min" {
		t.Fatalf("card subtitle = %q", got)
	}
}

func TestServeShell_injectsBeforeHeadWhenThereIsNoTitle(t *testing.T) {
	rec := httptest.NewRecorder()
	serveShell(rec, []byte("<html><head><meta charset=\"utf-8\"></head><body></body></html>"), "<meta property=\"og:title\" content=\"x\">\n")
	body := rec.Body.String()
	if !strings.Contains(body, "og:title") || strings.Index(body, "og:title") > strings.Index(body, "</head>") {
		t.Fatalf("tags were not injected inside head: %s", body)
	}
}

// writePNG writes a solid green PNG of the given size, standing in for a
// downloaded thumbnail (yt-dlp emits .jpg/.png/.webp; the decoders for all three
// are registered by share_meta.go). The color is what the card test looks for.
func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.RGBA{0x20, 0xd0, 0x50, 0xff}), image.Point{}, draw.Src)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create thumbnail: %v", err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode thumbnail: %v", err)
	}
}
