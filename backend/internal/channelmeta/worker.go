package channelmeta

import (
	"context"
	"log/slog"
	"math/rand"
	"time"
)

const (
	// refreshInterval is how long a subscribed channel's metadata is allowed
	// to stand before it is re-read. A week: names, artwork and subscriber
	// counts move slowly, and every refresh is a yt-dlp call against YouTube.
	refreshInterval = 7 * 24 * time.Hour
	// refreshJitter is the symmetric random window applied to every
	// reschedule. Without it, channels refreshed in the same pass would stay
	// exactly one week apart forever and slowly converge into the weekly
	// batch this feature exists to avoid; with it, the schedule keeps
	// scattering on its own. Migration 0004 seeds the initial spread.
	refreshJitter = 12 * time.Hour
	// pollInterval is how often the worker looks for something to do. One
	// channel is claimed per pass, so this doubles as the spacing between
	// refreshes — including when draining a large never-resolved backlog
	// after an import, which trickles at 12 channels/hour rather than
	// arriving as a burst.
	pollInterval = 5 * time.Minute
	// resolveTimeout bounds a single refresh. Generous because the call
	// waits out the Runner's shared pacer before yt-dlp even starts.
	resolveTimeout = 5 * time.Minute
	// sqlTimeLayout is declared in refresher.go.
)

// Deps are the worker's collaborators. Refresher and Channels are required.
type Deps struct {
	Refresher *Refresher
	// CookieStatus reports the current cookie status (settings.CookieStatus).
	// Required: refreshing without a valid cookie just burns failed attempts.
	CookieStatus func(ctx context.Context) string
	// AllowAnonymous is the dev-only escape hatch (config.AllowAnonymousYoutube)
	// mirrored from the scan scheduler: when true, a non-"valid" CookieStatus
	// no longer skips the pass.
	AllowAnonymous bool
	// YoutubePaused, when set and returning true, skips passes (the global
	// kill-switch).
	YoutubePaused func(ctx context.Context) bool
	Now           func() time.Time // injectable clock (defaults to time.Now)
	PollInterval  time.Duration    // defaults to pollInterval
	Logger        *slog.Logger
}

// Worker keeps stored channel metadata from going stale. Construct with
// NewWorker and drive with Run.
//
// It is the fourth kind of thing that talks to YouTube (after downloads,
// channel scans and on-demand HTTP resolves) and deliberately the least
// urgent: it claims at most one channel per poll, it never runs without a
// valid cookie, it stops entirely while the kill-switch is on, and its claim
// queries keep it away from the same channel's video scan.
type Worker struct {
	d    Deps
	rand func() float64
}

// NewWorker builds a Worker, filling in defaults for the optional Deps.
func NewWorker(d Deps) *Worker {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.PollInterval <= 0 {
		d.PollInterval = pollInterval
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d, rand: pseudoRand()}
}

// Run is the refresh loop; it blocks until ctx is cancelled. Each pass is
// cookie-gated and kill-switch-gated exactly like the scan scheduler's, then
// claims at most one channel and refreshes it.
func (w *Worker) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		// Cookie gate: no valid cookie → don't call YouTube, UNLESS the
		// dev-only anonymous escape hatch is enabled. Without this a
		// cookieless install would burn a failed refresh on every channel,
		// and (worse) stamp each one as attempted.
		if w.d.CookieStatus != nil && w.d.CookieStatus(ctx) != "valid" && !w.d.AllowAnonymous {
			if !w.sleep(ctx, w.d.PollInterval) {
				return
			}
			continue
		}
		// Kill-switch gate: youtube_paused → skip this pass. Re-checked each
		// poll, so clearing the flag resumes refreshing automatically.
		if w.d.YoutubePaused != nil && w.d.YoutubePaused(ctx) {
			if !w.sleep(ctx, w.d.PollInterval) {
				return
			}
			continue
		}
		channelID, ok := w.claim()
		if !ok {
			if !w.sleep(ctx, w.d.PollInterval) {
				return
			}
			continue
		}
		w.refresh(ctx, channelID)
		if !w.sleep(ctx, w.d.PollInterval) {
			return
		}
	}
}

