package ytdlp

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// The whole point of the type: creating it must not start the clock. A timer
// that armed on construction would be the bug this exists to prevent, since
// callers construct it before the call that waits in the pacer.
func TestDeferredTimer_doesNotFireUntilStarted(t *testing.T) {
	var fired atomic.Int32
	dt := NewDeferredTimer(20*time.Millisecond, func() { fired.Add(1) })

	time.Sleep(60 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("fired %d times before Start, want 0", got)
	}

	dt.Start()
	time.Sleep(60 * time.Millisecond)
	if got := fired.Load(); got != 1 {
		t.Fatalf("fired %d times after Start, want 1", got)
	}
}

// A non-positive duration is how "no cap" is expressed, so Start must be inert
// rather than every caller re-checking.
func TestDeferredTimer_nonPositiveDurationNeverFires(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		var fired atomic.Int32
		dt := NewDeferredTimer(d, func() { fired.Add(1) })
		dt.Start()
		dt.Reset()
		time.Sleep(30 * time.Millisecond)
		if got := fired.Load(); got != 0 {
			t.Errorf("duration %v: fired %d times, want 0", d, got)
		}
		if dt.Stop() {
			t.Errorf("duration %v: Stop reported an armed timer", d)
		}
	}
}

// Stop reports whether the timer was armed and had not yet fired. Both false
// cases matter to callers and mean different things, which is why they are
// asserted separately here: they are told apart at the call site by whether the
// bounded context was cancelled.
func TestDeferredTimer_stopDistinguishesArmedFromNeverArmed(t *testing.T) {
	// Never armed — the call returned before reaching exec, so the hook never
	// fired. This is the ordinary case for a pause gate or a cookie gate.
	neverArmed := NewDeferredTimer(time.Minute, func() {})
	if neverArmed.Stop() {
		t.Error("Stop on a never-started timer reported true")
	}

	// Armed and stopped in time — the call returned before the cap.
	inTime := NewDeferredTimer(time.Minute, func() {})
	inTime.Start()
	if !inTime.Stop() {
		t.Error("Stop on a running timer reported false")
	}

	// Armed and fired.
	fired := make(chan struct{})
	expired := NewDeferredTimer(10*time.Millisecond, func() { close(fired) })
	expired.Start()
	<-fired
	if expired.Stop() {
		t.Error("Stop after firing reported true")
	}
}

// Once stopped it stays stopped: a start hook firing late — a retry inside the
// Runner, a fake in a test — must not resurrect a timer whose call has already
// returned and left nothing to cancel.
func TestDeferredTimer_stoppedStaysStopped(t *testing.T) {
	var fired atomic.Int32
	dt := NewDeferredTimer(20*time.Millisecond, func() { fired.Add(1) })
	dt.Stop()
	dt.Start()
	dt.Reset()

	time.Sleep(60 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("fired %d times after Stop, want 0", got)
	}
}

// Reset is the "something happened" signal for an inactivity watchdog. It must
// be a no-op before Start, so activity that somehow precedes the process cannot
// arm a timer the pacer has not released yet.
func TestDeferredTimer_resetBeforeStartDoesNotArm(t *testing.T) {
	var fired atomic.Int32
	dt := NewDeferredTimer(20*time.Millisecond, func() { fired.Add(1) })
	dt.Reset()

	time.Sleep(60 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Fatalf("Reset armed the timer: fired %d times, want 0", got)
	}
}

func TestDeferredTimer_resetExtendsTheCountdown(t *testing.T) {
	var fired atomic.Int32
	dt := NewDeferredTimer(50*time.Millisecond, func() { fired.Add(1) })
	dt.Start()
	for i := 0; i < 4; i++ {
		time.Sleep(20 * time.Millisecond)
		dt.Reset()
	}
	// 80ms has passed against a 50ms cap; without Reset it would have fired.
	if got := fired.Load(); got != 0 {
		t.Fatalf("fired %d times while being reset, want 0", got)
	}
	dt.Stop()
}

// The pairing this type exists for: the hook fires after the pacer, so a call
// that spends time queueing does not spend its cap. Uses the real throttle via
// a Runner rather than calling Start by hand, since the ordering inside
// execWithProgress is the thing under test.
func TestDeferredTimer_withStartHook_capSurvivesAPacerWait(t *testing.T) {
	var waits int
	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie", "valid" },
		// Stands in for the pacer's wait. Real elapsed time, so a cap armed on
		// entry would genuinely expire during it.
		Sleep: func(ctx context.Context, d time.Duration) error {
			// Only a non-zero duration is a real wait: an idle Runner still
			// calls Sleep, it just asks for nothing.
			if d <= 0 {
				return nil
			}
			waits++
			time.Sleep(60 * time.Millisecond)
			return nil
		},
		ThrottleFloor:  time.Hour,
		ThrottleJitter: time.Nanosecond,
	})

	// An idle Runner goes straight through, so the first call is what claims a
	// slot and makes the second one queue. Without it there is no wait and the
	// test proves nothing.
	if _, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ"); err != nil {
		t.Fatalf("warm-up metadata: %v", err)
	}
	if waits != 0 {
		t.Fatalf("idle runner waited %d times, want 0", waits)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A cap far shorter than the wait the second call now has to sit through.
	// Armed on entry it would kill the call; armed on the hook it never gets
	// close, because by then the waiting is over.
	bound := NewDeferredTimer(25*time.Millisecond, cancel)
	if _, err := r.Metadata(WithStartHook(ctx, bound.Start), "https://youtu.be/dQw4w9WgXcQ"); err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if !bound.Stop() {
		t.Fatal("cap fired: it was armed before the pacer wait, not after it")
	}
	if waits != 1 {
		t.Fatalf("queued call waited %d times, want 1 — no wait means this proves nothing", waits)
	}
}
