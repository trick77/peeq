// Package scan drives the single serial channel-scan scheduler: the one
// goroutine that periodically claims a due subscription, lists its recent
// uploads via yt-dlp, and records/classifies each video into the per-channel
// ledger (seen / pending / queued). Like the download worker it is serial by
// design — YouTube tolerates only so many calls, so scanning two channels at
// once buys nothing and risks a throttle. It is cookie-gated (no scanning
// without a valid cookie) and spaces consecutive channel scans by at least
// betweenChannels.
//
// Each subscription owns a slot in the 24-hour cycle, and every reschedule
// targets that slot — see NextScanAt for why the schedule is anchored to the
// cycle rather than to when the previous scan finished.
package scan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/failmonitor"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/sched"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
	"github.com/trick77/peeq/internal/ytgate"
)

const (
	scanInterval    = 24 * time.Hour
	betweenChannels = 60 * time.Second
	defaultListSize = 50
	scanBackoff     = time.Hour
	// scanBackoffJitter scatters the retry after a failed scan. The retry
	// itself is a fixed hour, but a failure that hits every channel at once
	// (an expired cookie is the common one) would otherwise re-queue the whole
	// fleet on the same instant and drain it back-to-back an hour later — the
	// convoy the slot schedule below exists to prevent, rebuilt by the error
	// path. Small next to the hour it scatters: this is a retry, not a place
	// to re-spread the fleet, which the next successful scan does anyway.
	scanBackoffJitter = 15 * time.Minute
	autoPriority      = 0 // below manual (10), matching Phase 1
)

// ChannelLister is the subset of *ytdlp.Runner the scheduler needs: a flat
// listing of a channel's recent uploads, plus one of its recent livestreams.
// Declaring it here (rather than importing the concrete Runner) keeps the
// scheduler testable with a fake that never shells out to yt-dlp; the real
// *ytdlp.Runner satisfies it.
//
// The tabs are two calls because YouTube keeps them apart: ordinary uploads
// never show under /streams and stream VODs never show under /videos, so
// listing both is the only way to see a channel whole.
type ChannelLister interface {
	ChannelVideos(ctx context.Context, ucid string, n int) ([]ytdlp.ChannelEntry, error)
	ChannelStreams(ctx context.Context, ucid string, n int) ([]ytdlp.ChannelEntry, error)
}

// JobEnqueuer is the subset of *jobs.Store scanOnce needs. Narrowed to an
// interface (rather than the concrete store) so tests can inject a
// transient-failure fake; the real *jobs.Store satisfies it.
type JobEnqueuer interface {
	Enqueue(videoID string, priority int) (int64, error)
}

// Deps are the scheduler's collaborators and tunables. The stores, Lister,
// and CookieStatus are required; the rest have safe defaults applied in New.
type Deps struct {
	Channels *channels.Store
	Ledger   *channelvideos.Store
	Videos   *videos.Store
	Jobs     JobEnqueuer
	Settings *settings.Store
	Lister   ChannelLister
	// Prober, when set, re-checks whether a video parked as unavailable
	// (members-only, age-gated, ...) has become reachable. Nil leaves parked
	// rows moving only on the listing's own availability field, which is
	// usually absent — see VideoProber. Production always sets it.
	Prober       VideoProber
	CookieStatus func(ctx context.Context) string // settings.CookieStatus
	// AllowAnonymous is the dev-only escape hatch (config.AllowAnonymousYoutube)
	// mirrored here: when true, a non-"valid" CookieStatus no longer skips the
	// poll, so the scheduler proceeds to scan with an absent cookie exactly
	// like the ytdlp.Runner's own cookieGate does. Callers must only ever set
	// this from the same boot-gated config value used to build the Runner.
	AllowAnonymous bool
	// YoutubePaused, when set and returning true, skips scan passes (the
	// kill-switch), beside the cookie gate.
	YoutubePaused func(ctx context.Context) bool
	// FailMonitor feeds the auto-pause heuristic: Fail(channelID) on a
	// count-worthy scan failure, Reset() on a clean pass.
	FailMonitor failmonitor.Sink
	// Activity records scan outcomes for the Activity feed. Optional (nil = off).
	Activity     activity.Recorder
	Now          func() time.Time // injectable clock (defaults to time.Now)
	PollInterval time.Duration    // idle re-check (default 30s)
	Logger       *slog.Logger

	// Images fetches a newly-pending video's poster (best-effort, off the
	// scan's critical path) so the inbox never loads it from YouTube in the
	// browser. Production passes ytdlp.Runner.FetchImage, so every poster is a
	// turn in the YouTube queue. Nil disables prefetch — most tests leave it
	// unset.
	Images media.ImageFetcher

	// listSize is a test seam: how many entries to request per channel.
	// Zero selects defaultListSize.
	listSize int
}

