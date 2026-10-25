package videos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// sqliteTime is datetime('now')'s format, so a stamp written from Go compares
// as text against the column default.
const sqliteTime = "2006-01-02 15:04:05"

// DeferInDepthRetry records that the video's in-depth summary is still owed:
// attempts so far, the error that last stopped it, and when to try again
// (migration 0044). It replaces any row the video already has.
func (s *Store) DeferInDepthRetry(id, lastErr string, attempts int, next time.Time) error {
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO in_depth_retry (video_id, attempts, next_at, last_error)
VALUES (?, ?, ?, ?)
ON CONFLICT(video_id) DO UPDATE SET
	attempts   = excluded.attempts,
	next_at    = excluded.next_at,
	last_error = excluded.last_error`, id, attempts, next.UTC().Format(sqliteTime), lastErr)
	if err != nil {
		return fmt.Errorf("defer video %s in-depth retry: %w", id, err)
	}
	return nil
}

// NextInDepthRetry returns the video whose retry has waited longest past its
// time, with its attempts so far, or "" when none is due.
func (s *Store) NextInDepthRetry(now time.Time) (id string, attempts int, err error) {
	err = s.db.QueryRowContext(context.Background(), `
SELECT video_id, attempts FROM in_depth_retry
WHERE next_at <= ?
ORDER BY next_at, video_id LIMIT 1`, now.UTC().Format(sqliteTime)).Scan(&id, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("next in-depth retry: %w", err)
	}
	return id, attempts, nil
}

// ClearInDepthRetry drops the video's retry: done, or nothing left to do.
func (s *Store) ClearInDepthRetry(id string) error {
	if _, err := s.db.ExecContext(context.Background(),
		`DELETE FROM in_depth_retry WHERE video_id = ?`, id); err != nil {
		return fmt.Errorf("clear video %s in-depth retry: %w", id, err)
	}
	return nil
}
