package channels

import (
	"context"
	"fmt"
)

// ScanQuietWindow is how far a metadata refresh keeps away from the same
// channel's video scan, on both sides: a channel scanned within the last
// ScanQuietWindow, or due to be scanned within the next one, is passed over.
//
// Both are yt-dlp calls against the same channel url, and firing them minutes
// apart is the shape of traffic that gets an account throttled — for no gain,
// since the metadata is a week stale either way and waiting an hour costs
// nothing. Expressed as the two SQLite modifier fragments the queries actually
// need, so the window is computed in SQL without concatenating constants at
// query time.
//
// Skipping is all that is needed to resolve it: the channel stays due and is
// claimed on a later pass, once its scan has aged out of the window. Nothing
// is rescheduled and nothing is lost.
const (
	ScanQuietWindow = "30 minutes"
	scanQuietBefore = "-" + ScanQuietWindow
	scanQuietAfter  = "+" + ScanQuietWindow
)

// scanQuietPredicate is the shared half of both claim queries: true when
// channelID's scan is far enough away, given a subscription row that may not
// exist (an unsubscribed channel has no scan to collide with). Written once
// because the two callers must agree — a refresher that dodged scans in one
// query and not the other would be worse than one that never dodged at all.
//
// The lower bound is deliberately "next_scan_at is in the near FUTURE", not
// simply "next_scan_at is near now". A subscription that is failing and
// backing off carries a next_scan_at in the past, and treating that as
// imminent would park its metadata refresh forever, precisely for the
// channels most likely to need re-reading.
//
// Placeholders, in order: now (last-scan bound), now (next-scan lower bound),
// now (next-scan upper bound).
const scanQuietPredicate = `
    (s.channel_id IS NULL
     OR (
          (s.last_scanned_at IS NULL OR s.last_scanned_at <= datetime(?, '` + scanQuietBefore + `'))
      AND (s.next_scan_at <= ? OR s.next_scan_at > datetime(?, '` + scanQuietAfter + `'))
        ))`

// ClaimDueMetadata returns the subscribed channel whose metadata refresh is
// most overdue (next_meta_refresh_at <= now) and whose video scan is not
// within ScanQuietWindow, or (nil, nil) if none qualifies. Like ClaimDue it is
// a plain SELECT rather than an atomic state flip: the refresher runs on a
// single goroutine, so there is no second claimant to race against.
//
// It returns the whole channel row rather than an id, because every caller
// immediately needs it (Resolve wants the cached handle and whether a row
// exists at all) and the query has already matched it — fetching it again by
// id would be a second round trip for a row we are holding.
//
// Only subscribed channels are in this rotation. An added-but-unsubscribed
// channel has no subscriptions row and so no schedule — ClaimUnresolved is
// what covers it, once.
func (s *Store) ClaimDueMetadata(now string) (*Channel, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT `+channelColumns+`
FROM subscriptions s
JOIN channels c ON c.id = s.channel_id
WHERE s.next_meta_refresh_at IS NOT NULL AND s.next_meta_refresh_at <= ?
  AND `+scanQuietPredicate+`
ORDER BY s.next_meta_refresh_at ASC
LIMIT 1`, now, now, now, now)

	c, err := scanChannel(row)
	if err != nil {
		return nil, fmt.Errorf("claim due metadata: %w", err)
	}
	return c, nil
}

// ClaimUnresolved returns the longest-listed channel peeq has NEVER read from
// YouTube (resolved_at IS NULL), or (nil, nil) if there is none. The
// same ScanQuietWindow applies, for the same reason — most of these channels
// are unsubscribed and have no scan to collide with at all, but the ones that
// do are no different from the weekly rotation's.
//
// This is the never-read backlog, not the weekly rotation, and it exists
// because "resolve on first page visit" is not good enough for a channel that
// has no name, avatar, banner or subscriber count at all: a channel the user
// just added, or one of the hundreds a TubeArchivist import created, should
// fill itself in without waiting for someone to open its page.
//
// It is self-limiting without any extra bookkeeping: every resolve attempt
// stamps resolved_at, success or failure, so each channel is claimed here
// exactly once. Afterwards it either joins the weekly rotation (if subscribed)
// or rests. That also means this query never re-reads a FAILED resolve — doing
// so would recreate exactly the retry-forever loop 0001's resolved_at rule
// exists to prevent.
//
// The backlog covers exactly the channels List shows: added ones, plus the
// ones present only because the library holds a downloaded video from them.
// The latter have to be in it — they list under "From downloads" with nothing
// but whatever name the video row carried, so without a resolve they would sit
// there behind a placeholder gradient forever.
//
// Purely cache-only rows stay excluded: they exist for any channel peeq has
// ever glanced at, and reading every one of them from YouTube is a lot of
// requests for channels the user never asked about.
//
// Ordering falls back to first_seen_at for the download-only rows, which have
// no added_at — oldest first either way, so nothing starves.
func (s *Store) ClaimUnresolved(now string) (*Channel, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT `+channelColumns+`
FROM channels c
LEFT JOIN subscriptions s ON s.channel_id = c.id
WHERE (c.added_at IS NOT NULL OR `+hasDownloadsPredicate+`)
  AND c.resolved_at IS NULL
  AND `+scanQuietPredicate+`
ORDER BY COALESCE(c.added_at, c.first_seen_at) ASC
LIMIT 1`, now, now, now)

	c, err := scanChannel(row)
	if err != nil {
		return nil, fmt.Errorf("claim unresolved channel: %w", err)
	}
	return c, nil
}

