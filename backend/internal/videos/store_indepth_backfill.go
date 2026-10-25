package videos

import (
	"context"
	"fmt"
)

// BACKFILL(in-depth): one-off fill of videos analysed before 0039. Remove
// (this file, store_indepth_backfill_test.go, summarize/backfill_indepth*.go and
// the call in summarize.Worker.Run) once in_depth_retry has drained to 0.

// QueueMissingInDepth queues a retry for every unwatched (or partly watched)
// video with a finished summary and no in-depth summary, and returns how many
// it added. A video already queued keeps its row. Left out: tombstoned and
// errored videos, and inbox reads the user ignored (kept only because they are
// indexed): nobody opens their page.
//
// Read first, then one short insert per video: the read walks every videos row
// on a cold disk, and doing it inside the INSERT would hold the write lock for
// all of it, past other writers' busy_timeout.
func (s *Store) QueueMissingInDepth() (int64, error) {
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `
SELECT v.id FROM videos v
WHERE v.summary_status = ? AND v.summary <> '' AND v.watched = 0
  AND v.status IN (?, ?, ?, ?)
  AND EXISTS (SELECT 1 FROM video_transcripts t WHERE t.video_id = v.id)
  AND NOT EXISTS (SELECT 1 FROM video_in_depth d WHERE d.video_id = v.id)
  AND NOT EXISTS (SELECT 1 FROM in_depth_retry r WHERE r.video_id = v.id)
  AND NOT EXISTS (SELECT 1 FROM channel_videos cv WHERE cv.video_id = v.id AND cv.state = 'ignored')`,
		SummaryDone, StatusNew, StatusQueued, StatusDownloading, StatusDownloaded)
	if err != nil {
		return 0, fmt.Errorf("find missing in-depth summaries: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("find missing in-depth summaries: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("find missing in-depth summaries: %w", err)
	}
	var n int64
	for _, id := range ids {
		res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO in_depth_retry (video_id) VALUES (?)`, id)
		if err != nil {
			return n, fmt.Errorf("queue missing in-depth summary %s: %w", id, err)
		}
		added, _ := res.RowsAffected()
		n += added
	}
	return n, nil
}
