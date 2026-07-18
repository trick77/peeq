package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

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
		errCh <- serve(ctx, srv)
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