// claim picks the next channel to refresh: the weekly rotation first, and only
// when nothing there is due, the never-resolved backlog. Due work always wins;
// the backlog drains in the gaps, which is the right priority — a channel
// already showing a name and an avatar can wait, one showing nothing cannot
// wait *instead* of it.
func (w *Worker) claim() (string, bool) {
	now := w.d.Now().UTC().Format(sqlTimeLayout)
	store := w.d.Refresher.Channels

	channelID, ok, err := store.ClaimDueMetadata(now)
	if err != nil {
		w.d.Logger.Error("channel metadata: claim due failed", "err", err)
		return "", false
	}
	if ok {
		return channelID, true
	}
	channelID, ok, err = store.ClaimUnresolved(now)
	if err != nil {
		w.d.Logger.Error("channel metadata: claim unresolved failed", "err", err)
		return "", false
	}
	return channelID, ok
}

// refresh re-reads one channel under a panic guard, then reschedules it.
//
// The reschedule happens on EVERY outcome — success, failure, or a recovered
// panic. A failure that skipped it would leave the channel due forever and the
// worker would retry it every poll, which is exactly the hammering the whole
// design avoids. Nothing else is recorded: a failed refresh must not feed the
// dead-scan counter that auto-unsubscribes channels, because that decision
// belongs to the scan scheduler, which guards it against peeq's own cookie
// being the real problem.
func (w *Worker) refresh(ctx context.Context, channelID string) {
	defer func() {
		// This parses yt-dlp output and remote HTTP responses, both external
		// input. An unrecovered panic here would take down the whole process,
		// so it is contained the way every other peeq worker contains one.
		if r := recover(); r != nil {
			w.d.Logger.Error("channel metadata: recovered from panic", "channel_id", channelID, "panic", r)
		}
		w.reschedule(channelID)
	}()

	cached, err := w.d.Refresher.Channels.Get(channelID)
	if err != nil {
		w.d.Logger.Error("channel metadata: load channel failed", "channel_id", channelID, "err", err)
		return
	}

	rctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	if err := w.d.Refresher.Resolve(rctx, channelID, cached); err != nil {
		w.d.Logger.Warn("channel metadata refresh failed", "channel_id", channelID, "err", err)
		return
	}
	w.d.Logger.Info("channel metadata refreshed", "channel_id", channelID)
}

// reschedule pushes the channel's next refresh out by a jittered interval. It
// is a no-op for an unsubscribed channel, which has no subscriptions row and
// so no rotation — a backlog channel resolved once is simply done, its
// resolved_at stamp being what keeps ClaimUnresolved from returning it again.
func (w *Worker) reschedule(channelID string) {
	next := w.d.Now().Add(w.jitteredInterval()).UTC().Format(sqlTimeLayout)
	if err := w.d.Refresher.Channels.MarkMetaRefreshed(channelID, next); err != nil {
		w.d.Logger.Error("channel metadata: reschedule failed", "channel_id", channelID, "err", err)
	}
}

// jitteredInterval returns refreshInterval plus a symmetric random jitter in
// [-refreshJitter, +refreshJitter), clamped to at least an hour so no rounding
// or configuration mistake can turn the rotation into a tight loop. Mirrors
// the scan scheduler's jitteredInterval; the small duplication is deliberate,
// since importing the scan package for fifteen lines would tie two unrelated
// schedulers together.
func (w *Worker) jitteredInterval() time.Duration {
	delta := time.Duration((w.rand()*2 - 1) * float64(refreshJitter))
	d := refreshInterval + delta
	if d < time.Hour {
		d = time.Hour
	}
	return d
}

// sleep waits d, returning false if ctx was cancelled first.
func (w *Worker) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// pseudoRand returns a seeded, non-cryptographic float64 source. Scheduling
// jitter needs no cryptographic quality.
func pseudoRand() func() float64 {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return r.Float64
}
