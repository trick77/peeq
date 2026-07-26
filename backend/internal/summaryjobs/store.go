// Package summaryjobs persists the offline summarization+embedding queue
// (summary_jobs). It mirrors internal/jobs but is simpler: no priority, no
// log tail, no cancel — a summary either completes or fails with bounded retries.
package summaryjobs

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
	res, err := s.db.Exec(`INSERT INTO summary_jobs (video_id) VALUES (?)`, videoID)
	if err != nil {
		return 0, fmt.Errorf("summaryjobs: enqueue: %w", err)
	}
	return res.LastInsertId()
}

// ListActive returns the in-flight summary queue — every pending or running
// job — in claim order (oldest enqueued first), so the Queue page can show what
// is being summarized right now. Terminal jobs ('done'/'failed') are omitted:
// the queue is about work still to happen, and a failed job's only recovery is
// the Player's manual Re-summarize, not this list.
func (s *Store) ListActive() ([]Job, error) {
	rows, err := s.db.Query(`
		SELECT id, video_id, state, attempts, max_attempts, last_error
		FROM summary_jobs
		WHERE state IN ('pending','running')
		ORDER BY enqueued_at, id`)
	if err != nil {
		return nil, fmt.Errorf("summaryjobs: list active: %w", err)
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.VideoID, &j.State, &j.Attempts, &j.MaxAttempts, &j.LastError); err != nil {
			return nil, fmt.Errorf("summaryjobs: list active scan: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ClaimNext atomically moves the oldest pending job to running and returns it,
// or (nil, nil) when the queue is empty.
func (s *Store) ClaimNext() (*Job, error) {
	row := s.db.QueryRow(`
		UPDATE summary_jobs SET state='running', started_at=datetime('now'), attempts=attempts+1
		WHERE id = (SELECT id FROM summary_jobs WHERE state='pending' ORDER BY enqueued_at, id LIMIT 1)
		RETURNING id, video_id, state, attempts, max_attempts, last_error`)
	var j Job
	err := row.Scan(&j.ID, &j.VideoID, &j.State, &j.Attempts, &j.MaxAttempts, &j.LastError)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("summaryjobs: claim: %w", err)
	}
	return &j, nil
}

// Finish records a terminal state ('done' or 'failed').
func (s *Store) Finish(id int64, state, lastErr string) error {
	_, err := s.db.Exec(`UPDATE summary_jobs SET state=?, last_error=?, finished_at=datetime('now') WHERE id=?`, state, lastErr, id)
	return err
}

// Fail requeues a job as pending after a retryable error, unless it has
// exhausted max_attempts, in which case it is marked failed. terminal reports
// whether this call moved the job to 'failed' (as opposed to requeuing it), so
// a caller can act only on the genuinely-terminal failure and not on every
// retry. The truth comes from the row's own attempts/max_attempts via
// RETURNING (like ClaimNext), not from the passed attempts, which is advisory.
func (s *Store) Fail(id int64, attempts int, lastErr string) (terminal bool, err error) {
	var state string
	err = s.db.QueryRow(`
		UPDATE summary_jobs
		SET state = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'pending' END,
		    last_error = ?
		WHERE id = ?
		RETURNING state`, lastErr, id).Scan(&state)
	if err != nil {
		return false, err
	}
	return state == StateFailed, nil
}

// EnqueueMissing enqueues a job for every downloaded video that has no
// summary_jobs row at all, and returns how many it added. It closes the gap
// left by a process killed between marking a video downloaded and enqueueing
// its summary — chiefly `peeq import-ta`, whose re-runs skip anything already
// downloaded and would therefore never revisit such a video.
//
// "No row at all" is the deliberate criterion: a video whose job exhausted
// max_attempts keeps its 'failed' row and is NOT retried here, so a poison
// video cannot be re-queued on every boot. Those stay a manual Re-summarize.
func (s *Store) EnqueueMissing() (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO summary_jobs (video_id)
		SELECT v.id FROM videos v
		WHERE v.status = 'downloaded'
		  AND v.summary_status NOT IN ('done', 'no_transcript')
		  AND NOT EXISTS (SELECT 1 FROM summary_jobs j WHERE j.video_id = v.id)`)
	if err != nil {
		return 0, fmt.Errorf("summaryjobs: enqueue missing: %w", err)
	}
	return res.RowsAffected()
}

// ResetOrphans returns jobs left 'running' by a crashed process to 'pending'.
func (s *Store) ResetOrphans() error {
	_, err := s.db.Exec(`UPDATE summary_jobs SET state='pending', started_at=NULL WHERE state='running'`)
	return err
}
