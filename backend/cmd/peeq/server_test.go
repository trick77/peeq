package main

import (
	"net/http"
	"testing"
)

// A zero timeout means "no limit", which is the slow-loris exposure a
// zero-value http.Server has: a client that opens a connection and then stalls
// holds a goroutine and a file descriptor indefinitely. Asserting non-zero is
// what stops a later edit from silently reverting this.
func TestNewServerSetsHeaderAndIdleTimeouts(t *testing.T) {
	srv := newServer(":9999", http.NewServeMux())

	if srv.Addr != ":9999" {
		t.Errorf("Addr = %q, want :9999", srv.Addr)
	}
	if srv.Handler == nil {
		t.Error("Handler is nil")
	}
	if srv.ReadHeaderTimeout == 0 {
		t.Error("ReadHeaderTimeout is 0, which means no limit")
	}
	if srv.IdleTimeout == 0 {
		t.Error("IdleTimeout is 0, which means no limit")
	}
	// ErrorLog must survive the extraction: without it net/http's own failures
	// bypass slog entirely.
	if srv.ErrorLog == nil {
		t.Error("ErrorLog is nil, so net/http failures would bypass slog")
	}
}

// The two deadlines that must stay unset. Both would break streaming, and both
// look like sensible hardening to a later reader, so the reason is asserted
// here rather than left to a comment.
func TestNewServerLeavesStreamingTimeoutsUnset(t *testing.T) {
	srv := newServer(":9999", http.NewServeMux())

	// WriteTimeout would truncate the text/event-stream responses internal/sse
	// writes.
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0: it would cut off the SSE stream", srv.WriteTimeout)
	}
	// ReadTimeout bounds the whole request including the body, and the same
	// deadline then cancels r.Context(), so it kills a long-lived stream and
	// truncates a slow upload. ReadHeaderTimeout already closes slow loris.
	if srv.ReadTimeout != 0 {
		t.Errorf("ReadTimeout = %v, want 0: it would cancel the request context mid-stream", srv.ReadTimeout)
	}
}
