package scan

import (
	"time"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/sched"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

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
		return nil //nolint:nilerr // a Get failure is treated as not-added: skip the enqueue rather than risk a stray videos row and job for a channel that may have just been deleted
	}
	if err := s.d.Videos.Upsert(videos.Video{
		ID: e.ID, URL: e.URL, Title: e.Title, ChannelID: sub.ChannelID,
		DurationSeconds: int64(e.DurationSeconds), RequestedFormat: sub.FormatOverride,
	}); err != nil {
		return err
	}
	if err := s.d.Videos.SetStatus(e.ID, videos.StatusQueued, ""); err != nil {
		return err
	}
	if _, err := s.d.Jobs.Enqueue(e.ID, autoPriority); err != nil {
		return err
	}
	return nil
}

// nextScanAt is the scheduler's own slot lookup: it reads the channel's rank
// among current subscriptions and returns the instant its next scan belongs on.
//
// A failed rank query falls back to a plain interval rather than propagating.
// Losing the even spacing for one cycle is a cosmetic problem; failing to
// reschedule at all would leave next_scan_at in the past and the loop would
// re-claim that channel on every pass, hammering YouTube.
func (s *Scheduler) nextScanAt(channelID string) string {
	rank, count, err := s.d.Channels.SubscriptionRank(channelID)
	if err != nil {
		s.d.Logger.Error("scan: subscription rank failed", "channel_id", channelID, "err", err)
		return s.d.Now().Add(scanInterval).UTC().Format(store.TimeLayout)
	}
	return NextScanAt(s.d.Now(), rank, count)
}

// NextScanAt returns the instant the channel ranked rank-of-count should next
// be scanned, in the SQLite text form next_scan_at is stored in.
//
// Each subscription owns one slot in the 24-hour cycle — rank * 24h / count, so
// 44 channels sit 32.7 minutes apart — and every reschedule targets that slot
// rather than "one interval after this scan happened to finish". That
// distinction is the whole fix: a completion-anchored schedule has no way back
// once a cookie expiry or a restart makes the whole fleet due at once and it
// drains back-to-back, because each channel then re-anchors to the burst. A
// slot is a fixed point the fleet returns to on its own, one cycle later.
//
// The half-interval floor is the trap this has to avoid. A channel scanned
// early out of a backlog can be sitting minutes BEFORE its own slot instant,
// and the next occurrence after now would then be a re-scan within the hour.
// Starting the search half a cycle out makes that impossible; the cost is that
// a scan lands anywhere in 12–36h while a channel is moving onto its slot. Once
// there, it is exactly 24h, every day, forever.
//
// Exported so the "skip this one" action on Up next reschedules onto the same
// grid the scheduler uses. A skip anchored on the occurrence being skipped
// therefore lands exactly one cycle later, keeping the channel on its slot no
// matter how often it is skipped.
func NextScanAt(now time.Time, rank, count int) string {
	slot := sched.Slot(rank, count, scanInterval)
	return sched.NextSlotAfter(now.Add(scanInterval/2), scanInterval, slot).Format(store.TimeLayout)
}
