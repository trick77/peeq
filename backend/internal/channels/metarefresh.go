package channels

import (
	"context"
	"database/sql"
	"fmt"
)

// ScanQuietWindow is how far a metadata refresh keeps away from the same
// channel's video scan, on both sides: a channel scanned within the last
// ScanQuietWindow, or due to be scanned within the next one, is passed over.
//
// Both are yt-dlp calls against the same channel url, and firing them minutes
// apart is the shape of traffic that gets an account throttled — for no gain,
// since the metadata is a week stale either way and waiting an hour costs
// nothing. Deliberately expressed as a SQLite modifier fragment so the two
// claim queries can compute the window in SQL rather than in Go.
//
// Skipping is all that is needed to resolve it: the channel stays due and is
// claimed on a later pass, once its scan has aged out of the window. Nothing
// is rescheduled and nothing is lost.
const ScanQuietWindow = "30 minutes"

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
          (s.last_scanned_at IS NULL OR s.last_scanned_at <= datetime(?, '-' || '` + ScanQuietWindow + `'))
      AND (s.next_scan_at <= ? OR s.next_scan_at > datetime(?, '+' || '` + ScanQuietWindow + `'))
        ))`

// ClaimDueMetadata returns the subscribed channel whose metadata refresh is
// most overdue (next_meta_refresh_at <= now) and whose video scan is not
// within ScanQuietWindow, or ("", false, nil) if none qualifies. Like ClaimDue
// it is a plain SELECT rather than an atomic state flip: the refresher runs on
// a single goroutine, so there is no second claimant to race against.
//
// Only subscribed channels are in this rotation. A tracked-but-unsubscribed
// channel has no subscriptions row and so no schedule — ClaimUnresolved is
// what covers it, once.
func (s *Store) ClaimDueMetadata(now string) (string, bool, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT s.channel_id
FROM subscriptions s
WHERE s.next_meta_refresh_at IS NOT NULL AND s.next_meta_refresh_at <= ?
  AND `+scanQuietPredicate+`
ORDER BY s.next_meta_refresh_at ASC
LIMIT 1`, now, now, now, now)

	var channelID string
	switch err := row.Scan(&channelID); {
	case err == sql.ErrNoRows:
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("claim due metadata: %w", err)
	}
	return channelID, true, nil
}

// ClaimUnresolved returns the longest-tracked channel peeq has NEVER read from
// YouTube (resolved_at IS NULL), or ("", false, nil) if there is none. The
// same ScanQuietWindow applies, for the same reason — most of these channels
// are unsubscribed and have no scan to collide with at all, but the ones that
// do are no different from the weekly rotation's.
//
// This is the never-read backlog, not the weekly rotation, and it exists
// because "resolve on first page visit" is not good enough for a channel that
// has no name, avatar, banner or subscriber count at all: a channel the user
// just tracked, or one of the hundreds a TubeArchivist import created, should
// fill itself in without waiting for someone to open its page.
//
// It is self-limiting without any extra bookkeeping: every resolve attempt
// stamps resolved_at, success or failure, so each channel is claimed here
// exactly once. Afterwards it either joins the weekly rotation (if subscribed)
// or rests. That also means this query never re-reads a FAILED resolve — doing
// so would recreate exactly the retry-forever loop 0001's resolved_at rule
// exists to prevent.
//
// Untracked cache-only rows are excluded: they exist for any channel peeq has
// ever glanced at, and reading every one of them from YouTube is a lot of
// requests for channels the user never asked about.
func (s *Store) ClaimUnresolved(now string) (string, bool, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT c.id
FROM channels c
LEFT JOIN subscriptions s ON s.channel_id = c.id
WHERE c.tracked_at IS NOT NULL AND c.resolved_at IS NULL
  AND `+scanQuietPredicate+`
ORDER BY c.tracked_at ASC
LIMIT 1`, now, now, now)

	var channelID string
	switch err := row.Scan(&channelID); {
	case err == sql.ErrNoRows:
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("claim unresolved channel: %w", err)
	}
	return channelID, true, nil
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
// tracked: it has no rotation to be scheduled into.
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
