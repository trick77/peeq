package channelmeta

import (
	"context"
	"log/slog"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/sched"
	"github.com/trick77/peeq/internal/ytdlp"
)

// ActivityRecorder records a metadata-refresh outcome for the Activity feed.
// Nil-safe. Only failures are recorded — a routine weekly refresh succeeding is
// the definition of no-news, so recording it would just be noise.
type ActivityRecorder interface {
	Record(activity.Event)
}

const (
	// refreshInterval is how long a subscribed channel's metadata is allowed
	// to stand before it is re-read. A week: names, artwork and subscriber
	// counts move slowly, and every refresh is a yt-dlp call against YouTube.
	refreshInterval = 7 * 24 * time.Hour
	// pollInterval is how often the worker looks for something to do. One
	// channel is claimed per pass, so this doubles as the spacing between
	// refreshes — including when draining a large never-resolved backlog
	// after an import, which trickles at 12 channels/hour rather than
	// arriving as a burst.
	pollInterval = 5 * time.Minute
	// resolveTimeout bounds a single refresh, measured from the moment yt-dlp
	// actually starts rather than from when Resolve is entered — see the
	// DeferredTimer in refresh. It no longer has to be generous enough to
	// absorb the pacer's queueing wait, only long enough for one yt-dlp call
	// and two image fetches, but it is left where it was: shortening it is a
	// separate decision from fixing what it measures.
	resolveTimeout = 5 * time.Minute
	// sqlTimeLayout is declared in refresher.go.
)

// Deps are the worker's collaborators. Refresher and Channels are required.
type Deps struct {
	Refresher *Refresher
	// CookieStatus reports the current cookie status (settings.CookieStatus).
	// REQUIRED, and called unconditionally — leaving it nil panics on the first
	// pass rather than silently skipping the cookie gate.
	CookieStatus func(ctx context.Context) string
	// AllowAnonymous is the dev-only escape hatch (config.AllowAnonymousYoutube)
	// mirrored from the scan scheduler: when true, a non-"valid" CookieStatus
	// no longer skips the pass.
	AllowAnonymous bool
	// YoutubePaused, when set and returning true, skips passes (the global
	// kill-switch).
	YoutubePaused func(ctx context.Context) bool
	// Activity, when set, records a FAILED metadata refresh for the Activity feed.
	Activity     ActivityRecorder
	Now          func() time.Time // injectable clock (defaults to time.Now)
	PollInterval time.Duration    // defaults to pollInterval
	// ResolveTimeout bounds one refresh, measured from the moment yt-dlp starts
	// rather than from when Resolve is entered. Defaults to resolveTimeout.
	// Injectable for the same reason download.Deps.Watchdog is: the production
	// value is minutes long, and the tests that matter are about what the cap
	// does and does not count.
	ResolveTimeout time.Duration
	Logger         *slog.Logger
}

// Worker keeps stored channel metadata from going stale. Construct with
// NewWorker and drive with Run.
//
// It is the fourth kind of thing that talks to YouTube (after downloads,
// channel scans and on-demand HTTP resolves) and deliberately the least
// urgent: it claims at most one channel per poll, it never runs without a
// valid cookie, it stops entirely while the kill-switch is on, and its claim
// queries keep it away from the same channel's video scan.
//
// It holds no random source: the rotation is a fixed slot per channel, not a
// jittered interval, so there is nothing here to scatter.
type Worker struct {
	d Deps
}