// Scheduler is the scan loop. Construct with New and drive with Run.
type Scheduler struct {
	d            Deps
	lastScanTime time.Time // in-memory; enforces betweenChannels spacing
	rand         func() float64
	// thumbs feeds the thumbnail drainer Run owns; see thumbs.go.
	thumbs chan thumbJob
	// gate is the per-pass cookie and kill-switch check, built from Deps.
	gate ytgate.Gate
}

// New builds a Scheduler, filling in defaults for the optional Deps fields.
func New(d Deps) *Scheduler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.PollInterval <= 0 {
		d.PollInterval = 30 * time.Second
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.listSize <= 0 {
		d.listSize = defaultListSize
	}
	return &Scheduler{
		d: d, rand: sched.PseudoRand(), thumbs: make(chan thumbJob, prefetchQueueSize),
		gate: ytgate.Gate{CookieStatus: d.CookieStatus, AllowAnonymous: d.AllowAnonymous, Paused: d.YoutubePaused},
	}
}

// Run is the scan loop; it blocks until ctx is cancelled. Each pass is
// cookie-gated (a non-valid cookie skips the pass so we never hammer YouTube
// without credentials), then claims the single oldest due subscription,
// enforces the betweenChannels spacing, and scans it. A scan error backs the
// subscription off by scanBackoff without advancing its baseline.
func (s *Scheduler) Run(ctx context.Context) {
	// The thumbnail drainers live exactly as long as the loop: Run does not
	// return until they have stopped, so main's WaitGroup covers them and no
	// prefetch can write after the database is closed.
	defer s.runThumbnailDrainers(ctx)()
	for {
		if ctx.Err() != nil {
			return
		}
		// Cookie and kill-switch gates: no valid cookie (unless the dev-only
		// anonymous escape hatch is on) or youtube_paused → skip this pass.
		// Re-read each poll, so a pasted cookie or a cleared switch resumes
		// scanning by itself. See ytgate.Gate.
		if !s.gate.Open(ctx) {
			if !s.sleep(ctx, s.d.PollInterval) {
				return
			}
			continue
		}
		nowStr := s.d.Now().UTC().Format(store.TimeLayout)
		sub, err := s.d.Channels.ClaimDue(nowStr)
		if err != nil {
			s.d.Logger.Error("scan: claim due failed", "err", err)
			if !s.sleep(ctx, s.d.PollInterval) {
				return
			}
			continue
		}
		if sub == nil {
			if !s.sleep(ctx, s.d.PollInterval) {
				return
			}
			continue
		}
		// >=60s between channel scans.
		if wait := betweenChannels - s.d.Now().Sub(s.lastScanTime); wait > 0 {
			if !s.sleep(ctx, wait) {
				return
			}
		}
		s.lastScanTime = s.d.Now()
		s.scanChannel(ctx, sub)
	}
}

