package ytdlp

import "context"

// heldTurn is the YouTube turn taken for several calls in a row (holdTurn).
// It is an explicit value handed to execCall, never something carried in a
// ctx: a held turn passed along ambiently would let any call made with a
// derived ctx skip the queue and the gap without anyone seeing it.
type heldTurn struct {
	release func(ran bool)
	// ran records whether any call under the turn started a process.
	ran bool
}

// done gives the turn back. Once-only (acquire's release is).
func (h *heldTurn) done() { h.release(h.ran) }

// holdTurn takes the turn for several calls in a row; make them with execCall
// and the returned turn, space them with gapWithin, and give the turn back
// with done.
//
// Download uses it for the media call and the subtitle call after it. Queued
// separately, the subtitle call could wait behind other work long enough to
// fire the download's inactivity watchdog (armed by the media call and not
// re-armed), which would throw the finished media away.
//
// Refused before queueing, as execCall's first pass would be, so a paused peeq
// does not wait for a turn only to be refused.
func (r *Runner) holdTurn(ctx context.Context) (*heldTurn, error) {
	if _, err := r.gates(); err != nil {
		return nil, &RefusedError{Err: err}
	}
	release, err := r.acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &heldTurn{release: release}, nil
}

// gapWithin waits one gap between two calls made under a held turn, so they
// are spaced like any two calls even though nobody else can start between.
func (r *Runner) gapWithin(ctx context.Context) error {
	return r.cfg.Sleep(ctx, r.drawGap())
}
