package httpapi

import (
	"net/http"
	"net/http/httptest"
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

// TestChannelDelete_publishesInboxEvent: deleting a channel cascades its
// pending cards away, so every open Inbox has to drop them.
func TestChannelDelete_publishesInboxEvent(t *testing.T) {
	hub := sse.NewHub()
	h := newPendingTestServerWith(t, func(d *Deps) { d.SSEHub = hub })
	h.seedChannel("UC1")
	if err := h.channels.MarkAdded("UC1", "2026-01-01 00:00:00"); err != nil {
		t.Fatalf("mark added: %v", err)
	}
	ch, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	req := httptest.NewRequest(http.MethodDelete, "/api/channels/UC1", nil)
	req.AddCookie(loginAndGetCookie(t, h))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	select {
	case ev := <-ch:
		if ev.Name != "inbox" || ev.Data != `{}` {
			t.Fatalf("event = %+v", ev)
		}
	default:
		t.Fatal("channel delete published no event")
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