// MarkResolveAttemptedIfUnset stamps resolved_at only when it is still NULL,
// leaving an existing stamp (and everything else on the row) alone.
//
// It is the backstop for an attempt that ended without recording itself at
// all: a panic in the middle of parsing yt-dlp's output, or a failure to even
// read the channel row. Resolve records its own outcomes, so on every ordinary
// path this is a no-op — it exists for the paths that never reached Resolve's
// recording.
//
// Without it, such an attempt on an UNSUBSCRIBED channel is invisible: the
// channel has no subscriptions row, so MarkMetaRefreshed matches nothing,
// resolved_at stays NULL, and ClaimUnresolved hands the same channel back on
// the very next poll — a yt-dlp call every poll, forever, which is the exact
// unbounded-call shape the whole design exists to prevent.
//
// Conditional in SQL rather than read-then-write, mirroring SetCategoryIfUnset:
// the check and the write are one statement, so a concurrent successful resolve
// cannot be overwritten by this backstop.
func (s *Store) MarkResolveAttemptedIfUnset(channelID, at string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE channels SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL`,
		at, channelID,
	)
	if err != nil {
		return fmt.Errorf("mark resolve attempted if unset %s: %w", channelID, err)
	}
	return nil
}

// MarkMetaRefreshed schedules channelID's next metadata refresh, and is called
// after EVERY attempt — a failed refresh is rescheduled exactly like a
// successful one, so a channel that cannot be read right now is retried next
// week rather than retried immediately, forever.
//
// It touches nothing else. In particular it does not feed the dead-scan
// counter: deciding a channel is gone belongs to the scan scheduler, which
// guards that decision against our own cookie being broken.
//
// A no-op for an unsubscribed channel (no subscriptions row to update), which
// is the correct outcome for a ClaimUnresolved backlog channel that is merely
// added: it has no rotation to be scheduled into.
func (s *Store) MarkMetaRefreshed(channelID, nextAt string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE subscriptions SET next_meta_refresh_at = ? WHERE channel_id = ?`,
		nextAt, channelID,
	)
	if err != nil {
		return fmt.Errorf("mark metadata refreshed %s: %w", channelID, err)
	}
	return nil
}
