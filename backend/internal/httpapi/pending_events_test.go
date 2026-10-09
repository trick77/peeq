package httpapi

import (
	"net/http"
	"testing"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/sse"
)

// TestPending_publishesInboxEvent asserts every Inbox decision tells the other
// open tabs: download, ignore and retry-captions each publish one "inbox" event
// naming the video, so a second browser drops or refreshes the card live.
func TestPending_publishesInboxEvent(t *testing.T) {
	hub := sse.NewHub()
	h := newPendingTestServerWith(t, func(d *Deps) { d.SSEHub = hub })
	h.seedChannel("UC1")
	for _, id := range []string{"p1", "p2"} {
		if err := h.ledger.Insert(channelvideos.Entry{VideoID: id, ChannelID: "UC1", Title: id, URL: "https://www.youtube.com/watch?v=" + id, DurationSeconds: 600, State: "pending"}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	seedGaveUp(t, h, "p3")

	ch, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	for _, c := range []struct{ path, id string }{
		{"/api/pending/p1/download", "p1"},
		{"/api/pending/p2/ignore", "p2"},
		{"/api/pending/p3/retry-captions", "p3"},
	} {
		if rr := postJSON(t, h, c.path, nil); rr.Code != http.StatusOK {
			t.Fatalf("POST %s status = %d, body = %s", c.path, rr.Code, rr.Body.String())
		}
		select {
		case ev := <-ch:
			if ev.Name != "inbox" || ev.Data != `{"video_id":"`+c.id+`"}` {
				t.Fatalf("POST %s event = %+v", c.path, ev)
			}
		default:
			t.Fatalf("POST %s published no event", c.path)
		}
	}
}

// TestPending_failedDecisionPublishesNothing: a 404 changed nothing, so it must
// not send every open Inbox off to refetch.
func TestPending_failedDecisionPublishesNothing(t *testing.T) {
	hub := sse.NewHub()
	h := newPendingTestServerWith(t, func(d *Deps) { d.SSEHub = hub })
	ch, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	if rr := postJSON(t, h, "/api/pending/nope/ignore", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rr.Code)
	}
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %+v", ev)
	default:
	}
}
