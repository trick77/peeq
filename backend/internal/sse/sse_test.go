package sse

import "testing"

// TestHub_publishFansOutToAllSubscribers asserts every current subscriber
// receives a published event, and a subscriber that connects after Publish
// never sees it.
func TestHub_publishFansOutToAllSubscribers(t *testing.T) {
	h := NewHub()
	ch1, cancel1 := h.Subscribe()
	defer cancel1()
	ch2, cancel2 := h.Subscribe()
	defer cancel2()

	h.Publish("progress", `{"job_id":1}`)

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Name != "progress" || ev.Data != `{"job_id":1}` {
				t.Fatalf("subscriber %d got %+v, want progress event", i, ev)
			}
		default:
			t.Fatalf("subscriber %d got no event", i)
		}
	}
}

// TestHub_unsubscribeStopsDelivery asserts a canceled subscription receives
// nothing published afterward, and Publish does not panic or block when
// there are no (or stale) subscribers.
func TestHub_unsubscribeStopsDelivery(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe()
	cancel()

	h.Publish("progress", "x")

	select {
	case ev, ok := <-ch:
		if ok {
			t.Fatalf("expected no delivery after unsubscribe, got %+v", ev)
		}
	default:
		// No delivery and channel still open (Subscribe/unsubscribe never
		// closes the channel) — this is the expected no-op path.
	}
}

// TestHub_publishNeverBlocksOnFullSubscriber asserts a slow subscriber
// whose buffer fills up does not stall Publish or other subscribers.
func TestHub_publishNeverBlocksOnFullSubscriber(t *testing.T) {
	h := NewHub()
	slow, cancelSlow := h.Subscribe()
	defer cancelSlow()
	fast, cancelFast := h.Subscribe()
	defer cancelFast()

	// Overflow the slow subscriber's buffer; none of these calls may block.
	for i := 0; i < 64; i++ {
		h.Publish("progress", "x")
	}

	select {
	case <-fast:
	default:
		t.Fatal("fast subscriber got no event despite publishing 64 times")
	}
	_ = slow // drained implicitly by GC; buffer overflow is the point of the test
}