// scanChannel runs one channel's scan under a panic guard. On a scan error OR
// a recovered panic it backs the subscription off by scanBackoff (without
// advancing baselined_at), so a persistently-failing or panicking channel is
// bounded to roughly one attempt per hour rather than being re-claimed every
// betweenChannels forever.
func (s *Scheduler) scanChannel(ctx context.Context, sub *channels.Subscription) {
	// requested = a user pressed "Scan now" and is owed an answer for THIS pass.
	// Read once, up front: the row is rewritten below (MarkScanned clears the
	// marker), so consulting it again later would report the wrong thing.
	requested := sub.ScanRequestedAt != ""
	// Registered first so it runs LAST: whatever happens below — clean pass,
	// classified failure, panic — the marker is spent. Leaving it set would let
	// some later AUTOMATIC pass announce itself as the answer to a request the
	// user has already been told about. MarkScanned also clears it on the success
	// path; clearing twice is idempotent and costs one write per manual check.
	defer func() {
		if !requested {
			return
		}
		if ctx.Err() != nil {
			// Process shutdown: no scan ran and nobody was told, so the request
			// is still owed an answer. Leave the marker for the next boot.
			return
		}
		if err := s.d.Channels.ClearScanRequest(sub.ChannelID, sub.ScanRequestedAt); err != nil {
			s.d.Logger.Error("scan: clear scan request failed", "channel_id", sub.ChannelID, "err", err)
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			s.d.Logger.Error("scan: recovered from panic", "channel_id", sub.ChannelID, "panic", r)
			// A panic records nothing today, which is right for an automatic pass
			// (the operator has the ERROR above) but not for a requested one: the
			// user is watching a "Queued" button and would otherwise wait forever.
			s.recordRequestedFail(requested, sub.ChannelID, "internal error")
			s.backoff(sub.ChannelID)
		}
	}()
	if err := s.scanOnce(ctx, sub); err != nil {
		if ctx.Err() != nil {
			// Process shutdown mid-scan: not this channel failing. No cookie
			// flip, no auto-pause count, no "scan failed" row, no backoff, and
			// a "Scan now" marker stays set — the next boot claims it again.
			return
		}
		// A bot-block or a dead cookie surfaced by a SCAN (not a download) must
		// flip cookie_status the same way the download worker's pause() does —
		// otherwise the scheduler's own cookie gate (CookieStatus != "valid")
		// never trips and it keeps polling YouTube on a dead cookie forever
		// while the UI stays green. The normal Backoff still applies below; it
		// is harmless, since the flipped status stops all scanning next pass
		// until the user re-pastes a cookie.
		var terminal *ytdlp.TerminalError
		switch {
		case errors.Is(err, ytdlp.ErrBlocked):
			if serr := s.d.Settings.SetCookie(ctx, "", settings.CookieBlocked); serr != nil {
				s.d.Logger.Error("scan: set cookie status failed", "status", "blocked", "err", serr)
			}
			s.recordScanFail(sub.ChannelID, requested, "YouTube blocked the request")
		case errors.Is(err, ytdlp.ErrCookieExpired):
			if serr := s.d.Settings.SetCookie(ctx, "", settings.CookieStale); serr != nil {
				s.d.Logger.Error("scan: set cookie status failed", "status", "stale", "err", serr)
			}
			s.recordScanFail(sub.ChannelID, requested, "cookie expired")
		case errors.Is(err, ytdlp.ErrPaused):
			// Kill-switch tripped mid-scan: not a real failure, so don't
			// feed FailMonitor. The YoutubePaused gate in Run parks the
			// loop next iteration; the plain backoff below still applies
			// (harmless, since scanning is gated off anyway).
			s.recordRequestedFail(requested, sub.ChannelID, "YouTube access is paused")
		case errors.Is(err, ytdlp.ErrNoCookie):
			// No cookie at all: race-only and self-limiting — the scheduler's
			// own cookie gate stops scanning next pass — so it must not count
			// toward the shared auto-pause heuristic, mirroring the download
			// worker's classify. Leave cookie_status ('absent') as-is.
			s.recordRequestedFail(requested, sub.ChannelID, "no YouTube cookie")
		case errors.As(err, &terminal):
			// Terminal ytdlp error (members-only/deleted/private/age/geo
			// channel): permanent and per-channel-expected, mirroring the
			// download worker's classify — don't count it toward the
			// shared auto-pause heuristic.
			s.staleUnsubscribe(ctx, sub.ChannelID, terminal.Reason)
			// staleUnsubscribe stays silent until the dead-scan threshold, which
			// is right for a background pass but leaves a requested check with no
			// answer for the first N attempts.
			s.recordRequestedFail(requested, sub.ChannelID, terminal.Reason)
		default:
			// Everything else (transient/unclassified failures) is
			// count-worthy for the shared auto-pause heuristic; the two
			// cookie-status branches above already have their own signal and
			// are not double-counted here.
			if s.d.FailMonitor != nil {
				s.d.FailMonitor.Fail(sub.ChannelID)
			}
			s.recordScanFail(sub.ChannelID, requested, "scan failed")
		}
		s.d.Logger.Warn("scan failed; backing off", "channel_id", sub.ChannelID, "err", err)
		s.backoff(sub.ChannelID)
	}
}

