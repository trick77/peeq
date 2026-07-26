// Package scan drives the single serial channel-scan scheduler: the one
// goroutine that periodically claims a due subscription, lists its recent
// uploads via yt-dlp, and records/classifies each video into the per-channel
// ledger (seen / pending / queued). Like the download worker it is serial by
// design — YouTube tolerates only so many calls, so scanning two channels at
// once buys nothing and risks a throttle. It is cookie-gated (no scanning
// without a valid cookie) and spaces consecutive channel scans by at least
// betweenChannels.
package scan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/sched"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

const (
	scanInterval    = 24 * time.Hour
	scanJitter      = 3 * time.Hour
	betweenChannels = 60 * time.Second
	defaultListSize = 50
	scanBackoff     = time.Hour
	autoPriority    = 0                     // below manual (10), matching Phase 1
	sqlTimeLayout   = "2006-01-02 15:04:05" // SQLite datetime('now') text form (UTC)
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

// FailMonitor is the subset of *failmonitor.Monitor the scheduler uses to
// feed the auto-pause heuristic. Nil disables it (tests that don't care).
// Mirrors the download package's FailMonitor interface of the same shape —
// the two consumers dedup independently, keyed by their own entity kind
// (video id for downloads, channel id for scans).
type FailMonitor interface {
	Fail(entityID string)
	Reset()
}

// ActivityRecorder records a background-work event for the Activity feed.
// Narrow and nil-safe (like FailMonitor): tests leave it nil, main.go wires the
// one shared *activity.Store. The scheduler declares its own so it does not have
// to import a concrete recorder.
type ActivityRecorder interface {
	Record(activity.Event)
}

// Deps are the scheduler's collaborators and tunables. The stores, Lister,
// and CookieStatus are required; the rest have safe defaults applied in New.
type Deps struct {
	Channels     *channels.Store
	Ledger       *channelvideos.Store
	Videos       *videos.Store
	Jobs         JobEnqueuer
	Settings     *settings.Store
	Lister       ChannelLister
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
	FailMonitor FailMonitor
	// Activity records scan outcomes for the Activity feed. Optional (nil = off).
	Activity     ActivityRecorder
	Now          func() time.Time // injectable clock (defaults to time.Now)
	PollInterval time.Duration    // idle re-check (default 30s)
	Logger       *slog.Logger

	// listSize is a test seam: how many entries to request per channel.
	// Zero selects defaultListSize.
	listSize int
}

// Scheduler is the scan loop. Construct with New and drive with Run.
type Scheduler struct {
	d            Deps
	lastScanTime time.Time // in-memory; enforces betweenChannels spacing
	rand         func() float64
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
	return &Scheduler{d: d, rand: sched.PseudoRand()}
}

// Run is the scan loop; it blocks until ctx is cancelled. Each pass is
// cookie-gated (a non-valid cookie skips the pass so we never hammer YouTube
// without credentials), then claims the single oldest due subscription,
// enforces the betweenChannels spacing, and scans it. A scan error backs the
// subscription off by scanBackoff without advancing its baseline.
func (s *Scheduler) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		// Cookie gate: no valid cookie → don't scan (don't hammer), UNLESS the
		// dev-only anonymous escape hatch is enabled, in which case the poll
		// proceeds without a cookie exactly like ytdlp.Runner's cookieGate.
		if s.d.CookieStatus(ctx) != "valid" && !s.d.AllowAnonymous {
			if !s.sleep(ctx, s.d.PollInterval) {
				return
			}
			continue
		}
		// Kill-switch gate: youtube_paused → skip this pass. Re-checked each
		// poll, so clearing the flag resumes scanning automatically.
		if s.d.YoutubePaused != nil && s.d.YoutubePaused(ctx) {
			if !s.sleep(ctx, s.d.PollInterval) {
				return
			}
			continue
		}
		nowStr := s.d.Now().UTC().Format(sqlTimeLayout)
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
	// requested = a user pressed "Check now" and is owed an answer for THIS pass.
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
		if err := s.d.Channels.ClearScanRequest(sub.ChannelID, sub.ScanRequestedAt); err != nil {
			s.d.Logger.Error("scan: clear scan request failed", "channel", sub.ChannelID, "err", err)
		}
	}()
	defer func() {
		if r := recover(); r != nil {
			s.d.Logger.Error("scan: recovered from panic", "channel", sub.ChannelID, "panic", r)
			// A panic records nothing today, which is right for an automatic pass
			// (the operator has the ERROR above) but not for a requested one: the
			// user is watching a "Queued" button and would otherwise wait forever.
			s.recordRequestedFail(requested, sub.ChannelID, "internal error")
			s.backoff(sub.ChannelID)
		}
	}()
	if err := s.scanOnce(ctx, sub); err != nil {
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
			if serr := s.d.Settings.SetCookie(ctx, "", "blocked"); serr != nil {
				s.d.Logger.Error("scan: set cookie status failed", "status", "blocked", "err", serr)
			}
			s.recordScanFail(sub.ChannelID, requested, "YouTube blocked the request")
		case errors.Is(err, ytdlp.ErrCookieExpired):
			if serr := s.d.Settings.SetCookie(ctx, "", "stale"); serr != nil {
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
		s.d.Logger.Warn("scan failed; backing off", "channel", sub.ChannelID, "err", err)
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
			s.d.Logger.Error("scan: reset dead scan failed", "channel", channelID, "err", err)
		}
		return
	}
	if paused, _ := s.d.Settings.YoutubePaused(ctx); paused {
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
		s.d.Logger.Error("scan: record dead scan failed", "channel", channelID, "err", err)
		return
	}
	if n < channels.DeadScanThreshold {
		return
	}
	at := s.d.Now().UTC().Format(sqlTimeLayout)
	if err := s.d.Channels.AutoUnsubscribe(channelID, channels.ReasonDeleted, at); err != nil {
		s.d.Logger.Error("scan: auto unsubscribe failed", "channel", channelID, "err", err)
		return
	}
	s.d.Logger.Info("scan: auto-unsubscribed dead channel", "channel", channelID, "reason", channels.ReasonDeleted, "dead_scans", n)
	s.recordActivity(activity.Event{
		Kind: activity.KindScan, Outcome: activity.OutcomeWarn,
		SubjectID: channelID, Subject: s.channelName(channelID),
		Summary: "auto-unsubscribed", Detail: fmt.Sprintf("gone on %d scans in a row", n),
	})
}

