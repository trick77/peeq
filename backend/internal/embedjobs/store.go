// Package embedjobs persists the re-embed queue (embed_jobs): videos whose
// stored chunks predate the current content recipe and need rebuilding.
//
// It mirrors internal/summaryjobs exactly in shape, but differs in cost: a
// re-embed makes no chat calls at all. Everything it needs is already stored —
// the summary and chapters are columns on videos, the transcript re-parses from
// the subtitle file on disk — so only the embeddings endpoint is touched, and a
// retry is nearly free.
//
// This queue is temporary by design. Once every video reaches the current
// recipe it has no further work; issue #240 tracks removing it.
package embedjobs

import (
	"database/sql"
	"fmt"
)

type Job struct {
	ID          int64
	VideoID     string
	State       string
	Attempts    int
	MaxAttempts int
	LastError   string
}

type Store struct{ db *sql.DB }

func New(db *sql.DB) *Store { return &Store{db: db} }

// Enqueue inserts a pending job for videoID and returns its id.
func (s *Store) Enqueue(videoID string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO embed_jobs (video_id) VALUES (?)`, videoID)
	if err != nil {
		return 0, fmt.Errorf("embedjobs: enqueue: %w", err)
	}
	return res.LastInsertId()
}

// EnqueueStale queues every video whose index predates rev, returning how many
// were added. This is the backfill: called once at boot, it is what actually
// re-indexes the existing library after a recipe change.
//
// The exclusions are each load-bearing:
//
//   - an empty embed_model means the video was never indexed at all. That is the
//     summarize queue's job, not this one — claiming it here would race the
//     summarize worker for the same chunks.
//   - a video already in embed_jobs is not re-added, so a poison row that
//     exhausted its attempts is not resurrected on every restart.
//   - a video with a pending or running summary job is skipped, since that
//     worker is about to write the same three tables.
//   - status and subtitle_path guard against work that cannot succeed: a
//     re-embed re-parses the subtitle file, so a tombstoned or subtitle-less
//     video has nothing to rebuild from.
func (s *Store) EnqueueStale(rev int) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO embed_jobs (video_id)
		SELECT v.id FROM videos v
		WHERE v.status = 'downloaded'
		  AND v.subtitle_path <> ''
		  AND v.embed_model <> ''
		  AND v.embed_rev < ?
		  AND NOT EXISTS (SELECT 1 FROM embed_jobs j WHERE j.video_id = v.id)
		  AND NOT EXISTS (
		        SELECT 1 FROM summary_jobs j
		        WHERE j.video_id = v.id AND j.state IN ('pending','running'))`, rev)
	if err != nil {
		return 0, fmt.Errorf("embedjobs: enqueue stale: %w", err)
	}
	return res.RowsAffected()
}

// PendingCount reports how many jobs are still queued or running, so a caller
// can log the size of a backfill (and, eventually, tell whether the drain that
// issue #240 waits on has finished).
func (s *Store) PendingCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM embed_jobs WHERE state IN ('pending','running')`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("embedjobs: pending count: %w", err)
	}
	return n, nil
}

// ClaimNext atomically moves the oldest pending job to running and returns it,
// or (nil, nil) when the queue is empty.
func (s *Store) ClaimNext() (*Job, error) {
	row := s.db.QueryRow(`
		UPDATE embed_jobs SET state='running', started_at=datetime('now'), attempts=attempts+1
		WHERE id = (SELECT id FROM embed_jobs WHERE state='pending' ORDER BY enqueued_at, id LIMIT 1)
		RETURNING id, video_id, state, attempts, max_attempts, last_error`)
	var j Job
	err := row.Scan(&j.ID, &j.VideoID, &j.State, &j.Attempts, &j.MaxAttempts, &j.LastError)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("embedjobs: claim: %w", err)
	}
	return &j, nil
}

// Finish records a terminal state ('done' or 'failed').
func (s *Store) Finish(id int64, state, lastErr string) error {
	_, err := s.db.Exec(
		`UPDATE embed_jobs SET state=?, last_error=?, finished_at=datetime('now') WHERE id=?`,
		state, lastErr, id)
	if err != nil {
		return fmt.Errorf("embedjobs: finish: %w", err)
	}
	return nil
}

// Fail requeues the job for another attempt, or marks it terminally failed once
// its attempts reach max_attempts. Reports whether the failure was terminal.
//
// The decision reads attempts back from the row rather than trusting the
// caller's copy, which was taken at claim time and may be stale.
func (s *Store) Fail(id int64, lastErr string) (terminal bool, err error) {
	row := s.db.QueryRow(`
		UPDATE embed_jobs
		SET state = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'pending' END,
		    last_error = ?,
		    finished_at = CASE WHEN attempts >= max_attempts THEN datetime('now') ELSE NULL END
		WHERE id = ?
		RETURNING state`, lastErr, id)
	var state string
	if err := row.Scan(&state); err != nil {
		return false, fmt.Errorf("embedjobs: fail: %w", err)
	}
	return state == StateFailed, nil
}

// ResetOrphans returns jobs left 'running' by a crash to 'pending' so they are
// claimed again. Called at boot, before the worker starts.
func (s *Store) ResetOrphans() (int64, error) {
	res, err := s.db.Exec(`UPDATE embed_jobs SET state='pending', started_at=NULL WHERE state='running'`)
	if err != nil {
		return 0, fmt.Errorf("embedjobs: reset orphans: %w", err)
	}
	return res.RowsAffected()
}