// staleUnsubscribe applies the dead-channel rule. It is deliberately inert
// while YouTube access is unhealthy: a stale cookie can make EVERY channel
// fail at once, and acting on that would empty the subscription list. The
// counter is not even incremented, so an outage cannot accumulate toward the
// threshold and then fire the instant access is restored.
func (s *Scheduler) staleUnsubscribe(ctx context.Context, channelID, reason string) {
	if reason != channels.ReasonDeleted {
		// A non-deleted terminal reason (private/members/age/geo) is itself
		// positive evidence the channel is ALIVE: yt-dlp reached it and
		// classified real content, not an absence. Without this reset, the
		// dead-scan counter is merely a count of the last N scans' outcomes
		// rather than a count of CONSECUTIVE dead scans (which its own doc
		// comment promises), so a sequence like deleted, deleted, members,
		// deleted would unsubscribe on the 4th scan despite the 3rd scan
		// proving the channel was reachable in between.
		//
		// This reset is unconditional, deliberately placed BEFORE (and
		// independent of) the pause/cookie interlock below. That interlock
		// exists solely to stop RecordDeadScan from trusting a "deleted"
		// verdict that might really be a symptom of OUR OWN broken cookie
		// (a stale/blocked/absent cookie can make yt-dlp misreport a live
		// channel as gone). A private/members/age/geo classification is not
		// that failure mode — it is yt-dlp successfully parsing a real
		// channel response, which is trustworthy on its own merits and
		// should not be withheld just because our cookie also happens to be
		// unhealthy right now. And per ResetDeadScan's own contract, a reset
		// only ever delays a future unsubscribe, never causes a wrong one,
		// so applying it unconditionally here cannot itself be unsafe.
		if err := s.d.Channels.ResetDeadScan(channelID); err != nil {
			s.d.Logger.Error("scan: reset dead scan failed", "channel_id", channelID, "err", err)
		}
		return
	}
	if paused, _, _ := s.d.Settings.YoutubePaused(ctx); paused {
		return
	}
	// Allowlist, not a denylist: only "valid" proceeds. The schema's CHECK
	// constraint happens to enumerate exactly four cookie_status values
	// today, but this switch used to deny-list three of them ("blocked",
	// "stale", "absent") and fail OPEN for anything else — a future fifth
	// status (e.g. a new degraded state) would have silently bypassed the
	// interlock and let dead-scan counting continue during an outage.
	// Requiring the one known-good value instead fails CLOSED against any
	// status this code doesn't yet know about.
	if s.d.Settings.CookieStatus(ctx) != "valid" {
		return
	}
	n, err := s.d.Channels.RecordDeadScan(channelID)
	if err != nil {
		s.d.Logger.Error("scan: record dead scan failed", "channel_id", channelID, "err", err)
		return
	}
	if n < channels.DeadScanThreshold {
		return
	}
	at := s.d.Now().UTC().Format(store.TimeLayout)
	if err := s.d.Channels.AutoUnsubscribe(channelID, channels.ReasonDeleted, at); err != nil {
		s.d.Logger.Error("scan: auto unsubscribe failed", "channel_id", channelID, "err", err)
		return
	}
	s.d.Logger.Info("scan: auto-unsubscribed dead channel", "channel_id", channelID, "reason", channels.ReasonDeleted, "dead_scans", n)
	activity.Record(s.d.Activity, activity.Event{
		Kind: activity.KindScan, Outcome: activity.OutcomeWarn,
		SubjectID: channelID, Subject: s.channelName(channelID),
		Summary: "auto-unsubscribed", Detail: fmt.Sprintf("gone on %d scans in a row", n),
	})
}

