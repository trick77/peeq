package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/trick77/vark/internal/sse"
)

// serveOnListener mirrors serve's shutdown behavior (hub.Close() before
// srv.Shutdown; see serve's doc comment) but serves on a pre-bound listener
// instead of letting srv.ListenAndServe bind its own, so a test can learn
// the port before starting the server.
func serveOnListener(ctx context.Context, srv *http.Server, ln net.Listener, hub *sse.Hub) error {
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	hub.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// TestServe_BindFailureReturnsError proves that a ListenAndServe failure
// (e.g. the address is already in use) is propagated as an error from
// serve, rather than serve hanging until ctx is cancelled. Without the fix,
// the goroutine running ListenAndServe only logged the error and serve kept
// blocking on <-ctx.Done(), so a boot-time bind failure never surfaced.
func TestServe_BindFailureReturnsError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to grab a free port: %v", err)
	}
	defer ln.Close()

	addr := ln.Addr().String()

	srv := &http.Server{Addr: addr, Handler: http.NewServeMux()}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(ctx, srv, sse.NewHub())
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected serve to return a non-nil error on bind failure, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return after a bind failure; it appears to be hanging on ctx.Done() instead of surfacing the error")
	}
}

// TestServe_ShutdownReturnsPromptlyWithOpenSSEStream proves that srv.Shutdown
// (called by serve once ctx is cancelled) returns quickly and with a nil
// error even while an SSE client is still connected. http.Server.Shutdown
// does not cancel in-flight request contexts — it only waits for handlers to
// return — so a stream handler blocked on r.Context().Done() would otherwise
// hold Shutdown open for its full 10s timeout and report
// context.DeadlineExceeded, which main.go treats as a fatal, non-zero exit.
// serve avoids this by closing the Hub before calling Shutdown, which
// unblocks the handler's channel receive.
func TestServe_ShutdownReturnsPromptlyWithOpenSSEStream(t *testing.T) {
	hub := sse.NewHub()
	mux := http.NewServeMux()
	handlerStarted := make(chan struct{})
	mux.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not support flushing")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ch, unsubscribe := hub.Subscribe()
		defer unsubscribe()
		close(handlerStarted)

		// Mirrors handleDownloadsStream's loop: return promptly when either
		// the request context is done or the subscriber channel is closed.
		for {
			select {
			case <-r.Context().Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
			}
		}
	})

	srv := &http.Server{Addr: "127.0.0.1:0", Handler: mux}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- serveOnListener(ctx, srv, ln, hub)
	}()

	// Open an SSE stream and leave it connected (never closed by the
	// client), simulating a browser tab that stays open across a deploy.
	resp, err := http.Get("http://" + ln.Addr().String() + "/stream")
	if err != nil {
		t.Fatalf("GET /stream: %v", err)
	}
	defer resp.Body.Close()
	go io.Copy(io.Discard, resp.Body)

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler never started")
	}

	// Trigger shutdown while the client is still connected.
	cancel()

	select {
	case err := <-serveErrCh:
		if err != nil {
			t.Fatalf("serve returned err = %v, want nil (shutdown should complete cleanly, not time out)", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("serve (srv.Shutdown) did not return within 3s with an open SSE stream connected; graceful shutdown is blocking on the client instead of on hub.Close()")
	}
}
