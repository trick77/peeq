package scan

import (
	"context"
	"errors"
	"time"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// unavailableRecheckWindow is how long peeq waits between checks on a parked
// video before spending a yt-dlp call to ask whether the gate has lifted.
//
// A window is needed at all because the cheap signal is unreliable: yt-dlp
// fills a flat entry's `availability` only when the tab card renders a badge,
// so most listings say nothing either way. Silence cannot revive a row (every
// parked video would bounce back into the inbox on the next pass) and cannot
// bury one either (a members-only upload the channel later made public would
// never resurface). The probe below is what breaks the tie; this is how often
// it is allowed to run per video.
const unavailableRecheckWindow = 14 * 24 * time.Hour

// maxUnavailableProbes caps how many parked videos one scan pass may probe.
// Each probe is a real per-video yt-dlp call on the shared pacer, and a channel
// with a large members-only back catalogue could otherwise turn a single pass
// into dozens of sequential requests. Rows not reached this pass keep their
// stamps and are simply first in line next time.
const maxUnavailableProbes = 3

// VideoProber resolves one video's metadata — the reliable source of the
// availability field, unlike a flat channel listing, which carries it only
// when the tab card happens to show a badge. *ytdlp.Runner satisfies it.
//
// Nil disables probing entirely: parked rows then move only on the listing's
// own (rare) positive evidence. Tests that do not care leave it nil.
type VideoProber interface {
	Metadata(ctx context.Context, rawURL string) (*ytdlp.Meta, error)
}

// recheckUnavailable decides what to do with a ledger row already parked in
// StateUnavailable, now that a scan has listed the video again. It reports
// whether the row was revived — returned to the inbox, or queued outright on
// an autodownload channel.
//
// The rule throughout is that a row moves on EVIDENCE, never on the mere
// passage of time. That distinction is the whole design: the video that
// prompted this work reached the inbox precisely BECAUSE its listing carried
// no badge, so a time-triggered re-offer would have put it back in front of
// the user every fortnight forever, each time to fail on click.
//
//   - listing shows a gate → stay parked, and restamp: fresh evidence the gate
//     still stands, so the probe clock legitimately resets. A channel scanned
//     daily therefore never spends a probe on a video it can already see is
//     gated.
//   - listing shows an ungated availability → revive now. Positive evidence,
//     and free.
//   - listing says nothing → probe, at most once per window per video and at
//     most maxUnavailableProbes times per pass. The probe's answer is evidence
//     either way; a probe that produces no answer changes nothing.
func (s *Scheduler) recheckUnavailable(ctx context.Context, row *channelvideos.Entry, e ytdlp.ChannelEntry, sub *channels.Subscription, probes *int) (bool, error) {
	if reason := e.GateReason(); reason != "" {
		return false, s.d.Ledger.SetUnavailable(row.VideoID, reason)
	}
	if e.Availability == "" {
		open, checked := s.probeAvailability(ctx, row, probes)
		if !checked {
			return false, nil
		}
		if !open {
			// Confirmed still gated. SetUnavailable restamps, which is what
			// spaces the next probe a full window out.
			return false, s.d.Ledger.SetUnavailable(row.VideoID, row.UnavailableReason)
		}
	}
	return s.revive(row.VideoID, e, sub)
}

// revive returns a parked video to the inbox (or straight to the queue).
//
// Autodownload is honoured rather than forcing everything through the inbox:
// the channel's setting is what the user asked for, and a video that was only
// ever withheld because of a gate should land where an ungated one from the
// same channel would have.
//
// Order matches the new-video path for the same reason: enqueue first, so a
// half-done revive leaves the row parked (and re-checkable) rather than
// pending-but-never-queued.
//
// A video peeq has ALREADY downloaded is never revived. The ledger row can be
// parked while the videos row is complete: nothing outside the inbox writes
// the ledger, so a video the user added by hand (URL paste, extension) after a
// scan parked it leaves that row at 'unavailable' forever — the likeliest
// reaction, in fact, to the misclassification this re-check exists to undo.
// Reviving it would flip a finished, watched video back to 'queued', blank its
// thumbnail_path through enqueueAuto's Upsert and enqueue a duplicate
// download; without autodownload it would offer a video already in the Library
// as an undecided inbox item.
//
// DownloadedAt is the signal rather than status or media_path, mirroring
// Worker.park's own "never discard a video that has ever finished downloading"
// invariant: a row left behind by park's failed-Discard branch has none and
// must stay revivable. The ledger row is settled at 'queued' (what the inbox's
// own keep writes) rather than left parked, so the probe budget stops being
// spent on a question the Library has already answered.
func (s *Scheduler) revive(videoID string, e ytdlp.ChannelEntry, sub *channels.Subscription) (bool, error) {
	v, err := s.d.Videos.Get(videoID)
	if err != nil {
		return false, err
	}
	if v != nil && v.DownloadedAt != "" {
		return false, s.d.Ledger.SetState(videoID, channelvideos.StateQueued)
	}
	if sub.Autodownload {
		if err := s.enqueueAuto(e, sub); err != nil {
			return false, err
		}
		return true, s.d.Ledger.SetState(videoID, channelvideos.StateQueued)
	}
	return true, s.d.Ledger.SetState(videoID, channelvideos.StatePending)
}

// recheckParkedOffListing probes the channel's parked videos that this pass's
// listing did NOT return, and revives any that answer as reachable. It reports
// how many it revived, split by where they landed.
//
// Without it, recovery from a wrong "unavailable" verdict is tied to recency:
// recheckUnavailable only ever sees ids the listing returned, and the listing
// is capped at defaultListSize. A video parked in error therefore became
// permanently unrecoverable the moment it aged out of that window — and since
// parking also discards the videos row (Worker.park), there was nothing left
// in the Library to re-download either. That matters most exactly when the
// verdict was never true: a stale yt-dlp reports working videos as
// unavailable, and the whole batch it condemns ages out together.
//
// It spends what is left of the SAME per-pass probe budget, after the listing
// loop has had first call on it. That ordering is deliberate — a video the
// channel still lists is the cheaper and more likely revival — and the shared
// budget is what keeps this from turning one scan into an unbounded number of
// per-video yt-dlp calls on a channel with a long parked tail.
func (s *Scheduler) recheckParkedOffListing(
	ctx context.Context,
	sub *channels.Subscription,
	listed map[string]bool,
	probes *int,
) (queued, pending, considered int, err error) {
	// Nothing can be answered without a prober, and an exhausted budget means
	// the listing loop already spent this pass's probes. Both are cheap to
	// check before the query, which would otherwise be run for nothing on every
	// pass of every channel.
	if s.d.Prober == nil || *probes >= maxUnavailableProbes {
		return 0, 0, 0, nil
	}
	rows, err := s.d.Ledger.ListUnavailableForChannel(sub.ChannelID)
	if err != nil {
		return 0, 0, 0, err
	}
	for i := range rows {
		if *probes >= maxUnavailableProbes {
			break
		}
		row := &rows[i]
		if listed[row.VideoID] {
			continue // already handled by the listing loop, with better evidence
		}
		considered++
		open, checked := s.probeAvailability(ctx, row, probes)
		if !checked {
			continue
		}
		if !open {
			// Still walled off. Restamping is what spaces the next probe a full
			// window out, so a long parked tail cannot monopolise the budget.
			if err := s.d.Ledger.SetUnavailable(row.VideoID, row.UnavailableReason); err != nil {
				return queued, pending, considered, err
			}
			continue
		}
		// The listing never returned this video, so the ledger row is the only
		// description of it there is — which is precisely what it was written
		// for. It carries everything enqueueAuto needs.
		revived, err := s.revive(row.VideoID, ytdlp.ChannelEntry{
			ID: row.VideoID, URL: row.URL, Title: row.Title,
			DurationSeconds: row.DurationSeconds,
			ThumbnailURL:    row.ThumbnailURL, PublishedAt: row.PublishedAt,
		}, sub)
		if err != nil {
			return queued, pending, considered, err
		}
		if !revived {
			continue
		}
		s.d.Logger.Info("scan: parked video is reachable again",
			"channel_id", sub.ChannelID, "video_id", row.VideoID)
		if sub.Autodownload {
			queued++
		} else {
			pending++
		}
	}
	return queued, pending, considered, nil
}

// probeAvailability asks yt-dlp directly whether a parked video is reachable
// now. It returns (open, checked): checked is false when no probe was made or
// it produced no usable answer, and the caller must then leave the row exactly
// as it found it.
//
// Every "no answer" case is deliberately indistinguishable to the caller —
// probing disabled, budget spent, window not elapsed, yt-dlp errored — because
// they all mean the same thing: nothing was learned, so nothing may change. In
// particular a transient failure must NOT restamp, or a run of network trouble
// would silently push the next real check out by a fortnight each time.
func (s *Scheduler) probeAvailability(ctx context.Context, row *channelvideos.Entry, probes *int) (open bool, checked bool) {
	// The URL check is not merely defensive. A row with none can never be
	// answered, and without this it would fail its probe on every pass without
	// restamping — eating a slot from the per-pass budget forever and starving
	// the rows that could actually be answered.
	if s.d.Prober == nil || row.URL == "" || *probes >= maxUnavailableProbes || !s.recheckDue(row.UnavailableAt) {
		return false, false
	}
	*probes++
	meta, err := s.d.Prober.Metadata(ctx, row.URL)
	if err != nil {
		// A terminal error IS an answer — the video is still walled off — and
		// it is the same answer as "gated", so it needs no separate branch.
		// Anything else (network, bot-block, kill-switch) is not an answer at
		// all, and treating it as one is exactly the silent-burial failure mode
		// this state exists to prevent.
		var terminal *ytdlp.TerminalError
		if errors.As(err, &terminal) {
			return false, true
		}
		s.d.Logger.Warn("scan: availability probe failed", "video_id", row.VideoID, "err", err)
		return false, false
	}
	return videos.NormalizeAvailability(meta.Availability) == videos.AvailabilityAvailable, true
}

// recheckDue reports whether unavailableRecheckWindow has elapsed since the row
// was last confirmed unavailable — i.e. whether a probe is allowed yet.
//
// Note the two clocks: unavailable_at is written by SQLite's datetime('now')
// while the comparison uses the injectable Deps.Now. In production both are
// real UTC and agree; they only diverge under a frozen test clock, which is why
// the window test backdates the stamp rather than advancing Now.
//
// A row with no stamp is treated as due: erring toward one extra probe is much
// cheaper than erring toward silent burial.
func (s *Scheduler) recheckDue(unavailableAt string) bool {
	if unavailableAt == "" {
		return true
	}
	parked, err := store.ParseTime(unavailableAt)
	if err != nil {
		return true
	}
	return s.d.Now().UTC().Sub(parked) >= unavailableRecheckWindow
}
