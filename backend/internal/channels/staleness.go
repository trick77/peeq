package channels

import (
	"context"
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
SELECT c.id, c.handle, c.name, c.avatar_path, c.added_at, au.reason, au.at
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
		if err := rows.Scan(&a.ID, &a.Handle, &a.Name, &a.AvatarPath, &a.AddedAt, &a.Reason, &a.At); err != nil {
			return nil, fmt.Errorf("scan auto unsubscribed: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate auto unsubscribed: %w", err)
	}
	return out, nil
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
