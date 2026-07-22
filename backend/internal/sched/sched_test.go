package sched

import (
	"context"
	"testing"
	"time"
)

func TestSleep_returnsFalseOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if Sleep(ctx, time.Hour) {
		t.Fatal("Sleep reported a completed wait on a cancelled context")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Sleep took %v; a cancelled context must return promptly", elapsed)
	}
}

// TestSleep_zeroDurationStillReportsCancellation: a loop that computed a zero
// delay must not keep going round after its context is done, which is why the
// d <= 0 shortcut checks ctx rather than returning true outright.
func TestSleep_zeroDurationStillReportsCancellation(t *testing.T) {
	if !Sleep(context.Background(), 0) {
		t.Fatal("Sleep(live ctx, 0) = false, want true")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Sleep(ctx, 0) {
		t.Fatal("Sleep(cancelled ctx, 0) = true; the loop would spin forever")
	}
}

func TestSleep_completesTheWait(t *testing.T) {
	if !Sleep(context.Background(), time.Millisecond) {
		t.Fatal("Sleep reported cancellation on a live context")
	}
}

func TestJitteredInterval_staysWithinItsWindow(t *testing.T) {
	const base, jitter = 24 * time.Hour, 3 * time.Hour
	for _, r := range []float64{0, 0.25, 0.5, 0.75, 0.999} {
		got := JitteredInterval(base, jitter, time.Hour, func() float64 { return r })
		if got < base-jitter || got >= base+jitter {
			t.Fatalf("rand=%v gave %v, outside [%v, %v)", r, got, base-jitter, base+jitter)
		}
	}
}

// TestJitteredInterval_clampsToTheFloor is the invariant worth keeping: rand is
// a replaceable seam, and a jitter wider than the base must not be able to turn
// a weekly schedule into a tight loop hammering YouTube.
func TestJitteredInterval_clampsToTheFloor(t *testing.T) {
	got := JitteredInterval(2*time.Hour, 10*time.Hour, time.Hour, func() float64 { return 0 })
	if got != time.Hour {
		t.Fatalf("got %v, want the %v floor", got, time.Hour)
	}
}

func TestPseudoRand_staysInRange(t *testing.T) {
	next := PseudoRand()
	for i := 0; i < 100; i++ {
		if v := next(); v < 0 || v >= 1 {
			t.Fatalf("PseudoRand returned %v, outside [0, 1)", v)
		}
	}
}