// NewWorker builds a Worker, filling in defaults for the optional Deps.
func NewWorker(d Deps) *Worker {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.PollInterval <= 0 {
		d.PollInterval = pollInterval
	}
	if d.ResolveTimeout == 0 {
		d.ResolveTimeout = resolveTimeout
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d}
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
		//
		// CookieStatus is called unconditionally, matching scan.Scheduler: a
		// nil-check here would make a caller that forgot to wire it fail OPEN
		// — silently disabling the gate and calling YouTube with an absent,
		// stale or blocked cookie. A nil dependency should take the process
		// down at the first pass, not quietly remove a protection.
		if w.d.CookieStatus(ctx) != "valid" && !w.d.AllowAnonymous {
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
		claimed := w.claim()
		if claimed == nil {
			if !w.sleep(ctx, w.d.PollInterval) {
				return
			}
			continue
		}
		w.refresh(ctx, claimed)
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
func (w *Worker) claim() *channels.Channel {
	now := w.d.Now().UTC().Format(sqlTimeLayout)
	store := w.d.Refresher.Channels

	due, err := store.ClaimDueMetadata(now)
	if err != nil {
		w.d.Logger.Error("channel metadata: claim due failed", "err", err)
		return nil
	}
	if due != nil {
		return due
	}
	unresolved, err := store.ClaimUnresolved(now)
	if err != nil {
		w.d.Logger.Error("channel metadata: claim unresolved failed", "err", err)
		return nil
	}
	return unresolved
}

// refresh re-reads one channel under a panic guard, then settles it. It takes
// the row the claim already matched rather than an id: re-reading it here would
// be a second query for a row we are holding, and its error path was one of the
// ways an attempt could end without recording itself.
//
// Settling happens on EVERY outcome — success, failure, or a recovered panic.
// An outcome that skipped it would leave the channel claimable forever and the
// worker would retry it every poll, which is exactly the hammering the whole
// design avoids. Nothing else is recorded: a failed refresh must not feed the
// dead-scan counter that auto-unsubscribes channels, because that decision
// belongs to the scan scheduler, which guards it against peeq's own cookie
// being the real problem.
func (w *Worker) refresh(ctx context.Context, cached *channels.Channel) {
	channelID := cached.ID
	defer func() {
		// This parses yt-dlp output and remote HTTP responses, both external
		// input. An unrecovered panic here would take down the whole process,
		// so it is contained the way every other peeq worker contains one.
		if r := recover(); r != nil {
			w.d.Logger.Error("channel metadata: recovered from panic", "channel_id", channelID, "panic", r)
		}
		w.settle(channelID)
	}()

	// The cap runs from when yt-dlp starts, not from when Resolve is entered.
	// This worker is on the BACKGROUND lane, so it queues behind every other
	// Runner call in flight; timing from entry counted that wait against the
	// process and killed a refresh that had not yet been allowed to begin.
	// The failure path here stamps resolve_ok = 0 — the one state peeq uses to
	// mean "this needs your attention" — so the channel got flagged for having
	// been patient.
	//
	// Resolve makes exactly one Runner call (Resolver.ResolveChannel), so the
	// hook fires once and the cap then covers that call plus the two image
	// fetches after it, which is the work this bound is actually about.
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	bound := ytdlp.NewDeferredTimer(w.d.ResolveTimeout, cancel)
	err := w.d.Refresher.Resolve(ytdlp.WithStartHook(rctx, bound.Start), channelID, cached)
	// Stop unconditionally, and NOT inside the && below: it is what disarms the
	// timer, so short-circuiting past it on the success path would leave an
	// AfterFunc holding cancel alive for the rest of the cap.
	stoppedInTime := bound.Stop()
	// A cap that fired means yt-dlp really did stall — the hook only arms the
	// timer once the process is running — so this says so rather than reporting
	// the bare "context canceled" it surfaces as.
	//
	// Stop() reporting false is NOT enough on its own: it says the same thing
	// for a timer that fired and for one that was never armed, and never-armed
	// is the ordinary outcome whenever the call returns before reaching exec —
	// the pause gate, the cookie gate, or a resolver that failed outright.
	// rctx.Err() is what separates them, since only the timer cancels it. The
	// ctx.Err() == nil term keeps an outer shutdown from being read as a stall.
	stalled := err != nil && !stoppedInTime && rctx.Err() != nil && ctx.Err() == nil
	if err != nil {
		summary := "metadata refresh failed"
		if stalled {
			summary = "metadata refresh stalled"
			w.d.Logger.Warn("channel metadata refresh stalled",
				"channel_id", channelID, "after", w.d.ResolveTimeout)
		}
		w.d.Logger.Warn("channel metadata refresh failed", "channel_id", channelID, "err", err)
		if w.d.Activity != nil {
			name := cached.Name
			if name == "" {
				name = channelID
			}
			w.d.Activity.Record(activity.Event{
				Kind: activity.KindChannelMeta, Outcome: activity.OutcomeWarn,
				SubjectID: channelID, Subject: name, Summary: summary,
			})
		}
		return
	}
	w.d.Logger.Info("channel metadata refreshed", "channel_id", channelID)
}

// settle takes the channel back out of the claim set, whatever happened to it.
// This is the loop-breaker, and it has to cover BOTH claim queries, because
// each is held off by a different column:
//
//   - the weekly rotation is held off by next_meta_refresh_at, pushed out to
//     the channel's own slot in the 7-day cycle;
//   - the never-read backlog is held off by channels.resolved_at, stamped only
//     if nothing else stamped it already.
//
// Rescheduling alone is not enough. An unsubscribed backlog channel has no
// subscriptions row, so MarkMetaRefreshed matches zero rows and reports no
// error — and if the attempt also died before Resolve could record itself (a
// panic mid-parse), resolved_at stays
// NULL and ClaimUnresolved returns that same channel on the next poll, forever.
// The conditional stamp closes that path without ever overwriting a real
// outcome.
func (w *Worker) settle(channelID string) {
	next := w.nextRefreshAt(channelID)
	if err := w.d.Refresher.Channels.MarkMetaRefreshed(channelID, next); err != nil {
		w.d.Logger.Error("channel metadata: reschedule failed", "channel_id", channelID, "err", err)
	}
	now := w.d.Now().UTC().Format(sqlTimeLayout)
	if err := w.d.Refresher.Channels.MarkResolveAttemptedIfUnset(channelID, now); err != nil {
		w.d.Logger.Error("channel metadata: record attempt failed", "channel_id", channelID, "err", err)
	}
}

// nextRefreshAt is the worker's own slot lookup: it reads the channel's rank
// among current subscriptions and returns the instant its next refresh belongs
// on. A failed rank query falls back to a plain interval — losing the even
// spacing for one cycle is cosmetic, whereas failing to reschedule would leave
// the channel claimable on every poll.
//
// A channel with no subscriptions row (the never-resolved backlog is mostly
// unsubscribed) still gets an answer — SubscriptionRank counts the ids below it
// and reports where it WOULD sit — and the answer is simply thrown away:
// MarkMetaRefreshed matches no row for it, so nothing is written. The backlog is
// held off by channels.resolved_at instead.
func (w *Worker) nextRefreshAt(channelID string) string {
	rank, count, err := w.d.Refresher.Channels.SubscriptionRank(channelID)
	if err != nil {
		w.d.Logger.Error("channel metadata: subscription rank failed", "channel_id", channelID, "err", err)
		return w.d.Now().Add(refreshInterval).UTC().Format(sqlTimeLayout)
	}
	return NextRefreshAt(w.d.Now(), rank, count)
}

// NextRefreshAt returns the instant the channel ranked rank-of-count should
// next be refreshed, in the SQLite text form next_meta_refresh_at is stored in.
//
// The weekly rotation is spread exactly like the daily scan and for exactly the
// same reason — see scan.NextScanAt for the argument in full. Each subscription
// owns a slot in the 7-day cycle (44 channels land 3.8 hours apart), and the
// search starts half a cycle out so a refresh pulled forward cannot immediately
// repeat. Migration 0005 scattered these rows once; a slot keeps them scattered
// through the restarts and cookie expiries that used to re-gather them.
//
// Both slots derive from the same rank, so for some channels the refresh comes
// due right beside that channel's own scan and ClaimDueMetadata defers it on
// ScanQuietWindow every week rather than occasionally. The two coincide when
// 6*rank is a multiple of the fleet size, which is gcd(6, count) channels —
// ranks 0 and 22 of production's 44, but six of them in a fleet of 12. That is
// the intended
// behaviour of the quiet window and it clears itself within a poll or two; it is
// written down because the recurrence looks like a stuck channel and is not.
//
// The small duplication with the scan package stays deliberate: importing it
// for twenty lines would tie two schedulers together that share only an idea.
func NextRefreshAt(now time.Time, rank, count int) string {
	slot := sched.Slot(rank, count, refreshInterval)
	return sched.NextSlotAfter(now.Add(refreshInterval/2), refreshInterval, slot).Format(sqlTimeLayout)
}

// sleep waits d, returning false if ctx was cancelled first.
func (w *Worker) sleep(ctx context.Context, d time.Duration) bool {
	return sched.Sleep(ctx, d)
}
