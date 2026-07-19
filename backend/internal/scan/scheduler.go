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
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/trick77/vark/internal/channels"
	"github.com/trick77/vark/internal/channelvideos"
	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/videos"
	"github.com/trick77/vark/internal/ytdlp"
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
// listing of a channel's recent uploads. Declaring it here (rather than
// importing the concrete Runner) keeps the scheduler testable with a fake
// that never shells out to yt-dlp; the real *ytdlp.Runner satisfies it.
type ChannelLister interface {
	ChannelVideos(ctx context.Context, ucid string, n int) ([]ytdlp.ChannelEntry, error)
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
	Channels     *channels.Store
	Ledger       *channelvideos.Store
	Videos       *videos.Store
	Jobs         JobEnqueuer
	Settings     *settings.Store
	Lister       ChannelLister
	CookieStatus func(ctx context.Context) string // settings.CookieStatus
	Now          func() time.Time                 // injectable clock (defaults to time.Now)
	PollInterval time.Duration                    // idle re-check (default 30s)
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
	return &Scheduler{d: d, rand: pseudoRand()}
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
		// Cookie gate: no valid cookie → don't scan (don't hammer).
		if s.d.CookieStatus(ctx) != "valid" {
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
	defer func() {
		if r := recover(); r != nil {
			s.d.Logger.Error("scan: recovered from panic", "channel", sub.ChannelID, "panic", r)
			s.backoff(sub.ChannelID)
		}
	}()
	if err := s.scanOnce(ctx, sub); err != nil {
		s.d.Logger.Warn("scan failed; backing off", "channel", sub.ChannelID, "err", err)
		s.backoff(sub.ChannelID)
	}
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
	entries, err := s.d.Lister.ChannelVideos(ctx, sub.ChannelID, s.d.listSize)
	if err != nil {
		return fmt.Errorf("scan: list %s: %w", sub.ChannelID, err)
	}
	baseline := sub.BaselinedAt == ""
	for _, e := range entries {
		exists, err := s.d.Ledger.Exists(e.ID)
		if err != nil {
			return err
		}
		if exists {
			continue // dedup vs ledger
		}
		if v, err := s.d.Videos.Get(e.ID); err != nil {
			return err
		} else if v != nil {
			continue // dedup vs videos (manually added / already downloaded)
		}
		entry := channelvideos.Entry{
			VideoID: e.ID, ChannelID: sub.ChannelID, Title: e.Title,
			DurationSeconds: e.DurationSeconds, URL: e.URL, ThumbnailURL: e.ThumbnailURL,
		}
		switch {
		case baseline:
			entry.State = "seen"
		case !passesFilters(e, set.MinVideoDurationSeconds):
			entry.State = "seen"
		case sub.Autodownload:
			entry.State = "queued"
		default:
			entry.State = "pending"
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
	return s.d.Channels.MarkScanned(sub.ChannelID, baseline, lastScanned, next)
}

// passesFilters drops sub-min-duration and upcoming/live entries. Shorts and
// finished livestreams are already excluded by querying only the /videos tab.
// A zero duration (yt-dlp omitted it in flat mode) FAILS OPEN — the video is
// kept, since we'd rather offer a maybe-short video than silently drop uploads.
func passesFilters(e ytdlp.ChannelEntry, minDuration int) bool {
	if e.LiveStatus == "is_upcoming" || e.LiveStatus == "is_live" {
		return false
	}
	if e.DurationSeconds > 0 && e.DurationSeconds < minDuration {
		return false
	}
	return true
}

// enqueueAuto seeds a videos row (carrying the per-channel format override on
// this fresh insert), flips it to 'queued', and enqueues a download job at
// autoPriority (below manual). Flat listings are metadata-poor — published_at/
// description/availability are intentionally left sparse here (no per-video -J
// call, to respect the throttle budget); thumbnail_path stays empty (a local
// path), while the remote thumbnail lives on the ledger row.
func (s *Scheduler) enqueueAuto(e ytdlp.ChannelEntry, sub *channels.Subscription) error {
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
	d := scanInterval + time.Duration(s.rand()*float64(2*scanJitter)) - scanJitter
	if d < time.Hour {
		d = time.Hour
	}
	return d
}

// sleep waits d unless ctx is cancelled first. It returns false if ctx was
// cancelled (the caller should stop), true if the full wait elapsed.
func (s *Scheduler) sleep(ctx context.Context, d time.Duration) bool {
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

// pseudoRand returns a float64-in-[0,1) source seeded from the wall clock. It
// is a package-private seam so tests can swap in a deterministic source; the
// jitter it feeds needs no cryptographic quality.
func pseudoRand() func() float64 {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	return r.Float64
}
