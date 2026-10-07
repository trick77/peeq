package videos

import (
	"context"
	"database/sql"
	"fmt"
)

// SetInDepth stores (or replaces) a video's in-depth summary. It lives in its
// own table (migration 0035) so whole-row reads of videos never carry it.
func (s *Store) SetInDepth(id, body string) error {
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO video_in_depth (video_id, body, updated_at)
VALUES (?, ?, datetime('now'))
ON CONFLICT(video_id) DO UPDATE SET
	body       = excluded.body,
	updated_at = excluded.updated_at`, id, body)
	if err != nil {
		return fmt.Errorf("set video %s in-depth summary: %w", id, err)
	}
	return nil
}

// InDepth returns a video's in-depth summary, or "" when it has none: a video
// analysed before the step existed, or one whose step failed for good.
func (s *Store) InDepth(id string) (string, error) {
	var body string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT body FROM video_in_depth WHERE video_id = ?`, id).Scan(&body)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get video %s in-depth summary: %w", id, err)
	}
	return body, nil
}

const deleteInDepthSQL = `DELETE FROM video_in_depth WHERE video_id = ?`
