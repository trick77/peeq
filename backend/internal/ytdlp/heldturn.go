package ytdlp

import (
	"context"
	"time"
)

// drawGap returns one gap: the floor (never under minThrottleFloor) plus a
// random jitter.
func (r *Runner) drawGap() time.Duration {
	return r.effectiveThrottleFloor() + time.Duration(r.cfg.RandFloat64()*float64(r.cfg.ThrottleJitter))
}

// heldTurnKey marks a ctx whose caller already holds the turn (see
// holdTurn): execWithProgress then neither queues nor releases.
type heldTurnKey struct{}

// heldTurn records whether any call under a held turn started a process.
type heldTurn struct{ ran bool }

// holdTurn takes the turn for several calls in a row and returns the ctx to
// make them with and the func that gives the turn back. The caller spaces its
// own calls with gapWithin.
//
// Download uses it for the media call and the subtitle call after it. Queued
// separately, the subtitle call could wait behind other work long enough to
// fire the download's inactivity watchdog (armed by the media call and not
// re-armed), which would throw the finished media away.
func (r *Runner) holdTurn(ctx context.Context) (context.Context, func(), error) {
	release, err := r.acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	h := &heldTurn{}
	return context.WithValue(ctx, heldTurnKey{}, h), func() { release(h.ran) }, nil
}

// gapWithin waits one gap between two calls made under a held turn, so they
// are spaced like any two calls even though nobody else can start between.
func (r *Runner) gapWithin(ctx context.Context) error {
	return r.cfg.Sleep(ctx, r.drawGap())
}
