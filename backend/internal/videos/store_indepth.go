package videos

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/trick77/peeq/internal/store"
)

// SetInDepth stores (or replaces) a video's in-depth summary. It lives in its
// own table (migration 0039) so whole-row reads of videos never carry it.
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

// InDepth returns a video's in-depth summary, or "" when it has none yet: its
// step has not run, or failed and waits in in_depth_retry.
func (s *Store) InDepth(id string) (string, error) { return s.InDepthCtx(context.Background(), id) }

// InDepthCtx is InDepth bounded by ctx.
func (s *Store) InDepthCtx(ctx context.Context, id string) (string, error) {
	var body string
	err := s.db.QueryRowContext(ctx,
		`SELECT body FROM video_in_depth WHERE video_id = ?`, id).Scan(&body)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get video %s in-depth summary: %w", id, err)
	}
	return body, nil
}

// dropInDepthTx removes the video's in-depth text and any retry queued for it:
// both belong to the analysis being wiped, and a surviving retry would hand
// the next analysis its attempt count and backoff.
func dropInDepthTx(ctx context.Context, x store.DBTX, id string) error {
	if _, err := x.ExecContext(ctx, `DELETE FROM video_in_depth WHERE video_id = ?`, id); err != nil {
		return err
	}
	_, err := x.ExecContext(ctx, `DELETE FROM in_depth_retry WHERE video_id = ?`, id)
	return err
}

// SetInDepthIfSummary stores the in-depth text only while the video still has
// the finished summary it was written beside, and reports whether it did. A
// retry's chat call takes minutes; a reprocess in that window wipes the
// summary, and a text from the old analysis stored after it would make the
// new job skip its own in-depth step for good.
func (s *Store) SetInDepthIfSummary(id, body, summary string) (bool, error) {
	res, err := s.db.ExecContext(context.Background(), `
INSERT INTO video_in_depth (video_id, body, updated_at)
SELECT id, ?, datetime('now') FROM videos
WHERE id = ? AND summary = ? AND summary <> '' AND summary_status = ?
ON CONFLICT(video_id) DO UPDATE SET
	body       = excluded.body,
	updated_at = excluded.updated_at`, body, id, summary, SummaryDone)
	if err != nil {
		return false, fmt.Errorf("set video %s in-depth summary: %w", id, err)
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