// recordScanFail records a scan failure with its classified reason. The
// kill-switch pause and the terminal (stale-unsubscribe) case deliberately do
// NOT come here — a pause is not a failure, and staleUnsubscribe records its own
// warn — so a failure row always means "this channel's scan actually broke".
//
// requested only changes the wording: a user who pressed "Scan now" is reading
// the answer to their click, so the row says "check failed" rather than naming a
// background scan they never asked about.
func (s *Scheduler) recordScanFail(channelID string, requested bool, reason string) {
	summary := "scan failed"
	if requested {
		summary = "check failed"
	}
	activity.Record(s.d.Activity, activity.Event{
		Kind: activity.KindScan, Outcome: activity.OutcomeFail,
		SubjectID: channelID, Subject: s.channelName(channelID),
		Summary: summary, Detail: reason,
	})
}

// recordRequestedFail is recordScanFail for the branches that deliberately stay
// silent on an automatic pass: a kill-switch pause, a missing cookie, a terminal
// per-channel verdict, a panic. None of those is a failure worth logging when
// nobody asked — but when someone did, silence is the bug being fixed here, so
// they get a row. A no-op unless requested.
func (s *Scheduler) recordRequestedFail(requested bool, channelID, reason string) {
	if !requested {
		return
	}
	s.recordScanFail(channelID, true, reason)
}

// channelName resolves a channel's display name for an activity record, falling
// back to the id. Best-effort: a lookup failure must never affect a scan.
func (s *Scheduler) channelName(channelID string) string {
	if s.d.Channels != nil {
		if c, err := s.d.Channels.Get(channelID); err == nil && c != nil && c.Name != "" {
			return c.Name
		}
	}
	return channelID
}

// backoff pushes a subscription's next_scan_at out by roughly scanBackoff,
// leaving baselined_at and last_scanned_at untouched.
//
// Roughly, not exactly: the retry is scattered by scanBackoffJitter so a fleet
// that failed together does not come back together. It deliberately does NOT go
// to the channel's slot — a failure should be retried in an hour, not tomorrow —
// and the scan that finally succeeds is what puts the channel back on its slot.
func (s *Scheduler) backoff(channelID string) {
	d := sched.JitteredInterval(scanBackoff, scanBackoffJitter, time.Minute, s.rand)
	next := s.d.Now().Add(d).UTC().Format(store.TimeLayout)
	if err := s.d.Channels.Backoff(channelID, next); err != nil {
		s.d.Logger.Error("scan: backoff failed", "channel_id", channelID, "err", err)
	}
}

// sleep waits d unless ctx is cancelled first. It returns false if ctx was
// cancelled (the caller should stop), true if the full wait elapsed.
func (s *Scheduler) sleep(ctx context.Context, d time.Duration) bool {
	return sched.Sleep(ctx, d)
}
