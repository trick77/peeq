package media

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestIsCDNRefusal: a 4xx or a non-image body is final; a 5xx or a network
// failure is not, so a brief CDN outage does not lock a poster out.
func TestIsCDNRefusal(t *testing.T) {
	cases := map[error]bool{
		&FetchStatusError{StatusCode: http.StatusNotFound}:           true,
		ErrUnsupportedContentType:                                    true,
		&FetchStatusError{StatusCode: http.StatusServiceUnavailable}: false,
		errors.New("connection reset"):                               false,
	}
	for err, want := range cases {
		if got := IsCDNRefusal(err); got != want {
			t.Errorf("IsCDNRefusal(%v) = %v, want %v", err, got, want)
		}
	}
}

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

	mime, data, err := FetchPendingThumbnail(context.Background(), FetchImageBytes, "vid1", srv.URL)
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

// TestFetchPendingThumbnail_usesTheGivenFetcher: every request goes through the
// fetcher the caller hands in, which in production is the YouTube queue.
func TestFetchPendingThumbnail_usesTheGivenFetcher(t *testing.T) {
	var urls []string
	fetch := func(_ context.Context, url string) (string, []byte, error) {
		urls = append(urls, url)
		return "", nil, &FetchStatusError{StatusCode: http.StatusNotFound}
	}
	withYTHost(t, "https://cdn.test")
	if _, _, err := FetchPendingThumbnail(context.Background(), fetch, "vid1", "https://cdn.test/vi/vid1/maxresdefault.jpg"); err == nil {
		t.Fatal("expected an error when the fetcher fails every candidate")
	}
	want := []string{"https://cdn.test/vi/vid1/maxresdefault.jpg", "https://cdn.test/vi/vid1/hqdefault.jpg"}
	if len(urls) != len(want) || urls[0] != want[0] || urls[1] != want[1] {
		t.Fatalf("fetched %v, want %v", urls, want)
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

	_, data, err := FetchPendingThumbnail(context.Background(), FetchImageBytes, "vid1", recorded.URL)
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

// TestFetchPendingThumbnail_neverRetriesAURL: every request is a turn in the
// YouTube queue, so a transient failure moves on to the fallback instead of
// asking the same URL again.
func TestFetchPendingThumbnail_neverRetriesAURL(t *testing.T) {
	var recordedHits, hqHits int32
	recorded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&recordedHits, 1)
		http.Error(w, "try later", http.StatusInternalServerError)
	}))
	defer recorded.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hqHits, 1)
		jpegHandler(w, r)
	}))
	defer fallback.Close()
	withYTHost(t, fallback.URL)

	if _, _, err := FetchPendingThumbnail(context.Background(), FetchImageBytes, "vid1", recorded.URL); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if recordedHits != 1 || hqHits != 1 {
		t.Fatalf("recorded asked %d times, hqdefault %d, want once each", recordedHits, hqHits)
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

	if _, _, err := FetchPendingThumbnail(context.Background(), FetchImageBytes, "vid1", notFound.URL); err == nil {
		t.Fatal("expected an error when all candidates fail")
	}
}

// TestFetchPendingThumbnail_guards covers the argument guard: an empty video id
// errors before any fetch, since the hqdefault fallback url is built from it.
func TestFetchPendingThumbnail_guards(t *testing.T) {
	if _, _, err := FetchPendingThumbnail(context.Background(), FetchImageBytes, "", "https://x/y.jpg"); err == nil {
		t.Fatal("expected an error for an empty video id")
	}
}

// TestFetchPendingThumbnail_cancelStopsBeforeTheFallback: a cancelled context
// (shutdown, a queue wait given up) ends the fetch instead of queueing the
// fallback too.
func TestFetchPendingThumbnail_cancelStopsBeforeTheFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	fetch := func(context.Context, string) (string, []byte, error) {
		calls++
		cancel()
		return "", nil, context.Canceled
	}
	if _, _, err := FetchPendingThumbnail(ctx, fetch, "vid1", "https://cdn.test/a.jpg"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("fetcher called %d times after cancel, want 1", calls)
	}
}

// TestFetchPendingThumbnail_refusalStopsBeforeTheFallback: a failure that is not
// the CDN's answer about the url (a refusal while YouTube calls are paused, a
// dead network) would meet the fallback the same way, so it is not tried.
func TestFetchPendingThumbnail_refusalStopsBeforeTheFallback(t *testing.T) {
	calls := 0
	refused := errors.New("youtube paused")
	fetch := func(context.Context, string) (string, []byte, error) {
		calls++
		return "", nil, refused
	}
	if _, _, err := FetchPendingThumbnail(context.Background(), fetch, "vid1", "https://cdn.test/a.jpg"); !errors.Is(err, refused) {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if calls != 1 {
		t.Fatalf("fetcher called %d times, want 1", calls)
	}
}
