package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFetchImage_savesAndReturnsRelativePath asserts the image lands under
// mediaDir and the returned path is relative, matching how subtitle_path is
// stored (SafeMediaPath resolves both, but relative is what new code writes).
func TestFetchImage_savesAndReturnsRelativePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff fake jpeg bytes"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	rel, err := FetchImage(context.Background(), srv.URL, dir, ".channels/UCx/avatar")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if filepath.IsAbs(rel) {
		t.Fatalf("returned path %q is absolute, want relative", rel)
	}
	if !strings.HasSuffix(rel, ".jpg") {
		t.Fatalf("returned path %q has no jpeg extension", rel)
	}
	if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

// TestFetchImage_rejectsNonImage asserts an HTML error page served with a 200
// is not written to disk as if it were an avatar.
func TestFetchImage_rejectsNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>nope</html>"))
	}))
	defer srv.Close()

	if _, err := FetchImage(context.Background(), srv.URL, t.TempDir(), ".channels/UCx/avatar"); err == nil {
		t.Fatal("expected an error for a non-image content type")
	}
}

// TestFetchImage_rejectsOversizeBody asserts a hostile or broken server
// cannot fill the disk through this path.
func TestFetchImage_rejectsOversizeBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		big := make([]byte, maxImageBytes+1024)
		_, _ = w.Write(big)
	}))
	defer srv.Close()

	if _, err := FetchImage(context.Background(), srv.URL, t.TempDir(), ".channels/UCx/avatar"); err == nil {
		t.Fatal("expected an error for an oversize body")
	}
}

// TestFetchImage_emptyURL asserts a channel with no banner is a no-op rather
// than an error the caller has to special-case.
func TestFetchImage_emptyURL(t *testing.T) {
	rel, err := FetchImage(context.Background(), "", t.TempDir(), ".channels/UCx/banner")
	if err != nil {
		t.Fatalf("empty url should not error: %v", err)
	}
	if rel != "" {
		t.Fatalf("rel = %q, want empty", rel)
	}
}
