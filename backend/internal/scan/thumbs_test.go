package scan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// TestScan_runAbandonsInFlightPrefetchOnCancel: a thumbnail fetch in flight
// when the loop is cancelled is abandoned promptly — Run returns, and the
// prefetch writes nothing afterwards. It used to be a detached goroutine on a
// background context that could store its result after main closed the
// database.
func TestScan_runAbandonsInFlightPrefetchOnCancel(t *testing.T) {
	entered := make(chan struct{})
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
	h.mediaDir = t.TempDir()
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
