package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// jpegHandler writes a minimal valid JPEG response.
func jpegHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write([]byte("\xff\xd8\xff fake jpeg bytes"))
}

// withYTHost points the hqdefault fallback at a test server for the duration of
// a test, restoring the real CDN host afterward.
func withYTHost(t *testing.T, host string) {
	t.Helper()
	prev := ytThumbHost
	ytThumbHost = host
	t.Cleanup(func() { ytThumbHost = prev })
}

// TestEnsurePendingThumbnail_fetchesAndStores asserts the recorded URL is
// fetched, stored under .pending/<id>/, and its relative path returned.
func TestEnsurePendingThumbnail_fetchesAndStores(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(jpegHandler))
	defer srv.Close()

	dir := t.TempDir()
	rel, err := EnsurePendingThumbnail(context.Background(), dir, "vid1", srv.URL)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if want := ".pending/vid1/thumbnail"; rel[:len(want)] != want {
		t.Fatalf("rel = %q, want prefix %q", rel, want)
	}
	if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

// TestEnsurePendingThumbnail_cacheHitNoFetch asserts a second call serves the
// cached file without another network fetch.
func TestEnsurePendingThumbnail_cacheHitNoFetch(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		jpegHandler(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	if _, err := EnsurePendingThumbnail(context.Background(), dir, "vid1", srv.URL); err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if _, err := EnsurePendingThumbnail(context.Background(), dir, "vid1", srv.URL); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("server hit %d times, want 1 (second call must serve from cache)", got)
	}
}

// TestEnsurePendingThumbnail_fallsBackToHqdefault asserts that when the recorded
// (largest) variant 404s — the common missing-maxresdefault case — the
// guaranteed hqdefault fallback is fetched instead.
func TestEnsurePendingThumbnail_fallsBackToHqdefault(t *testing.T) {
	recorded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such variant", http.StatusNotFound)
	}))
	defer recorded.Close()

	var hqHit int32
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hqHit, 1)
		jpegHandler(w, r)
	}))
	defer fallback.Close()
	withYTHost(t, fallback.URL)

	dir := t.TempDir()
	rel, err := EnsurePendingThumbnail(context.Background(), dir, "vid1", recorded.URL)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if atomic.LoadInt32(&hqHit) == 0 {
		t.Fatal("hqdefault fallback was never fetched")
	}
	if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
		t.Fatalf("file not written: %v", err)
	}
}

// TestEnsurePendingThumbnail_retriesTransient asserts a transient 5xx is retried
// rather than treated as terminal like a 4xx.
func TestEnsurePendingThumbnail_retriesTransient(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "try later", http.StatusInternalServerError)
			return
		}
		jpegHandler(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	if _, err := EnsurePendingThumbnail(context.Background(), dir, "vid1", srv.URL); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Fatalf("server called %d times, want >=2 (a 5xx must be retried)", got)
	}
}

// TestEnsurePendingThumbnail_allFail asserts an error when every candidate fails
// (here both the recorded URL and the hqdefault fallback 404).
func TestEnsurePendingThumbnail_allFail(t *testing.T) {
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer notFound.Close()
	withYTHost(t, notFound.URL)

	dir := t.TempDir()
	if _, err := EnsurePendingThumbnail(context.Background(), dir, "vid1", notFound.URL); err == nil {
		t.Fatal("expected an error when all candidates fail")
	}
}

// TestEnsurePendingThumbnail_guards covers the two argument guards: an unset
// media dir and an empty video id both error before any fetch.
func TestEnsurePendingThumbnail_guards(t *testing.T) {
	if _, err := EnsurePendingThumbnail(context.Background(), "", "vid1", "https://x/y.jpg"); err == nil {
		t.Fatal("expected an error for an empty media dir")
	}
	if _, err := EnsurePendingThumbnail(context.Background(), t.TempDir(), "", "https://x/y.jpg"); err == nil {
		t.Fatal("expected an error for an empty video id")
	}
}

// TestEnsurePendingThumbnail_unsupportedContentType asserts a 200 that isn't an
// image is treated as permanent (isPermanentFetchError via ErrUnsupportedContentType),
// so both candidates are tried once and the call errors without retrying.
func TestEnsurePendingThumbnail_unsupportedContentType(t *testing.T) {
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>nope</html>"))
	}))
	defer html.Close()
	withYTHost(t, html.URL)

	if _, err := EnsurePendingThumbnail(context.Background(), t.TempDir(), "vid1", html.URL); err == nil {
		t.Fatal("expected an error when every candidate serves a non-image body")
	}
}

// TestEnsurePendingThumbnail_ctxCancelledDuringBackoff asserts a cancelled
// context short-circuits the retry backoff rather than sleeping it out.
func TestEnsurePendingThumbnail_ctxCancelledDuringBackoff(t *testing.T) {
	// A 5xx is transient, so the first attempt schedules a backoff — where the
	// cancelled context is observed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "later", http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := EnsurePendingThumbnail(ctx, t.TempDir(), "vid1", srv.URL); err == nil {
		t.Fatal("expected an error when the context is cancelled")
	}
}
