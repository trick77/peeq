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

func TestSlot_spacesMembersEvenlyAcrossThePeriod(t *testing.T) {
	const period, count = 24 * time.Hour, 44
	var prev time.Duration
	for rank := 0; rank < count; rank++ {
		got := Slot(rank, count, period)
		if got < 0 || got >= period {
			t.Fatalf("rank %d gave slot %v, outside [0, %v)", rank, got, period)
		}
		if rank > 0 {
			// 24h / 44 = 32m43.6s, and integer division keeps every gap
			// within a nanosecond of it.
			if gap := got - prev; gap < period/count-time.Second || gap > period/count+time.Second {
				t.Fatalf("rank %d sits %v after its neighbour, want ~%v", rank, gap, period/count)
			}
		}
		prev = got
	}
}

// TestSlot_degenerateMembership: a count of zero has no cycle to divide, and
// dividing by it would panic in the middle of a reschedule — the one place a
// schedule must never fail to be written.
func TestSlot_degenerateMembership(t *testing.T) {
	for _, c := range []struct{ rank, count int }{{0, 0}, {3, 0}, {-1, 4}} {
		if got := Slot(c.rank, c.count, 24*time.Hour); got != 0 {
			t.Fatalf("Slot(%d, %d) = %v, want 0", c.rank, c.count, got)
		}
	}
	// A rank past the end wraps rather than running off the cycle.
	if got, want := Slot(5, 4, 24*time.Hour), Slot(1, 4, 24*time.Hour); got != want {
		t.Fatalf("Slot(5, 4) = %v, want it to wrap onto Slot(1, 4) = %v", got, want)
	}
}

func TestNextSlotAfter_landsOnTheSlot(t *testing.T) {
	const period, slot = 24 * time.Hour, 9 * time.Hour
	for _, anchor := range []time.Time{
		time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 22, 8, 59, 59, 0, time.UTC),
		time.Date(2026, 7, 22, 23, 59, 59, 0, time.UTC),
	} {
		got := NextSlotAfter(anchor, period, slot)
		if !got.After(anchor) {
			t.Fatalf("anchor %v gave %v, which is not later", anchor, got)
		}
		if got.Sub(anchor) > period {
			t.Fatalf("anchor %v gave %v, more than one period away", anchor, got)
		}
		if off := time.Duration(got.UnixNano()) % period; off != slot {
			t.Fatalf("anchor %v landed %v into the cycle, want %v", anchor, off, slot)
		}
	}
}

// TestNextSlotAfter_isStrictlyAfterTheSlotItself is what makes a skip a skip
// and a reschedule a reschedule: an anchor sitting exactly on a slot has to
// move a whole period, not answer with itself.
func TestNextSlotAfter_isStrictlyAfterTheSlotItself(t *testing.T) {
	const period, slot = 24 * time.Hour, 9 * time.Hour
	onSlot := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	got := NextSlotAfter(onSlot, period, slot)
	if want := onSlot.Add(period); !got.Equal(want) {
		t.Fatalf("got %v, want exactly one period later (%v)", got, want)
	}
}

// TestNextSlotAfter_beforeTheEpoch guards the double modulo: Go's % yields a
// negative remainder for a negative operand, which would put the answer a whole
// period in the past.
func TestNextSlotAfter_beforeTheEpoch(t *testing.T) {
	const period, slot = 24 * time.Hour, 9 * time.Hour
	anchor := time.Date(1960, 3, 4, 15, 0, 0, 0, time.UTC)
	got := NextSlotAfter(anchor, period, slot)
	if !got.After(anchor) || got.Sub(anchor) > period {
		t.Fatalf("got %v for anchor %v, want the next slot within one period", got, anchor)
	}
}

// TestNextSlotAfter_degeneratePeriod: no period means no slots to land on, and
// the caller gets its anchor back rather than a division by zero.
func TestNextSlotAfter_degeneratePeriod(t *testing.T) {
	anchor := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	if got := NextSlotAfter(anchor, 0, time.Hour); !got.Equal(anchor) {
		t.Fatalf("got %v, want the anchor back", got)
	}
}
