package web

import (
	"mime"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// built is a dist the way Vite leaves it, whatever the embedded one holds: a
// fresh clone has only .gitkeep, ship check builds the UI before go test.
var built = fstest.MapFS{
	"index.html":       {Data: []byte(`<!doctype html><title>Peeq</title><div id="root"></div>`)},
	"assets/app-1.js":  {Data: []byte("console.log(1)")},
	"site.webmanifest": {Data: []byte("{}")},
}

func TestHandlerServesIndex(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(built).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "root") {
		t.Fatalf("/ = %d %q, want the shell", rec.Code, rec.Body.String())
	}
}

// An unbuilt dist (only .gitkeep) answers the app's routes with the
// placeholder and a 503, not a directory listing or a blank 200.
func TestHandlerUnbuiltServesThePlaceholder(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(fstest.MapFS{".gitkeep": {}}).ServeHTTP(rec, httptest.NewRequest("GET", "/inbox", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "built without its UI") {
		t.Fatalf("unbuilt /inbox = %d %q, want 503 and the placeholder", rec.Code, rec.Body.String())
	}
}

// The share page's Open Graph tags are injected after </title> (share_meta.go),
// so the shell Vite builds must keep carrying that anchor: its source, the
// index.html Vite reads, is what is checked, built or not.
func TestShellSourceCarriesTheInjectionAnchor(t *testing.T) {
	src, err := os.ReadFile("../../ui/index.html")
	if err != nil {
		t.Fatalf("read ui/index.html: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(src)), "</title>") {
		t.Fatalf("ui/index.html has no </title> for meta injection")
	}
}

// IndexHTML is the embedded shell when the UI was built, an error otherwise
// (main then serves share links without unfurl meta).
func TestIndexHTML(t *testing.T) {
	shell, err := IndexHTML()
	if _, statErr := os.Stat("dist/index.html"); statErr != nil {
		if err == nil {
			t.Fatal("IndexHTML of an unbuilt dist = no error")
		}
		return
	}
	if err != nil || !strings.Contains(strings.ToLower(string(shell)), "</title>") {
		t.Fatalf("IndexHTML = %v, shell without </title>", err)
	}
}

// The embedded dist, as this binary has it: the shell when the UI was built,
// the placeholder's 503 when not; a probe path is a 404 either way.
func TestHandlerEmbedded(t *testing.T) {
	want := 503
	if _, err := os.Stat("dist/index.html"); err == nil {
		want = 200
	}
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != want {
		t.Errorf("/ = %d, want %d", rec.Code, want)
	}
	rec = httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/favicon.ico", nil))
	if rec.Code != 404 {
		t.Errorf("/favicon.ico = %d, want 404", rec.Code)
	}
}

// peeq ships no favicon.ico on purpose, so the bare root probe must 404 rather
// than reach the SPA fallback — which would answer an icon request with the
// whole index.html at 200.
func TestFaviconICOIsNotTheSPAShell(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(built).ServeHTTP(rec, httptest.NewRequest("GET", "/favicon.ico", nil))
	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d with body %q", rec.Code, rec.Body.String())
	}
}

// Only the client router's paths get the shell: an unknown one, and anything
// under /api, is a real 404. Files are served as they are.
func TestHandlerServesTheShellOnClientRoutesOnly(t *testing.T) {
	for path, want := range map[string]int{
		"/channel/UCx": 200, "/s/tok": 200, "/up-next": 200, "/video": 200, "/index.html": 200,
		"/assets/app-1.js": 200, "/site.webmanifest": 200,
		"/nonsense": 404, "/api/nope": 404, "/assets/gone.js": 404,
	} {
		rec := httptest.NewRecorder()
		handler(built).ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", path, rec.Code, want)
		}
	}
}

// The web app manifest is served by http.FileServer, which asks the mime package
// for a type and falls back to sniffing it as text/plain when there is no entry —
// Go ships none for .webmanifest. kit's spa registers one in its init.
func TestManifestExtensionHasAMIMEType(t *testing.T) {
	got := mime.TypeByExtension(".webmanifest")
	if !strings.HasPrefix(got, "application/manifest+json") {
		t.Fatalf("mime.TypeByExtension(.webmanifest) = %q, want application/manifest+json", got)
	}
}
