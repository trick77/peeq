// Package sched holds the pieces every peeq background loop needs and each had
// grown its own copy of: a cancellable sleep, a jittered repeat interval, the
// pseudo-random source that feeds the jitter, and the slot arithmetic that
// spreads a fleet of channels evenly across its cycle.
//
// The download worker, the scan scheduler and the channel-metadata refresher
// are deliberately separate loops with unrelated cadences, but they space
// themselves out the same way — and three identical implementations means a
// fix to the jitter maths (or to the cancellation semantics of sleep) has to be
// found three times. The differences that matter between those loops are their
// intervals, which stay where they belong: as constants in each package.
package sched

import (
	"context"
	"math/rand"
	"time"
)

// Sleep waits d unless ctx is cancelled first. It returns false if ctx was
// cancelled (the caller should stop), true if the full wait elapsed.
//
// A non-positive d is not a wait at all, but it still reports cancellation
// honestly: a loop that computed a zero delay must not keep going round after
// its context is done.
func Sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// JitteredInterval returns base plus a symmetric random jitter in
// [-jitter, +jitter), never less than min.
//
// The clamp is the important part: rand is a seam callers can replace, and a
// pathological source (or a jitter configured larger than the base) must not be
// able to turn a daily or weekly schedule into a tight loop hammering YouTube.
// It is a floor on the schedule, not a correction to the caller's arithmetic.
func JitteredInterval(base, jitter, min time.Duration, rand func() float64) time.Duration {
	d := base + time.Duration(rand()*float64(2*jitter)) - jitter
	if d < min {
		d = min
	}
	return d
}

// Slot returns the offset into period that belongs to rank out of count —
// rank * period / count, so count members are spaced period/count apart.
//
// It is the "where in the cycle does this one belong" half of the anti-convoy
// scheme, and it is computed from the CURRENT membership rather than stored:
// subscribe a channel and every slot recomputes on the next reschedule, which
// keeps the spacing even without anything having to re-balance it.
//
// A count of zero (or a negative rank) yields slot zero rather than dividing by
// it. That is a caller with no membership to speak of, and putting it at the top
// of the cycle is both harmless and the same answer as a fleet of one.
func Slot(rank, count int, period time.Duration) time.Duration {
	if count <= 0 || rank < 0 {
		return 0
	}
	return time.Duration(int64(rank%count) * int64(period) / int64(count))
}

// NextSlotAfter returns the first instant STRICTLY after `after` that sits on
// slot, where slots repeat every period counted from the Unix epoch.
//
// Strictly, not at-or-after: every caller is asking "when does this happen
// NEXT", and an anchor that already sits exactly on a slot must move a whole
// period rather than answering with itself — which for the skip action would be
// no skip at all, and for a reschedule would be an immediate re-run.
//
// The epoch is the shared origin that makes a slot mean the same instant for
// every channel: a 24h period puts slot zero at 00:00 UTC, a 7-day period at
// Thursday 00:00 UTC (the epoch's own weekday). Both are arbitrary, and that is
// fine — what matters is that they never move, so a channel returns to the same
// place in the cycle no matter when it last ran.
//
// A non-positive period has no slots to land on; the anchor is returned
// unchanged rather than dividing by zero.
func NextSlotAfter(after time.Time, period, slot time.Duration) time.Time {
	if period <= 0 {
		return after
	}
	p := int64(period)
	s := (int64(slot)%p + p) % p
	base := after.UnixNano()
	// Distance back to the last slot at or before the anchor. The double
	// modulo keeps this correct for anchors before the epoch, where Go's %
	// yields a negative remainder.
	behind := ((base-s)%p + p) % p
	return time.Unix(0, base-behind+p).UTC()
}

// PseudoRand returns a float64-in-[0,1) source seeded from the wall clock. It
// exists so callers hold an injectable seam rather than reaching for the global
// source; the scheduling jitter it feeds needs no cryptographic quality.
//
// The returned closure is NOT safe for concurrent use — each background loop is
// a single goroutine and holds its own.
func PseudoRand() func() float64 {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return r.Float64
}
