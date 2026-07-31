package media

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// TestFetchPendingThumbnail_fetchesRecordedURL asserts the recorded URL is
// fetched and its bytes and mime handed back for the caller to store.
func TestFetchPendingThumbnail_fetchesRecordedURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(jpegHandler))
	defer srv.Close()

	mime, data, err := FetchPendingThumbnail(context.Background(), "vid1", srv.URL)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}
	if len(data) == 0 {
		t.Fatal("no bytes returned")
	}
}

// TestFetchPendingThumbnail_fallsBackToHqdefault asserts that when the recorded
// (largest) variant 404s — the common missing-maxresdefault case — the
// guaranteed hqdefault fallback is fetched instead.
func TestFetchPendingThumbnail_fallsBackToHqdefault(t *testing.T) {
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

	_, data, err := FetchPendingThumbnail(context.Background(), "vid1", recorded.URL)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if atomic.LoadInt32(&hqHit) == 0 {
		t.Fatal("hqdefault fallback was never fetched")
	}
	if len(data) == 0 {
		t.Fatal("no bytes returned")
	}
}

// TestFetchPendingThumbnail_retriesTransient asserts a transient 5xx is retried
// rather than treated as terminal like a 4xx.
func TestFetchPendingThumbnail_retriesTransient(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "try later", http.StatusInternalServerError)
			return
		}
		jpegHandler(w, r)
	}))
	defer srv.Close()

	if _, _, err := FetchPendingThumbnail(context.Background(), "vid1", srv.URL); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Fatalf("server called %d times, want >=2 (a 5xx must be retried)", got)
	}
}

// TestFetchPendingThumbnail_allFail asserts an error when every candidate fails
// (here both the recorded URL and the hqdefault fallback 404).
func TestFetchPendingThumbnail_allFail(t *testing.T) {
	notFound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer notFound.Close()
	withYTHost(t, notFound.URL)

	if _, _, err := FetchPendingThumbnail(context.Background(), "vid1", notFound.URL); err == nil {
		t.Fatal("expected an error when all candidates fail")
	}
}

// TestFetchPendingThumbnail_guards covers the argument guard: an empty video id
// errors before any fetch, since the hqdefault fallback url is built from it.
func TestFetchPendingThumbnail_guards(t *testing.T) {
	if _, _, err := FetchPendingThumbnail(context.Background(), "", "https://x/y.jpg"); err == nil {
		t.Fatal("expected an error for an empty video id")
	}
}

// TestFetchPendingThumbnail_unsupportedContentType asserts a 200 that isn't an
// image is treated as permanent (isPermanentFetchError via ErrUnsupportedContentType),
// so both candidates are tried once and the call errors without retrying.
func TestFetchPendingThumbnail_unsupportedContentType(t *testing.T) {
	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>nope</html>"))
	}))
	defer html.Close()
	withYTHost(t, html.URL)

	if _, _, err := FetchPendingThumbnail(context.Background(), "vid1", html.URL); err == nil {
		t.Fatal("expected an error when every candidate serves a non-image body")
	}
}

// TestFetchPendingThumbnail_ctxCancelledDuringBackoff asserts a cancelled
// context short-circuits the retry backoff rather than sleeping it out.
func TestFetchPendingThumbnail_ctxCancelledDuringBackoff(t *testing.T) {
	// A 5xx is transient, so the first attempt schedules a backoff — where the
	// cancelled context is observed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "later", http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := FetchPendingThumbnail(ctx, "vid1", srv.URL); err == nil {
		t.Fatal("expected an error when the context is cancelled")
	}
}
