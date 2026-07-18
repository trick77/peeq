// Package sse provides a minimal Server-Sent Events writer for streaming
// progress (e.g. video download/transcode status) to the browser.
package sse

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Writer streams SSE events to an http.ResponseWriter. Send is safe for
// concurrent use so a handler and a background worker can emit events at the
// same time.
type Writer struct {
	w            http.ResponseWriter
	flusher      http.Flusher
	mu           sync.Mutex
	lastActivity time.Time
}

// NewWriter sets SSE headers and returns a Writer, or an error if the
// ResponseWriter does not support flushing.
func NewWriter(w http.ResponseWriter) (*Writer, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support flushing")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Ask any fronting reverse proxy / LB that honors this convention (Traefik and
	// several others) not to buffer the stream.
	w.Header().Set("X-Accel-Buffering", "no")
	return &Writer{w: w, flusher: flusher, lastActivity: time.Now()}, nil
}

// Send writes one event with the given name and data payload, then flushes.
func (s *Writer) Send(event, data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	s.flusher.Flush()
	s.lastActivity = time.Now()
	return nil
}

// Heartbeat keeps the connection alive through idle proxies while the stream is
// silent. A periodic SSE comment (": ...\n\n") is ignored by EventSource
// clients but resets intermediary idle timers. The comment is only emitted
// when the stream has actually been quiet, so it adds no noise while events
// flow. Returns a stop function; call it (typically via defer) when the
// stream ends. Cancelling ctx also stops it.
func (s *Writer) Heartbeat(ctx context.Context, interval time.Duration) func() {
	if interval <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				s.mu.Lock()
				if time.Since(s.lastActivity) >= interval {
					// Best-effort: a write error here means the client is gone, which
					// the real Send path surfaces; don't disrupt the stream over a
					// failed keep-alive.
					if _, err := fmt.Fprint(s.w, ": keepalive\n\n"); err == nil {
						s.flusher.Flush()
						s.lastActivity = time.Now()
					}
				}
				s.mu.Unlock()
			}
		}
	}()
	var once sync.Once
	// stop is synchronous: it returns only after the goroutine has exited, so no
	// keep-alive can be written after the caller (e.g. an HTTP handler) returns.
	return func() {
		once.Do(func() { close(done) })
		<-stopped
	}
}

// Event is one message fanned out by a Hub.
type Event struct {
	Name string
	Data string
}

// Hub fans out events (e.g. download progress) to any number of
// concurrently connected SSE clients. It has no memory of past events: a
// client that subscribes only sees events published after it subscribed.
// Safe for concurrent use.
type Hub struct {
	mu     sync.Mutex
	subs   map[chan Event]struct{}
	closed bool
}

// NewHub returns an empty Hub, ready to accept subscribers and publish
// events.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan Event]struct{})}
}

// Subscribe registers a new listener and returns its event channel plus an
// unsubscribe function. Callers MUST call unsubscribe (typically via defer)
// when done reading, or the channel leaks for the life of the Hub.
//
// Once the Hub has been Closed, Subscribe short-circuits: it returns an
// already-closed channel (so a caller's receive loop sees ok == false on its
// very first read, exactly as an existing subscriber does when Close runs)
// and a no-op unsubscribe.
func (h *Hub) Subscribe() (<-chan Event, func()) {
	// Buffered so a burst of progress events doesn't block Publish while a
	// slow client catches up; Publish drops events for a subscriber whose
	// buffer is full rather than block the publisher.
	ch := make(chan Event, 32)
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
		})
	}
	return ch, unsubscribe
}

// Publish fans out an event to every current subscriber. It never blocks: a
// subscriber whose buffer is full simply misses this event rather than
// stalling every other subscriber (and the caller, typically the download
// worker's progress callback). Publish is a no-op after Close.
func (h *Hub) Publish(name, data string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for ch := range h.subs {
		select {
		case ch <- Event{Name: name, Data: data}:
		default:
		}
	}
}

// Close marks the Hub closed and closes every currently-subscribed channel,
// so any handler blocked in a `case ev, ok := <-ch` receive (e.g. the SSE
// stream handler, which otherwise only unblocks on its own request context)
// observes ok == false and returns immediately. This lets graceful shutdown
// (which does not cancel in-flight request contexts) complete promptly even
// while SSE clients are connected. Safe to call multiple times and safe for
// concurrent use with Subscribe/Publish; after Close, Subscribe returns an
// already-closed channel and Publish is a no-op.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.subs {
		close(ch)
		delete(h.subs, ch)
	}
}
