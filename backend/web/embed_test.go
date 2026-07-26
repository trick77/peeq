package web

import (
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
