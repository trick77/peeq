package channels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// DeadScanThreshold is how many CONSECUTIVE scans must report the channel
// deleted before peeq unsubscribes itself. Deliberately > 1: yt-dlp derives
// the reason from substring-matching stderr, which is too thin a thread to
// hang an automatic action on.
const DeadScanThreshold = 3

// ReasonDeleted is the ONLY reason peeq auto-unsubscribes. members/age/geo
// describe restricted content on a living channel; private can revert.
const ReasonDeleted = "deleted"

// AutoUnsubscribed is a channel joined with the record of why (and when)
// peeq unsubscribed it on its own.
type AutoUnsubscribed struct {
	Channel
	Reason string
	At     string
}

// RecordDeadScan increments channelID's consecutive dead-scan count and
// returns the NEW count. Callers compare the result against
// DeadScanThreshold to decide whether to act.
func (s *Store) RecordDeadScan(channelID string) (int, error) {
	_, err := s.db.ExecContext(context.Background(), `
UPDATE subscriptions SET dead_scan_count = dead_scan_count + 1 WHERE channel_id = ?`,
		channelID,
	)
	if err != nil {
		return 0, fmt.Errorf("record dead scan %s: %w", channelID, err)
	}
	var count int
	row := s.db.QueryRowContext(context.Background(),
		`SELECT dead_scan_count FROM subscriptions WHERE channel_id = ?`, channelID)
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("record dead scan %s: read back count: %w", channelID, err)
	}
	return count, nil
}

// ResetDeadScan zeroes channelID's consecutive dead-scan count. Called on
// ANY clean scan, so only a sustained run of dead scans ever reaches
// DeadScanThreshold — a channel that recovers between unrelated failures
// cannot creep toward auto-unsubscription.
func (s *Store) ResetDeadScan(channelID string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE subscriptions SET dead_scan_count = 0 WHERE channel_id = ?`, channelID,
	)
	if err != nil {
		return fmt.Errorf("reset dead scan %s: %w", channelID, err)
	}
	return nil
}

// AutoUnsubscribe unsubscribes channelID and records why in one transaction.
// Both halves must land together: a crash between them would leave a
// channel silently unsubscribed with no visible record — precisely the
// invisible-automation failure this feature exists to avoid. The channel
// row, its channel_videos ledger, and its downloaded videos are all left
// untouched; unsubscribing must never delete media.
func (s *Store) AutoUnsubscribe(channelID, reason, at string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("auto unsubscribe %s: %w", channelID, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM subscriptions WHERE channel_id = ?`, channelID); err != nil {
		return fmt.Errorf("auto unsubscribe %s: delete subscription: %w", channelID, err)
	}
	if _, err := tx.Exec(`
INSERT INTO auto_unsubscribes (channel_id, reason, at) VALUES (?, ?, ?)
ON CONFLICT(channel_id) DO UPDATE SET reason = excluded.reason, at = excluded.at`,
		channelID, reason, at,
	); err != nil {
		return fmt.Errorf("auto unsubscribe %s: record reason: %w", channelID, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("auto unsubscribe %s: %w", channelID, err)
	}
	return nil
}

// AutoUnsubscribedList returns every channel peeq has auto-unsubscribed,
// joined with the reason and timestamp, ordered by most recent first.
func (s *Store) AutoUnsubscribedList() ([]AutoUnsubscribed, error) {
	rows, err := s.db.QueryContext(context.Background(), `
SELECT c.id, c.handle, c.name, c.first_seen_at, au.reason, au.at
FROM auto_unsubscribes au
JOIN channels c ON c.id = au.channel_id
ORDER BY au.at DESC, c.id`)
	if err != nil {
		return nil, fmt.Errorf("list auto unsubscribed: %w", err)
	}
	defer rows.Close()

	var out []AutoUnsubscribed
	for rows.Next() {
		var a AutoUnsubscribed
		if err := rows.Scan(&a.ID, &a.Handle, &a.Name, &a.FirstSeenAt, &a.Reason, &a.At); err != nil {
			return nil, fmt.Errorf("scan auto unsubscribed: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate auto unsubscribed: %w", err)
	}
	return out, nil
}

// AutoUnsubscribeFor returns channelID's auto-unsubscribe record, or
// (nil, nil) if peeq never unsubscribed this channel on its own. The channel
// page uses it to say "Gone from YouTube": an auto-unsubscribe carrying
// ReasonDeleted is peeq's most confident statement that a channel no longer
// exists, since recording one takes DeadScanThreshold consecutive dead scans.
// The embedded Channel is left zero — the caller already holds the channel
// row and needs only the reason and the date.
func (s *Store) AutoUnsubscribeFor(channelID string) (*AutoUnsubscribed, error) {
	row := s.db.QueryRowContext(context.Background(),
		`SELECT reason, at FROM auto_unsubscribes WHERE channel_id = ?`, channelID)
	var a AutoUnsubscribed
	if err := row.Scan(&a.Reason, &a.At); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("auto unsubscribe for %s: %w", channelID, err)
	}
	a.ID = channelID
	return &a, nil
}

