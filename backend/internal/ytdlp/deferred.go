package ytdlp

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// DeferredTimer is a one-shot timer that is not started when it is created, but
// when the work it bounds actually begins — see WithStartHook, which is what
// calls Start.
//
// It exists because a timeout meant to bound a yt-dlp process is almost never
// armed at the moment that process runs: entering a Runner call and the process
// starting are different instants, and the shared pacer makes the call wait its
// turn in between. A timer armed on entry counts that deliberate wait against
// the process, so a busy queue turns patience into a reported failure.
//
// It lives here rather than beside any one caller because every user of it is
// pairing it with WithStartHook, and the download worker is no longer the only
// one: the channel-metadata paths bound a Runner call the same way.
//
// A non-positive duration disables it: Start does nothing, and the timer never
// fires. That keeps "watchdog off" expressible without every caller
// re-checking.
//
// Once stopped it stays stopped, so a start hook firing late — a retry inside
// the Runner, a fake in a test — cannot resurrect a timer whose call already
// returned. The mutex guards against exactly that: in production all three
// methods run on the goroutine making the Runner call, but that is this
// package's internal detail rather than a promise the type should depend on.
type DeferredTimer struct {
	mu      sync.Mutex
	d       time.Duration
	fire    func()
	t       *time.Timer
	stopped bool
}

// NewDeferredTimer returns a timer that will call fire d after Start, and never
// before it.
func NewDeferredTimer(d time.Duration, fire func()) *DeferredTimer {
	return &DeferredTimer{d: d, fire: fire}
}

// Start arms the timer. Called when the process being bounded has begun.
func (dt *DeferredTimer) Start() {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	if dt.d <= 0 || dt.stopped || dt.t != nil {
		return
	}
	dt.t = time.AfterFunc(dt.d, dt.fire)
}

// Reset restarts the countdown — the "something happened" signal. A no-op
// before Start, so activity that somehow precedes the process cannot arm it.
func (dt *DeferredTimer) Reset() {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	if dt.t != nil && !dt.stopped {
		dt.t.Reset(dt.d)
	}
}

// Stop disarms permanently. Reports whether the timer had been armed and had
// not yet fired, so a caller can tell "the call returned in time" from "the
// timer already fired and cancelled it".
func (dt *DeferredTimer) Stop() bool {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.stopped = true
	if dt.t == nil {
		return false
	}
	return dt.t.Stop()
}

// CallWithCap runs fn under a cap of d that starts when the Runner call inside
// fn reaches exec — WithStartHook fires it — rather than when fn is entered,
// so the pacer's queueing wait is never counted against the process. fn gets
// the context to hand the Runner; the cap cancels it. A non-positive d
// disables the cap, as it does for DeferredTimer.
//
// stalled reports that the cap cut the call short: the timer fired while fn
// was still running, and fn failed. "While fn was still running" is what a
// bare Stop() result cannot say — it reports false both for a timer that
// fired and for one never armed, and never-armed is the ordinary outcome
// whenever the call returns before reaching exec (a gate refusal, a resolver
// failing outright). It also cannot tell a cap that killed the process from a
// timer that happened to expire in the sliver after the process failed on its
// own, which would report a private video as a stall and retry it as if it
// might come back. So the fire func checks whether fn has already returned
// and only then counts. A parent cancelled meanwhile is a shutdown, not a
// stall.
//
// The timer is stopped on every exit, panics included: it is what disarms
// the AfterFunc, and leaving it armed would hold the cancel alive for the rest
// of the cap.
func CallWithCap(ctx context.Context, d time.Duration, fn func(ctx context.Context) error) (stalled bool, err error) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var returned, fired atomic.Bool
	t := NewDeferredTimer(d, func() {
		if !returned.Load() {
			fired.Store(true)
		}
		cancel()
	})
	defer t.Stop()
	err = fn(WithStartHook(cctx, t.Start))
	returned.Store(true)
	t.Stop()
	stalled = fired.Load() && err != nil && ctx.Err() == nil
	return stalled, err
}
