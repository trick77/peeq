package videos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/trick77/peeq/internal/summaryjobs"
)

// ErrCaptionsSuperseded means fetched captions are no longer wanted: since
// the fetch was asked for, a download was queued, a transcript arrived or an
// analysis was queued. The caller drops the text; nothing was written.
var ErrCaptionsSuperseded = errors.New("fetched captions superseded")

// StoreFetchedCaptions stores captions fetched for a library video that had
// none and queues the analysis that reads them, in ONE transaction.
//
// The guard is inside it because the fetch waited in the YouTube queue for
// minutes: a re-download queued in that window brings its own transcript and
// runs the pipeline itself, and summary_jobs has no unique active-job index,
// so a check outside the transaction could still add a second analysis. One
// transaction also means no transcript is ever stored without its job.
//
// The reset is Reprocess's, except a hand-picked category survives: the user
// asked for captions, not a new category. audioLang "" keeps the stored one.
//
// ErrNotFound when no such video exists, ErrCaptionsSuperseded when the guard
// refuses.
func (s *Store) StoreFetchedCaptions(id, vtt, audioLang string) (jobID int64, err error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store fetched captions %s: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	var hasTranscript, activeJob bool
	err = tx.QueryRowContext(ctx, `
SELECT v.status,
       EXISTS (SELECT 1 FROM video_transcripts t WHERE t.video_id = v.id),
       EXISTS (SELECT 1 FROM summary_jobs j WHERE j.video_id = v.id AND j.state IN ('pending','running'))
  FROM videos v
 WHERE v.id = ?`, id).Scan(&status, &hasTranscript, &activeJob)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store fetched captions %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return 0, fmt.Errorf("store fetched captions %s: guard: %w", id, err)
	}
	if status == StatusQueued || status == StatusDownloading || hasTranscript || activeJob {
		return 0, fmt.Errorf("store fetched captions %s: %w", id, ErrCaptionsSuperseded)
	}

	if err := setTranscriptTx(ctx, tx, id, TranscriptSourceDownload, vtt); err != nil {
		return 0, err
	}
	if audioLang != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE videos SET audio_language = ? WHERE id = ?`, audioLang, id); err != nil {
			return 0, fmt.Errorf("store fetched captions %s: audio language: %w", id, err)
		}
	}
	if err := resetForReprocessTx(ctx, tx, id, true); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, summaryjobs.EnqueueSQL, id)
	if err != nil {
		return 0, fmt.Errorf("store fetched captions %s: enqueue: %w", id, err)
	}
	if jobID, err = res.LastInsertId(); err != nil {
		return 0, fmt.Errorf("store fetched captions %s: job id: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store fetched captions %s: commit: %w", id, err)
	}
	return jobID, nil
}
