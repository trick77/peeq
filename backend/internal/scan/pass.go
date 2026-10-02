package scan

import (
	"context"
	"fmt"
	"strings"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/store"
)

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
	entries, streamCount, streams, err := s.listChannel(ctx, sub.ChannelID, baseline, s.streamsTabKnownMissing(sub, baseline))
	if err != nil {
		return err
	}
	s.noteStreamsTab(sub, streams)
	// Tally for the Activity record: how many genuinely-new uploads were queued
	// automatically vs left for a manual decision, and (on the first pass) how
	// many the baseline snapshot recorded.
	var queuedCount, pendingCount, baselineCount, backlogCount, unavailableCount int
	// probes is this pass's shared budget for per-video availability re-checks,
	// threaded through rather than held on the Scheduler so it resets per pass
	// and stays visible at the places that spend it: the listing loop below,
	// and recheckParkedOffListing after it.
	var probes int
	// listed records every id this pass's listing returned, so the off-listing
	// re-check can tell what it has already covered.
	listed := make(map[string]bool, len(entries))
	// One batched read of the ledger rows for everything listed, instead of a
	// Get per entry — a pass used to cost a few hundred statements per channel
	// here. The whole row, not just its existence: an 'unavailable' row is the
	// one kind of known video a scan must still act on, so the state has to be
	// in hand. The videos-table check stays a point read, but only on the write
	// path below, which a few entries per pass reach at most: batching it too
	// would snapshot it before a loop that can block on throttled probes.
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if !listed[e.ID] {
			listed[e.ID] = true
			ids = append(ids, e.ID)
		}
	}
	known, err := s.d.Ledger.GetMany(ids)
	if err != nil {
		return err
	}
	handled := make(map[string]bool, len(entries))
	for _, e := range entries {
		// A tab can list the same id twice. The per-entry Get this loop used
		// to do absorbed the repeat (it found the row just inserted); the
		// snapshot cannot, so the repeat is skipped here.
		if handled[e.ID] {
			continue
		}
		handled[e.ID] = true
		if row := known[e.ID]; row != nil {
			// Dedup vs ledger — but heal the row's date first. Rows written
			// before migration 0008 have none, and a known video is never
			// revisited anywhere else, so this is the only chance an item
			// already sitting in the inbox has to gain one. Only a row still
			// without a date is written; Ledger.SetPublishedAt's own guard is
			// the second line of defence.
			if row.PublishedAt == "" && e.PublishedAt != "" {
				if err := s.d.Ledger.SetPublishedAt(e.ID, e.PublishedAt); err != nil {
					return err
				}
			}
			if row.State != channelvideos.StateUnavailable {
				continue
			}
			unavailableCount++
			revived, err := s.recheckUnavailable(ctx, row, e, sub, &probes)
			if err != nil {
				return err
			}
			switch {
			case revived && sub.Autodownload:
				queuedCount++
			case revived:
				pendingCount++
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
			entry.State = channelvideos.StateSeen
			baselineCount++
		case isUnfinishedStream(e):
			// Record NOTHING for a stream that has not finished. 'seen'
			// is terminal — the ledger dedup above (Ledger.GetMany) matches on
			// video_id with no state predicate, and nothing anywhere revisits
			// a seen row — so writing
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
			entry.State = channelvideos.StateSeen
			backlogCount++
		case !passesFilters(e, set.MinVideoDurationSeconds):
			// Reached only for the duration floor now (the live case above is
			// matched first). Terminal 'seen' is right here: a video too short
			// today will not grow longer tomorrow, so re-listing it every pass
			// forever would buy nothing.
			entry.State = channelvideos.StateSeen
		case e.GateReason() != "":
			// The listing itself says peeq cannot fetch this: members-only,
			// premium, or auth-walled. Park it rather than offering it, so a
			// gated upload never reaches the inbox only to fail the moment the
			// user clicks Download.
			//
			// Best-effort by nature — yt-dlp only fills `availability` when the
			// tab card carries a badge, so this branch catches what it can and
			// the download worker's terminal-error path catches the rest. The
			// two write the same state and the same reason, so nothing
			// downstream has to care which one saw it first.
			//
			// ORDER: after the duration floor, so a gated video that is also too
			// short still gets the cheaper terminal 'seen' and is never
			// re-checked; before Autodownload, so a gated upload on an
			// autodownload channel is not queued into a guaranteed failure.
			entry.State = channelvideos.StateUnavailable
			entry.UnavailableReason = e.GateReason()
			unavailableCount++
		case sub.Autodownload:
			entry.State = channelvideos.StateQueued
			queuedCount++
		default:
			entry.State = channelvideos.StatePending
			pendingCount++
		}
		// Autodownload: enqueue FIRST, then record the ledger row LAST. If
		// enqueueAuto fails part-way, the ledger row is never written, so the
		// next scan re-encounters the id and the videos-table dedup (the row
		// enqueueAuto Upserted) catches the half-done id — rather than a
		// premature 'queued' ledger row permanently masking a video that was
		// never actually enqueued.
		if entry.State == channelvideos.StateQueued {
			if err := s.enqueueAuto(e, sub); err != nil {
				return err
			}
		}
		if err := s.d.Ledger.Insert(entry); err != nil {
			return err
		}
		// A newly-pending upload gets its thumbnail cached now, best-effort and
		// off the scan's critical path (a bounded queue drained by one
		// goroutine Run owns, so a slow CDN never stalls the loop and a pass
		// with many uploads never bursts at it), so the inbox card renders from
		// peeq rather than loading i.ytimg.com in the browser. Only 'pending' —
		// seen/queued/unavailable rows never appear in the inbox. The serve
		// endpoint self-heals anything this misses.
		if entry.State == channelvideos.StatePending && s.d.MediaDir != "" {
			s.queueThumbnail(entry.VideoID, entry.ThumbnailURL)
		}
	}

	// Parked videos the listing no longer reaches get whatever probe budget the
	// loop above left. A baseline pass is skipped: it is a channel's FIRST pass,
	// so there is nothing parked from an earlier one, and its job is to snapshot
	// what already exists rather than to revive anything.
	if !baseline {
		offQueued, offPending, offConsidered, err := s.recheckParkedOffListing(ctx, sub, listed, &probes)
		if err != nil {
			return err
		}
		queuedCount += offQueued
		pendingCount += offPending
		// Matches the listing loop's counter: unavailable tallies parked rows
		// this pass looked at, not just the ones that came back.
		unavailableCount += offConsidered
	}

	next := s.nextScanAt(sub.ChannelID)
	lastScanned := s.d.Now().UTC().Format(store.TimeLayout)
	if err := s.d.Channels.MarkScanned(sub.ChannelID, baseline, lastScanned, next, sub.ScanRequestedAt); err != nil {
		return err
	}
	// Clean scan pass: reset the consecutive dead-scan streak (a channel that
	// recovers between unrelated failures must not creep toward
	// auto-unsubscription) and the shared failure streak (Reset() clears the
	// whole shared streak globally, not just for this channel).
	if err := s.d.Channels.ResetDeadScan(sub.ChannelID); err != nil {
		s.d.Logger.Error("scan: reset dead scan failed", "channel_id", sub.ChannelID, "err", err)
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
	// unavailable is broken out for the same reason as backlog: items peeq
	// declines to offer must be readable in the log rather than inferred from
	// an inbox that stayed empty. It counts every parked row this pass touched
	// — newly parked, re-confirmed, or revived — so the number answers "how
	// much of this channel is walled off from me?" rather than only reporting
	// change.
	s.d.Logger.Info("scan complete", "channel_id", sub.ChannelID,
		"listed", len(entries), "streams", streamCount, "new", newCount,
		"backlog", backlogCount, "unavailable", unavailableCount)

	// Activity record. The silence rule applies: a scan that surfaced nothing new
	// (the common case) writes nothing, so the agenda is not a wall of "0 new".
	// The first-run baseline is worth one row — it explains why a freshly
	// subscribed channel queued nothing. A REQUESTED scan is the deliberate
	// exception: the user pressed a button and is owed an answer, so it reports
	// even a nothing-new result rather than leaving them with no evidence the
	// check ran at all.
	switch {
	case baseline:
		activity.Record(s.d.Activity, activity.Event{
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
		activity.Record(s.d.Activity, activity.Event{
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
		activity.Record(s.d.Activity, activity.Event{
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
		activity.Record(s.d.Activity, activity.Event{
			Kind: activity.KindScan, Outcome: activity.OutcomeOK,
			SubjectID: sub.ChannelID, Subject: s.channelName(sub.ChannelID),
			Summary: "checked on request", Detail: "nothing new",
		})
	}
	return nil
}
