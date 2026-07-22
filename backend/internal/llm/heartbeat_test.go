package llm

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// quietLogger is a logger that goes nowhere. Tests here care about WHEN the
// heartbeat writes, never what it wrote, and routing that through
// slog.Default() would spray real lines into the test binary's stderr and
// couple the test to whatever else has reconfigured the global logger.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// countingWriter records how many lines have been written, so a test can
// assert that the heartbeat has genuinely stopped writing rather than sleep
// and hope.
type countingWriter struct {
	mu sync.Mutex
	n  int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.n++
	w.mu.Unlock()
	return len(p), nil
}

func (w *countingWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.n
}

// blockingWriter reports when a write starts and holds it there until it is
// released, so a test can pin the heartbeat goroutine mid-log-line and
// observe what stop() does while it is stuck.
type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default: // only the first write needs to be announced
	}
	<-w.release
	return len(p), nil
}

// TestStartHeartbeat_stopWaitsForTheLineInFlight is the regression test for a
// data race that failed CI on PRs touching neither package: stop() closed its
// channel and returned while the goroutine was still inside log.Info, so a
// caller that read the log sink straight afterwards raced the write.
//
// Deterministic rather than timing-hopeful: the writer is pinned mid-line, so
// a stop() that does not wait MUST return early and a stop() that waits
// CANNOT return until the line is released.
func TestStartHeartbeat_stopWaitsForTheLineInFlight(t *testing.T) {
	w := &blockingWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	log := slog.New(slog.NewJSONHandler(w, nil))

	stop := StartHeartbeat(context.Background(), log, time.Millisecond, "tick")

	// Wait until the goroutine is genuinely inside a write.
	select {
	case <-w.entered:
	case <-time.After(5 * time.Second):
		close(w.release)
		stop()
		t.Fatal("the heartbeat never logged")
	}

	returned := make(chan struct{})
	go func() {
		stop()
		close(returned)
	}()

	select {
	case <-returned:
		close(w.release)
		t.Fatal("stop() returned while a heartbeat line was still being written")
	case <-time.After(100 * time.Millisecond):
		// Correct: still blocked behind the in-flight write.
	}

	close(w.release)
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("stop() never returned after the write was released")
	}
}

// TestStartHeartbeat_disabledStopIsSafe covers the non-positive-interval
// branch: it starts no goroutine, so its stop has nothing to wait for and
// must not block.
func TestStartHeartbeat_disabledStopIsSafe(t *testing.T) {
	stop := StartHeartbeat(context.Background(), quietLogger(), 0, "tick")
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop() blocked for a disabled heartbeat")
	}
}

// TestStartHeartbeat_stopAfterContextCancel asserts stop() still returns once
// the goroutine has already exited on its own — the wait must observe a
// finished goroutine, not hang waiting for one that will never signal again.
//
// The "already exited" state is CONFIRMED, not assumed. Sleeping and hoping
// would let a loaded machine run the ordinary close-then-wait path instead
// and still pass, reporting success for a path it never exercised. Here the
// test watches the write count go quiet across many missed ticks first, so if
// the goroutine were still running the test would fail rather than silently
// test something else.
func TestStartHeartbeat_stopAfterContextCancel(t *testing.T) {
	w := &countingWriter{}
	log := slog.New(slog.NewJSONHandler(w, nil))

	ctx, cancel := context.WithCancel(context.Background())
	interval := time.Millisecond
	stop := StartHeartbeat(ctx, log, interval, "tick")
	cancel()

	// Quiet for 50 intervals running is the goroutine being gone, not slow.
	deadline := time.Now().Add(5 * time.Second)
	for {
		before := w.count()
		time.Sleep(50 * interval)
		if w.count() == before {
			break
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("the heartbeat kept logging long after its context was cancelled")
		}
	}

	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop() hung after the context had already stopped the goroutine")
	}
}
