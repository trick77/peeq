package scan

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/ytdlp"
)

// TestQueueThumbnail_skipsWaitingAndRecentlyFailed: the inbox asks for every
// uncached poster on every load, and each fetch costs turns in the YouTube
// queue. A poster already waiting is not queued twice, and one that failed is
// left alone for thumbRetryAfter.
func TestQueueThumbnail_skipsWaitingAndRecentlyFailed(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	s := New(Deps{
		Images: media.FetchImageBytes,
		Now:    func() time.Time { return now },
	})

	s.QueueThumbnail("v1", "u")
	s.QueueThumbnail("v1", "u")
	if n := len(s.thumbs); n != 1 {
		t.Fatalf("queued %d jobs for one poster, want 1", n)
	}

	<-s.thumbs
	s.thumbDone("v1", true)
	s.QueueThumbnail("v1", "u")
	if n := len(s.thumbs); n != 0 {
		t.Fatal("a poster that just failed was queued again")
	}

	now = now.Add(thumbRetryAfter)
	s.QueueThumbnail("v1", "u")
	if n := len(s.thumbs); n != 1 {
		t.Fatalf("a poster that failed %v ago was not queued again", thumbRetryAfter)
	}
}

// TestQueueThumbnail_pageNeverEvicts: the inbox asks for posters top to
// bottom, so a request finding the queue full must not push out the posters
// queued before it (the newest uploads, at the top of the page).
func TestQueueThumbnail_pageNeverEvicts(t *testing.T) {
	s := New(Deps{Images: media.FetchImageBytes})
	for i := range prefetchQueueSize {
		s.QueueThumbnail(fmt.Sprintf("v%d", i), "u")
	}
	s.QueueThumbnail("late", "u")
	if first := <-s.thumbs; first.videoID != "v0" {
		t.Fatalf("head of the queue = %s, want v0 — a page request evicted it", first.videoID)
	}
	if s.thumbWaiting["late"] {
		t.Fatal("a poster that did not fit is still marked waiting, so it could never be asked again")
	}
}

// TestQueueThumbnail_notWhileRefused: while YouTube calls are paused or the
// cookie is not valid every fetch would be refused, so nothing is queued.
func TestQueueThumbnail_notWhileRefused(t *testing.T) {
	s := New(Deps{
		Images:        media.FetchImageBytes,
		CookieStatus:  func(context.Context) string { return "valid" },
		YoutubePaused: func(context.Context) bool { return true },
	})
	s.QueueThumbnail("v1", "u")
	if n := len(s.thumbs); n != 0 {
		t.Fatalf("queued %d posters while paused, want 0", n)
	}
}

// TestScan_runAbandonsInFlightPrefetchOnCancel: a thumbnail fetch in flight
// when the loop is cancelled is abandoned promptly — Run returns, and the
// prefetch writes nothing afterwards. It used to be a detached goroutine on a
// background context that could store its result after main closed the
// database.
func TestScan_runAbandonsInFlightPrefetchOnCancel(t *testing.T) {
	// Buffered: the scheduler can reach the server before the test is waiting
	// on entered, and an unbuffered non-blocking send would drop the signal.
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff jpeg"))
	}))
	defer srv.Close()
	defer close(release)

	h := newScanHarness(t)
	h.images = media.FetchImageBytes
	h.sched = h.buildSched(h.jobs)
	h.addAndSubscribe("UC1", false, "")
	h.markBaselined("UC1", []string{"old1"})
	h.lister.set("UC1", []ytdlp.ChannelEntry{
		{ID: "newp", DurationSeconds: 600, LiveStatus: "not_live", ThumbnailURL: srv.URL},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.sched.Run(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the prefetch never reached the CDN")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return while a prefetch was blocked; the drainer must follow ctx")
	}
	if th, err := h.ledger.GetThumbnail("newp"); err == nil && th != nil {
		t.Fatal("a prefetch cut short by shutdown stored a thumbnail")
	}
}

// TestPrefetch_skipsARowThatLeftTheInbox: by the time a queued job drains, the
// user may have ignored the video (which deleted its poster cache on purpose)
// or the serve endpoint may have fetched it. Neither is re-fetched.
func TestPrefetch_skipsARowThatLeftTheInbox(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff jpeg"))
	}))
	defer srv.Close()

	h := newScanHarness(t)
	h.addAndSubscribe("UC1", false, "")
	for _, id := range []string{"ignored", "served"} {
		if err := h.ledger.Insert(channelvideos.Entry{
			VideoID: id, ChannelID: "UC1", Title: "A", URL: "https://www.youtube.com/watch?v=" + id,
			State: channelvideos.StatePending,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.ledger.SetState("ignored", channelvideos.StateSeen); err != nil {
		t.Fatal(err)
	}
	if err := h.ledger.SetThumbnail("served", "image/jpeg", []byte("already")); err != nil {
		t.Fatal(err)
	}

	h.sched.prefetchPendingThumbnail(context.Background(), thumbJob{videoID: "ignored", url: srv.URL})
	h.sched.prefetchPendingThumbnail(context.Background(), thumbJob{videoID: "served", url: srv.URL})

	if n := hits.Load(); n != 0 {
		t.Fatalf("CDN hit %d times for rows that need no prefetch", n)
	}
	if th, err := h.ledger.GetThumbnail("ignored"); err == nil && th != nil {
		t.Fatal("re-created the poster cache of a video that left the inbox")
	}
}

// TestQueueThumbnail_fullQueueDropsTheOldest: the queue is bounded, the scan
// never waits on it, and when it overflows the newest upload wins.
func TestQueueThumbnail_fullQueueDropsTheOldest(t *testing.T) {
	h := newScanHarness(t)
	for i := 0; i < prefetchQueueSize+1; i++ {
		h.sched.queueThumbnail("v"+string(rune('A'+i%26))+string(rune('a'+i/26)), "http://example.invalid")
	}
	if n := len(h.sched.thumbs); n != prefetchQueueSize {
		t.Fatalf("queued %d, want the cap %d", n, prefetchQueueSize)
	}
	first := <-h.sched.thumbs
	if first.videoID == "vAa" {
		t.Fatal("the oldest job survived the overflow; the newest must win")
	}
}