// recordActivity records a scan event for the Activity feed, nil-safe.
func (s *Scheduler) recordActivity(e activity.Event) {
	if s.d.Activity != nil {
		s.d.Activity.Record(e)
	}
}

// recordScanFail records a scan failure with its classified reason. The
// kill-switch pause and the terminal (stale-unsubscribe) case deliberately do
// NOT come here — a pause is not a failure, and staleUnsubscribe records its own
// warn — so a failure row always means "this channel's scan actually broke".
//
// requested only changes the wording: a user who pressed "Check now" is reading
// the answer to their click, so the row says "check failed" rather than naming a
// background scan they never asked about.
func (s *Scheduler) recordScanFail(channelID string, requested bool, reason string) {
	summary := "scan failed"
	if requested {
		summary = "check failed"
	}
	s.recordActivity(activity.Event{
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

// backoff pushes a subscription's next_scan_at out by scanBackoff, leaving
// baselined_at and last_scanned_at untouched.
func (s *Scheduler) backoff(channelID string) {
	next := s.d.Now().Add(scanBackoff).UTC().Format(sqlTimeLayout)
	if err := s.d.Channels.Backoff(channelID, next); err != nil {
		s.d.Logger.Error("scan: backoff failed", "channel", channelID, "err", err)
	}
}

// scanOnce lists sub's recent uploads and records each into the ledger. On a
// first-run baseline (sub.BaselinedAt == "") every current id is recorded as
// 'seen' and NOTHING is queued — only subsequent scans act on genuinely-new
// ids. On later scans a new id is filtered (sub-min-duration / upcoming /
// live → 'seen'), auto-downloaded ('queued' + videos row + job), or left
// 'pending' for a manual decision. Finally the subscription's scan schedule
// is stamped (and its baseline recorded on the first pass).
func (s *Scheduler) scanOnce(ctx context.Context, sub *channels.Subscription) error {
	set, err := s.d.Settings.Get(ctx)
	if err != nil {
		return fmt.Errorf("scan: settings: %w", err)
	}
	// Read the baseline flag BEFORE listing: a first pass needs a complete
	// snapshot, so listChannel is stricter about a half-listed channel there.
	baseline := sub.BaselinedAt == ""
	entries, streamCount, err := s.listChannel(ctx, sub.ChannelID, baseline)
	if err != nil {
		return err
	}
	// Tally for the Activity record: how many genuinely-new uploads were queued
	// automatically vs left for a manual decision, and (on the first pass) how
	// many the baseline snapshot recorded.
	var queuedCount, pendingCount, baselineCount, backlogCount int
	for _, e := range entries {
		exists, err := s.d.Ledger.Exists(e.ID)
		if err != nil {
			return err
		}
		if exists {
			// Dedup vs ledger — but heal the row's date first. Rows written
			// before migration 0008 have none, and a known video is never
			// revisited anywhere else, so this is the only chance an item
			// already sitting in the inbox has to gain one. Fills once, then
			// no-ops; see Ledger.SetPublishedAt.
			if err := s.d.Ledger.SetPublishedAt(e.ID, e.PublishedAt); err != nil {
				return err
			}
			continue
		}
		if v, err := s.d.Videos.Get(e.ID); err != nil {
			return err
		} else if v != nil {
			continue // dedup vs videos (manually added / already downloaded)
		}
		entry := channelvideos.Entry{
			VideoID: e.ID, ChannelID: sub.ChannelID, Title: e.Title,
			DurationSeconds: e.DurationSeconds, URL: e.URL, ThumbnailURL: e.ThumbnailURL,
			PublishedAt: e.PublishedAt,
		}
		switch {
		case baseline:
			entry.State = "seen"
			baselineCount++
		case isUnfinishedStream(e):
			// Record NOTHING for a stream that has not finished. 'seen'
			// is terminal — Ledger.Exists matches on video_id with no state
			// predicate, and nothing anywhere revisits a seen row — so writing
			// one here would lose the stream permanently, including after it
			// ends and becomes an ordinary video. That is silent data loss on a
			// channel whose uploads are mostly livestreams: every launch stream
			// caught mid-broadcast disappears for good.
			//
			// Leaving the id unknown is what makes it recoverable: the next pass
			// re-encounters it and classifies it normally once yt-dlp stops
			// reporting it as live. Baseline is matched first on purpose — a
			// first pass is a deliberate snapshot of "everything that already
			// existed", and that includes a stream running at the time.
			continue
		case isBackCatalogue(e.PublishedAt, sub.BaselinedAt):
			// Published before this channel was ever followed, so it is back
			// catalogue no matter how new it looks to the ledger. Terminal
			// 'seen' is right: the user subscribed to be told what a channel
			// posts NEXT, and an old upload will not become new later.
			//
			// This branch exists because "absent from the ledger" is only a
			// proxy for "new", and the proxy holds only while the set of
			// listed sources never changes. Adding the /streams tab broke it:
			// every stream VOD ever published was missing from the ledger of
			// every already-baselined channel, so a whole back catalogue
			// arrived at once as 'pending' (or, with autodownload, 'queued' —
			// fifty real downloads). The baseline branch above cannot cover
			// that, since it keys on BaselinedAt == "" and those channels were
			// baselined long ago. A publish-date gate is the general fix: any
			// source added later is covered without a further code change.
			//
			// ORDER IS LOAD-BEARING, both sides:
			//   - AFTER isUnfinishedStream: an in-flight broadcast's date is
			//     its START, which can predate the baseline. Gating it would
			//     write the terminal row that branch exists to avoid.
			//   - BEFORE Autodownload: otherwise the expensive version of this
			//     bug (a back catalogue downloaded to disk) survives.
			entry.State = "seen"
			backlogCount++
		case !passesFilters(e, set.MinVideoDurationSeconds):
			// Reached only for the duration floor now (the live case above is
			// matched first). Terminal 'seen' is right here: a video too short
			// today will not grow longer tomorrow, so re-listing it every pass
			// forever would buy nothing.
			entry.State = "seen"
		case sub.Autodownload:
			entry.State = "queued"
			queuedCount++
		default:
			entry.State = "pending"
			pendingCount++
		}
		// Autodownload: enqueue FIRST, then record the ledger row LAST. If
		// enqueueAuto fails part-way, the ledger row is never written, so the
		// next scan re-encounters the id and the videos-table dedup (the row
		// enqueueAuto Upserted) catches the half-done id — rather than a
		// premature 'queued' ledger row permanently masking a video that was
		// never actually enqueued.
		if entry.State == "queued" {
			if err := s.enqueueAuto(e, sub); err != nil {
				return err
			}
		}
		if err := s.d.Ledger.Insert(entry); err != nil {
			return err
		}
	}
	next := s.d.Now().Add(s.jitteredInterval()).UTC().Format(sqlTimeLayout)
	lastScanned := s.d.Now().UTC().Format(sqlTimeLayout)
	if err := s.d.Channels.MarkScanned(sub.ChannelID, baseline, lastScanned, next, sub.ScanRequestedAt); err != nil {
		return err
	}
	// Clean scan pass: reset the consecutive dead-scan streak (a channel that
	// recovers between unrelated failures must not creep toward
	// auto-unsubscription) and the shared failure streak (Reset() clears the
	// whole shared streak globally, not just for this channel).
	if err := s.d.Channels.ResetDeadScan(sub.ChannelID); err != nil {
		s.d.Logger.Error("scan: reset dead scan failed", "channel", sub.ChannelID, "err", err)
	}
	if s.d.FailMonitor != nil {
		s.d.FailMonitor.Reset()
	}

	// A completed pass was invisible until now: nothing logged it at any level,
	// so "did my scan actually run?" could only be answered from the database.
	// One INFO line per channel per day is cheap and makes the container log the
	// first place to look.
	newCount := queuedCount + pendingCount
	// streams is broken out because it is the one number that answers "does this
	// channel publish through livestreams?" — the reason the second tab is
	// listed at all — without opening the database. It is the raw /streams tab
	// count, not the post-dedup one, so it stays honest for a channel whose
	// streams also surface elsewhere.
	// backlog is broken out because suppressing items in bulk is exactly the kind
	// of thing that must never be silent — the one-shot burst when a new source
	// tab first appears should be readable in the log, not inferred from an
	// inbox that stayed empty.
	s.d.Logger.Info("scan complete", "channel", sub.ChannelID,
		"listed", len(entries), "streams", streamCount, "new", newCount,
		"backlog", backlogCount)

	// Activity record. The silence rule applies: a scan that surfaced nothing new
	// (the common case) writes nothing, so the agenda is not a wall of "0 new".
	// The first-run baseline is worth one row — it explains why a freshly
	// subscribed channel queued nothing. A REQUESTED scan is the deliberate
	// exception: the user pressed a button and is owed an answer, so it reports
	// even a nothing-new result rather than leaving them with no evidence the
	// check ran at all.
	switch {
	case baseline:
		s.recordActivity(activity.Event{
			Kind: activity.KindScan, Outcome: activity.OutcomeOK,
			SubjectID: sub.ChannelID, Subject: s.channelName(sub.ChannelID),
			Summary: fmt.Sprintf("baselined %d videos", baselineCount),
		})
	case newCount > 0:
		var parts []string
		if queuedCount > 0 {
			parts = append(parts, fmt.Sprintf("%d queued", queuedCount))
		}
		if pendingCount > 0 {
			parts = append(parts, fmt.Sprintf("%d to decide", pendingCount))
		}
		if backlogCount > 0 {
			parts = append(parts, fmt.Sprintf("%d older skipped", backlogCount))
		}
		s.recordActivity(activity.Event{
			Kind: activity.KindScan, Outcome: activity.OutcomeOK,
			SubjectID: sub.ChannelID, Subject: s.channelName(sub.ChannelID),
			Summary: fmt.Sprintf("%d new", newCount), Detail: strings.Join(parts, ", "),
		})
	case backlogCount > 0:
		// Nothing new, but a pile of history was just swallowed — almost always
		// the first pass after a new source tab starts being listed. The silence
		// rule does not apply to it: this is a one-off, it explains an otherwise
		// unexplained burst of work, and staying quiet here is the same invisible
		// bulk behaviour that made the flood so unwelcome in the first place.
		// No Detail. It used to read "published before you followed this
		// channel", which defines the word "older" that the summary already
		// used. One channel at a time that is a helpful gloss; History shows
		// many at once, and on a first pass over a subscription list it was the
		// same sentence down six consecutive rows, crowding out the counts that
		// actually differed. The word "older" carries it.
		s.recordActivity(activity.Event{
			Kind: activity.KindScan, Outcome: activity.OutcomeOK,
			SubjectID: sub.ChannelID, Subject: s.channelName(sub.ChannelID),
			Summary: fmt.Sprintf("%d older videos skipped", backlogCount),
		})
	case sub.ScanRequestedAt != "":
		// Requested, and it found nothing. The two cases above already answer a
		// requested scan when there IS something to report, so this is only the
		// nothing-new receipt — the row that turns "the button did nothing" into
		// "peeq looked, and there was nothing there". Read from the in-memory
		// subscription: MarkScanned above has already cleared the column.
		s.recordActivity(activity.Event{
			Kind: activity.KindScan, Outcome: activity.OutcomeOK,
			SubjectID: sub.ChannelID, Subject: s.channelName(sub.ChannelID),
			Summary: "checked on request", Detail: "nothing new",
		})
	}
	return nil
}

// listChannel lists a channel's recent uploads AND its recent livestreams,
// returning them as one list (uploads first, deduped by id in case an item ever
// surfaces on both tabs).
//
// The two calls are not equals. A real /videos failure fails the scan, as it
// always has. /streams failing does NOT: the tab is absent entirely for any
// channel that has never gone live, which is most of them, so a failure there
// means "no streams" and the uploads still stand. The exceptions are the two
// account-wide sentinels — a bot block or a dead cookie is not a fact about this
// tab, and swallowing it would leave the cookie status un-flipped and the next
// channel walking into the same wall.
//
// A MISSING TAB is tolerated on either side, symmetrically. A channel whose
// output is entirely livestreams has no /videos tab at all, and yt-dlp refuses
// it exactly the way it refuses /streams on a channel that never streamed.
// Failing the scan there would make the very channels this two-tab listing
// exists for unscannable forever, so an absent /videos tab means "no uploads"
// and the /streams call still runs. A deleted channel is unaffected: Classify
// maps it to a TerminalError, which IsMissingTab never matches, so
// auto-unsubscribe still sees it.
//
// baseline tightens the streams rule for a first pass only. The baseline
// snapshot is the one listing that must be COMPLETE: everything it fails to see
// counts as new on the next pass, so swallowing a transient /streams failure
// there would dump a channel's whole back catalogue of VODs into the inbox (and,
// with autodownload on, into the download queue). A genuinely absent tab is
// still quiet — that case is IsMissingTab, and there is nothing to miss.
//
// The returned count is how many entries the /streams tab listed, which is what
// answers "does this channel publish through livestreams?" — deliberately not
// the post-dedup number, which would read 0 for a channel whose streams happen
// to also surface on /videos.
func (s *Scheduler) listChannel(ctx context.Context, ucid string, baseline bool) ([]ytdlp.ChannelEntry, int, error) {
	uploads, err := s.d.Lister.ChannelVideos(ctx, ucid, s.d.listSize)
	switch {
	case err == nil:
	case ytdlp.IsMissingTab(err):
		s.d.Logger.Debug("scan: channel has no videos tab", "channel", ucid)
		uploads = nil
	default:
		// Return before spending a second throttled call on a channel whose
		// first call already failed.
		return nil, 0, fmt.Errorf("scan: list %s: %w", ucid, err)
	}
	streams, serr := s.d.Lister.ChannelStreams(ctx, ucid, s.d.listSize)
	switch {
	case serr == nil:
	case errors.Is(serr, ytdlp.ErrBlocked), errors.Is(serr, ytdlp.ErrCookieExpired):
		return nil, 0, fmt.Errorf("scan: list streams %s: %w", ucid, serr)
	case ytdlp.IsMissingTab(serr):
		s.d.Logger.Debug("scan: channel has no streams tab", "channel", ucid)
	case baseline:
		return nil, 0, fmt.Errorf("scan: baseline list streams %s: %w", ucid, serr)
	default:
		s.d.Logger.Warn("scan: listing streams failed, using uploads only",
			"channel", ucid, "err", serr)
	}
	if len(streams) == 0 {
		return uploads, 0, nil
	}
	// Built fresh rather than appended onto uploads: the slice came from the
	// lister and is not ours to grow into.
	merged := make([]ytdlp.ChannelEntry, 0, len(uploads)+len(streams))
	merged = append(merged, uploads...)
	seen := make(map[string]struct{}, len(uploads))
	for _, e := range uploads {
		seen[e.ID] = struct{}{}
	}
	for _, e := range streams {
		if _, dup := seen[e.ID]; dup {
			continue
		}
		seen[e.ID] = struct{}{}
		merged = append(merged, e)
	}
	return merged, len(streams), nil
}

// isBackCatalogue reports whether an entry was published before the channel was
// first followed, i.e. whether it belongs to the back catalogue the baseline
// pass was supposed to swallow.
//
// The cutoff is baselined_at — "everything that already existed when I started
// following this channel" — and deliberately NOT last_scanned_at, which is the
// tempting reading of "what's new since the last check". last_scanned_at would
// be wrong in a way that loses data: a broadcast that started three days ago and
// only settles into a VOD today has a publish date older than the last scan, so
// it would be marked terminally 'seen' and vanish. That is exactly the silent
// loss the unfinished-stream branch above was written to prevent. Ongoing
// "new since last check" is already the ledger's job; this gate only has to
// answer the question the ledger cannot — whether a source peeq has not looked
// at before is showing us history.
//
// BOTH empty cases fail OPEN (not back catalogue) on purpose:
//   - no publish date: yt-dlp omitted every timestamp, so there is nothing to
//     judge. An unjudgeable entry reaching the inbox is a nuisance; one silently
//     marked 'seen' is a lost video, and 'seen' is terminal.
//   - no baseline: the first pass has not completed, and the baseline branch
//     owns that case anyway.
//
// backCatalogueGrace absorbs skew rather than precision: PublishedAt comes from
// approximate_date, which is derived from relative-time text ("2 weeks ago") and
// so is good to the day only for recent items. Three days is ample at the
// boundary, and it errs toward the inbox — a recoverable nuisance — rather than
// toward a terminal row.
//
// The effective window is three to four days, not exactly three: publishedAt is
// date-only (parsed as midnight UTC) while baselinedAt carries a time of day, so
// a channel baselined at 12:00 admits everything published from cutoff-day+1
// onward and still suppresses the cutoff day itself. That imprecision is
// deliberate — sharpening it would only move a boundary the input is too coarse
// to place anyway — and it stays on the fail-open side of nothing that matters.
const backCatalogueGrace = 3 * 24 * time.Hour

func isBackCatalogue(publishedAt, baselinedAt string) bool {
	if publishedAt == "" || baselinedAt == "" {
		return false
	}
	pub, err := time.Parse("2006-01-02", publishedAt)
	if err != nil {
		return false
	}
	base, err := time.Parse(sqlTimeLayout, baselinedAt)
	if err != nil {
		return false
	}
	return pub.Before(base.Add(-backCatalogueGrace))
}

// isUnfinishedStream reports whether an entry is a stream that has not settled
// into a finished video yet. Its caller records no ledger row for those, so the
// entry stays recoverable on a later pass — see the comment at that branch for
// why that matters.
//
// It is written as an ALLOWLIST of the settled states — an ordinary upload
// ("not_live", or "" when a flat listing omits the field) and a completed
// stream ("was_live") — so that anything else is deferred. That covers the
// known unsettled states ("is_upcoming", "is_live", and "post_live", the window
// where a broadcast has ended but YouTube has not finished cutting the VOD) and
// also any status yt-dlp grows later. Deferring an unfamiliar status costs a
// day; treating it as finished risks downloading half a broadcast.
//
// It overlaps passesFilters below by design. This is the predicate that decides
// whether an entry is recorded AT ALL, and that is a different question from
// whether an entry qualifies for the inbox; keeping the two separate means
// neither can be broken by editing the other.
func isUnfinishedStream(e ytdlp.ChannelEntry) bool {
	switch e.LiveStatus {
	case "", "not_live", "was_live":
		return false
	default:
		return true
	}
}

// passesFilters drops sub-min-duration entries and, redundantly, any unfinished
// stream. Shorts are excluded by construction — they have their own tab, which
// peeq never lists.
//
// The unfinished-stream check cannot be reached today (the caller matches
// isUnfinishedStream first and skips the entry). It is kept anyway, delegating
// to the same helper so the two can never disagree: this is the predicate that
// decides whether an entry belongs in the inbox, and an airing stream does not,
// whatever the calling code around it comes to look like.
//
// A zero duration (yt-dlp omitted it in flat mode) FAILS OPEN — the video is
// kept, since we'd rather offer a maybe-short video than silently drop uploads.
func passesFilters(e ytdlp.ChannelEntry, minDuration int) bool {
	if isUnfinishedStream(e) {
		return false
	}
	if e.DurationSeconds > 0 && e.DurationSeconds < minDuration {
		return false
	}
	return true
}

// enqueueAuto seeds a videos row (carrying the per-channel format override on
// this fresh insert), flips it to 'queued', and enqueues a download job at
// autoPriority (below manual). Flat listings are metadata-poor —
// description/availability are intentionally left sparse here (no per-video -J
// call, to respect the throttle budget); thumbnail_path stays empty (a local
// path), while the remote thumbnail lives on the ledger row.
//
// published_at is left unset on PURPOSE even though the listing now carries an
// approximate date: videos.published_at is the exact upload_date the download's
// own metadata call writes moments later, and seeding it with an approximation
// would downgrade the date Library renders. The approximation stays on the
// ledger row, where it only ever feeds the inbox card.
func (s *Scheduler) enqueueAuto(e ytdlp.ChannelEntry, sub *channels.Subscription) error {
	// Best-effort narrowing of the delete-vs-scan window: if the channel was
	// deleted mid-scan, skip creating a download for it (videos has no FK to
	// channels, so only the later ledger Insert would fail — leaving a stray
	// videos row + job for a just-deleted channel). channels is now a
	// metadata cache: Get() returning a row no longer means "added" — a
	// concurrent maybeResolveChannel can re-create a cache-only row for the
	// very id the user just deleted. So the guard must check added_at, not
	// mere presence, or a scan in flight would enqueue a download for a
	// channel that isn't added anymore. This is not fully atomic across
	// stores; full atomicity is a documented follow-up.
	if c, err := s.d.Channels.Get(sub.ChannelID); err != nil || c == nil || c.AddedAt == "" {
		return nil
	}
	if err := s.d.Videos.Upsert(videos.Video{
		ID: e.ID, URL: e.URL, Title: e.Title, ChannelID: sub.ChannelID,
		DurationSeconds: int64(e.DurationSeconds), RequestedFormat: sub.FormatOverride,
	}); err != nil {
		return err
	}
	if err := s.d.Videos.SetStatus(e.ID, "queued", ""); err != nil {
		return err
	}
	if _, err := s.d.Jobs.Enqueue(e.ID, autoPriority); err != nil {
		return err
	}
	return nil
}

// jitteredInterval returns the base scan interval plus a symmetric random
// jitter in [-scanJitter, +scanJitter], clamped to at least one hour so a
// pathological rand value can never schedule a near-immediate re-scan.
func (s *Scheduler) jitteredInterval() time.Duration {
	return sched.JitteredInterval(scanInterval, scanJitter, time.Hour, s.rand)
}

// NextScanAt returns the instant one ordinary scan interval after now, in the
// SQLite text form next_scan_at is stored in. Exported so a caller that pushes
// a channel's scan out — the "skip this one" action on Up next — reschedules by
// the same cadence the scheduler itself uses, instead of restating 24 hours
// somewhere the constant above would never be checked against.
//
// rand supplies the jitter; pass sched.PseudoRand()'s closure. Every scheduled
// scan is scattered this way, and a skip is no exception: landing skips on an
// exact interval would gather everything skipped in one sitting into a convoy.
func NextScanAt(now time.Time, rand func() float64) string {
	d := sched.JitteredInterval(scanInterval, scanJitter, time.Hour, rand)
	return now.Add(d).UTC().Format(sqlTimeLayout)
}

// sleep waits d unless ctx is cancelled first. It returns false if ctx was
// cancelled (the caller should stop), true if the full wait elapsed.
func (s *Scheduler) sleep(ctx context.Context, d time.Duration) bool {
	return sched.Sleep(ctx, d)
}