// ClearAutoUnsubscribe removes channelID's auto-unsubscribe record, clearing
// the way for it to be subscribed again.
func (s *Store) ClearAutoUnsubscribe(channelID string) error {
	_, err := s.db.ExecContext(context.Background(),
		`DELETE FROM auto_unsubscribes WHERE channel_id = ?`, channelID,
	)
	if err != nil {
		return fmt.Errorf("clear auto unsubscribe %s: %w", channelID, err)
	}
	return nil
}

// DormantAfter is the SQLite modifier for how long a subscribed channel may
// go without a newly discovered video before it is flagged for review.
// Deliberately loose (6 months): channel_videos.discovered_at is when peeq
// first SAW a video, not when YouTube published it, so it is only a proxy
// for the channel's real posting cadence. That's why dormancy only ever
// raises a flag for a human — see DormantChannels — and never acts on its
// own the way ReasonDeleted does.
const DormantAfter = "-6 months"

// Dormant is a subscribed channel that has gone quiet for longer than
// DormantAfter, surfaced for a human to review and decide on.
type Dormant struct {
	ChannelID   string
	Name        string
	LastVideoAt string
}

// DormantChannels returns every subscribed channel whose most recently
// discovered video is older than DormantAfter relative to now, excluding
// any channel whose dormancy was dismissed and has not seen a newer
// discovery since. now is a bound parameter, never datetime('now') inlined
// into the query, so tests can control the clock deterministically.
func (s *Store) DormantChannels(now string) ([]Dormant, error) {
	rows, err := s.db.QueryContext(context.Background(), `
SELECT s.channel_id, c.name, MAX(cv.discovered_at) AS last_seen
FROM subscriptions s
JOIN channels c ON c.id = s.channel_id
LEFT JOIN channel_videos cv ON cv.channel_id = s.channel_id
GROUP BY s.channel_id
HAVING last_seen IS NOT NULL                       -- never flag on absent data: a
                                                     -- LEFT JOIN + this NULL check is
                                                     -- how a brand-new subscription
                                                     -- with zero scans stays unflagged;
                                                     -- swapping in an INNER JOIN would
                                                     -- silently drop those channels
                                                     -- from the result instead of
                                                     -- excluding them by intent.
   AND last_seen < datetime(?, ?)
   AND (s.dormant_dismissed_at IS NULL OR last_seen > s.dormant_dismissed_at)
ORDER BY last_seen`,
		now, DormantAfter,
	)
	if err != nil {
		return nil, fmt.Errorf("dormant channels: %w", err)
	}
	defer rows.Close()

	var out []Dormant
	for rows.Next() {
		var d Dormant
		if err := rows.Scan(&d.ChannelID, &d.Name, &d.LastVideoAt); err != nil {
			return nil, fmt.Errorf("scan dormant channel: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dormant channels: %w", err)
	}
	return out, nil
}

// DismissDormant records that the user reviewed channelID's dormancy flag
// at `at` and chose to suppress it. The flag re-arms automatically the next
// time channel_videos gets a discovery newer than `at` and the channel then
// goes quiet again — dismissal is a snooze, not a permanent silence.
//
// Returns whether a subscription row actually existed for channelID. Without
// this signal the UPDATE affecting zero rows (an unknown or
// added-but-unsubscribed channel id) would look identical to a successful
// dismissal — the HTTP handler needs to tell the two apart to return 404
// instead of a misleading 200 (Task 2 review finding).
func (s *Store) DismissDormant(channelID, at string) (bool, error) {
	res, err := s.db.ExecContext(context.Background(),
		`UPDATE subscriptions SET dormant_dismissed_at = ? WHERE channel_id = ?`,
		at, channelID,
	)
	if err != nil {
		return false, fmt.Errorf("dismiss dormant %s: %w", channelID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("dismiss dormant %s: rows affected: %w", channelID, err)
	}
	return n > 0, nil
}
