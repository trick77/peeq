// Package sched holds the three pieces every peeq background loop needs and
// each had grown its own copy of: a cancellable sleep, a jittered repeat
// interval, and the pseudo-random source that feeds the jitter.
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
