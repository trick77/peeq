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
// it added. A video already queued keeps its row. Tombstoned and errored
// videos are left out: nobody opens their page.
func (s *Store) QueueMissingInDepth() (int64, error) {
	res, err := s.db.ExecContext(context.Background(), `
INSERT OR IGNORE INTO in_depth_retry (video_id)
SELECT v.id FROM videos v
WHERE v.summary_status = ? AND v.summary <> '' AND v.watched = 0
  AND v.status IN (?, ?, ?, ?)
  AND EXISTS (SELECT 1 FROM video_transcripts t WHERE t.video_id = v.id)
  AND NOT EXISTS (SELECT 1 FROM video_in_depth d WHERE d.video_id = v.id)`,
		SummaryDone, StatusNew, StatusQueued, StatusDownloading, StatusDownloaded)
	if err != nil {
		return 0, fmt.Errorf("queue missing in-depth summaries: %w", err)
	}
	return res.RowsAffected()
}
