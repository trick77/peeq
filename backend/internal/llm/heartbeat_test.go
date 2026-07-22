package llm

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

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
		// Deliberately NOT calling stop() here. It waits without a bound, so
		// on the very failure this branch reports — a goroutine that is not
		// responding — it would hang the run until Go's test timeout and bury
		// this message. The abandoned goroutine dies with the test binary.
		close(w.release)
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
	log, _ := capture()
	stop := StartHeartbeat(context.Background(), log, 0, "tick")
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
// Reaching that state is the whole difficulty, and both easy ways of doing it
// are wrong. Sleeping and hoping lets a loaded machine run the ordinary
// close-then-wait path and pass anyway. Cancelling immediately is no better:
// the goroutine then exits before its first tick, so "it logged nothing" is
// equally consistent with a StartHeartbeat that never started a goroutine at
// all — the assertion proves nothing about the state it names.
//
// So the test watches an actual TRANSITION: heartbeats arriving (the
// goroutine is demonstrably alive), then cancel, then heartbeats stopping
// (it is demonstrably gone). Only then is stop() asked to handle the
// already-exited case.
func TestStartHeartbeat_stopAfterContextCancel(t *testing.T) {
	log, buf := capture()
	ctx, cancel := context.WithCancel(context.Background())
	const interval = time.Millisecond
	stop := StartHeartbeat(ctx, log, interval, "tick")

	ticks := func() int { return countMsg(buf.records(t), "tick") }

	// 1. Prove it is alive.
	alive := time.Now().Add(5 * time.Second)
	for ticks() < 2 {
		if time.Now().After(alive) {
			// No stop() on this path either: it waits without a bound, and a
			// heartbeat that is not logging is exactly the case where it would
			// hang instead of failing. The abandoned goroutine dies with the
			// test binary.
			// Report what was actually seen: the loop waits for TWO ticks, so
			// this also fires after exactly one, and "never logged" would send
			// a reader hunting for a goroutine that never started when the real
			// condition was one tick and then a stall.
			t.Fatalf("the heartbeat logged %d times in 5s, so this test would prove nothing about a goroutine that exited", ticks())
		}
		time.Sleep(interval)
	}

	// 2. Stop it by its context, and prove it went quiet.
	//
	// quietWindowsNeeded consecutive silent windows, not one. A single window
	// proves nothing on its own: this suite runs under -race alongside every
	// other package, and a goroutine descheduled for one 50ms window looks
	// exactly like a goroutine that exited. Concluding "gone" from that would
	// hand stop() a live goroutine and quietly test the ordinary path instead
	// — the very substitution this test was rewritten to stop making. Requiring
	// several in a row means one stall cannot decide it.
	const quietWindowsNeeded = 3
	cancel()
	quiet := time.Now().Add(5 * time.Second)
	for silent := 0; silent < quietWindowsNeeded; {
		before := ticks()
		time.Sleep(50 * interval)
		if ticks() == before {
			silent++
			continue
		}
		silent = 0 // it spoke; start counting again
		if time.Now().After(quiet) {
			// No stop() here either: see the branch above.
			t.Fatal("the heartbeat kept logging long after its context was cancelled")
		}
	}

	// 3. Only now is stop() facing an already-finished goroutine.
	//
	// Tight bound on purpose. Waiting on a goroutine that has already returned
	// is a nanosecond operation, so the seconds-long allowance the other two
	// tests need for scheduling jitter would here hide a regression that made
	// stop() wait on something new — a drain, a lock, a second goroutine —
	// and stalled every request path that defers it.
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("stop() took over 250ms for a goroutine that had already exited")
	}
}
