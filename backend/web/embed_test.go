package web

import (
	"mime"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesIndex(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()

	Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "root") {
		t.Fatalf("expected body to contain %q, got %q", "root", rec.Body.String())
	}
}

// The share page's Open Graph tags are injected after </title> (share_meta.go),
// so the embedded shell must keep carrying that anchor — in the tracked dist
// placeholder as well as a real Vite build.
func TestIndexHTMLCarriesTheInjectionAnchor(t *testing.T) {
	shell, err := IndexHTML()
	if err != nil {
		t.Fatalf("IndexHTML: %v", err)
	}
	if !strings.Contains(strings.ToLower(string(shell)), "</title>") {
		t.Fatalf("shell has no </title> for meta injection:\n%s", shell)
	}
}

// peeq ships no favicon.ico on purpose, so the bare root probe must 404 rather
// than reach the SPA fallback — which would answer an icon request with the
// whole index.html at 200. This is testable in CI precisely because the file is
// absent: it needs no built frontend.
func TestFaviconICOIsNotTheSPAShell(t *testing.T) {
	req := httptest.NewRequest("GET", "/favicon.ico", nil)
	rec := httptest.NewRecorder()

	Handler().ServeHTTP(rec, req)

	if rec.Code != 404 {
		t.Fatalf("expected 404, got %d with body %q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "<div id=\"root\">") {
		t.Fatalf("favicon.ico served the SPA shell:\n%s", rec.Body.String())
	}
}

// The web app manifest is served by http.FileServer, which asks the mime package
// for a type and falls back to sniffing it as text/plain when there is no entry —
// Go ships none for .webmanifest. This asserts the package's init registered one.
//
// Note what is NOT tested here: that ui/public's icons are actually served. The
// backend CI job never runs the frontend build, so the embedded dist holds only
// the tracked index.html placeholder and any such test would pass locally and
// fail in CI.
func TestManifestExtensionHasAMIMEType(t *testing.T) {
	got := mime.TypeByExtension(".webmanifest")
	if !strings.HasPrefix(got, "application/manifest+json") {
		t.Fatalf("mime.TypeByExtension(.webmanifest) = %q, want application/manifest+json", got)
	}
}
