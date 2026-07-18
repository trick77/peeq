// Package jobs persists the download queue (the download_jobs table from
// migration 0001_init.sql). It is the single source of truth for what the
// download worker should do next: which video to fetch, in what priority
// order, and how many attempts remain. The claim operation is atomic so a
// job can never be handed to two workers (today the worker is
// single-concurrency, but the atomic claim keeps that a property of the
// store rather than an assumption of the caller).
package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotRunning is returned by Finish, Bump, and Fail when their guarded
// UPDATE affects no rows because the target job is no longer in the 'running'
// state — typically because a concurrent Cancel moved it to 'canceled' out
// from under the worker. It is not a failure: it tells the caller the job was
// settled elsewhere and it must not write any further terminal state.
var ErrNotRunning = errors.New("jobs: job not in running state")

// Job mirrors one row of the download_jobs table. StartedAt and FinishedAt
// are empty strings when the underlying column is NULL (job not yet
// started / not yet finished).
type Job struct {
	ID          int64
	VideoID     string
	State       string
	Priority    int
	Attempts    int
	MaxAttempts int
	LastError   string
	LogTail     string
	EnqueuedAt  string
	StartedAt   string
	FinishedAt  string
}

// Store persists the download queue.
type Store struct {
	db *sql.DB
}

// New returns a jobs store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// selectColumns is the shared column list for every row read, in Job field
// order, so scanRow can be reused by ClaimNext and List.
const selectColumns = `id, video_id, state, priority, attempts, max_attempts,
	last_error, log_tail, enqueued_at, started_at, finished_at`

// scanRow scans one download_jobs row (in selectColumns order) into a Job,
// mapping NULL started_at/finished_at to empty strings.
func scanRow(sc interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var startedAt, finishedAt sql.NullString
	if err := sc.Scan(
		&j.ID, &j.VideoID, &j.State, &j.Priority, &j.Attempts, &j.MaxAttempts,
		&j.LastError, &j.LogTail, &j.EnqueuedAt, &startedAt, &finishedAt,
	); err != nil {
		return Job{}, err
	}
	j.StartedAt = startedAt.String
	j.FinishedAt = finishedAt.String
	return j, nil
}

// Enqueue inserts a new pending job for videoID at the given priority
// (higher runs first) and returns its autoincrement id. The referenced
// video row must already exist (foreign_keys is ON).
func (s *Store) Enqueue(videoID string, priority int) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(context.Background(),
		`INSERT INTO download_jobs (video_id, priority) VALUES (?, ?) RETURNING id`,
		videoID, priority,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("enqueue job: %w", err)
	}
	return id, nil
}

// ClaimNext atomically selects the oldest highest-priority pending job,
// flips it to running (stamping started_at), and returns it. Ordering is
// priority DESC, then enqueued_at ASC, then id ASC (the id tiebreak makes
// FIFO deterministic even when enqueued_at collides at second resolution).
// The single UPDATE ... RETURNING statement is the atomic claim: no two
// callers can ever observe the same row as pending. Returns (nil, nil) when
// the queue has no pending jobs.
func (s *Store) ClaimNext() (*Job, error) {
	row := s.db.QueryRowContext(context.Background(), `
UPDATE download_jobs
SET state = 'running', started_at = datetime('now')
WHERE id = (
	SELECT id FROM download_jobs
	WHERE state = 'pending'
	ORDER BY priority DESC, enqueued_at ASC, id ASC
	LIMIT 1
)
RETURNING `+selectColumns)
	j, err := scanRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim next job: %w", err)
	}
	return &j, nil
}

// Finish marks a claimed job terminal (state must be one of done, failed,
// canceled), recording the final error text and log tail and stamping
// finished_at. The WHERE clause is guarded by state = 'running': a row that
// was externally moved to 'canceled' can never be resurrected to done/failed.
// Returns ErrNotRunning (and writes nothing) when the guard matches no row.
func (s *Store) Finish(id int64, state, lastErr, logTail string) error {
	res, err := s.db.ExecContext(context.Background(), `
UPDATE download_jobs
SET state = ?, last_error = ?, log_tail = ?, finished_at = datetime('now')
WHERE id = ? AND state = 'running'`,
		state, lastErr, logTail, id,
	)
	if err != nil {
		return fmt.Errorf("finish job %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finish job %d: rows affected: %w", id, err)
	}
	if n == 0 {
		return ErrNotRunning
	}
	return nil
}

// Fail marks a running job terminally failed, recording the final attempts
// count and error text in the SAME guarded write (state must still be
// 'running'). Doing it in one statement — rather than Bump-to-pending then
// Finish — means there is never an intermediate 'pending' window in which
// another claimer could grab a job that is about to be failed. Returns
// ErrNotRunning (and writes nothing) when the row is no longer running.
func (s *Store) Fail(id int64, attempts int, lastErr string) error {
	res, err := s.db.ExecContext(context.Background(), `
UPDATE download_jobs
SET state = 'failed', attempts = ?, last_error = ?, finished_at = datetime('now')
WHERE id = ? AND state = 'running'`,
		attempts, lastErr, id,
	)
	if err != nil {
		return fmt.Errorf("fail job %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("fail job %d: rows affected: %w", id, err)
	}
	if n == 0 {
		return ErrNotRunning
	}
	return nil
}

// Bump returns a job to pending with its attempts count set to the given
// value (the caller passes job.Attempts+1 for a real retry, or the
// unchanged job.Attempts to requeue without burning an attempt — e.g. when
// the worker pauses on a blocked cookie). started_at is cleared so the job
// looks freshly queued. The WHERE clause is guarded by state = 'running' so a
// job canceled out from under the worker is not resurrected to pending;
// returns ErrNotRunning (and writes nothing) when the guard matches no row.
func (s *Store) Bump(id int64, attempts int, lastErr string) error {
	res, err := s.db.ExecContext(context.Background(), `
UPDATE download_jobs
SET state = 'pending', attempts = ?, last_error = ?, started_at = NULL
WHERE id = ? AND state = 'running'`,
		attempts, lastErr, id,
	)
	if err != nil {
		return fmt.Errorf("bump job %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("bump job %d: rows affected: %w", id, err)
	}
	if n == 0 {
		return ErrNotRunning
	}
	return nil
}

// Cancel marks a job canceled, but only if it is still pending or running
// (a job that already finished, failed, or was canceled is left untouched).
func (s *Store) Cancel(id int64) error {
	_, err := s.db.ExecContext(context.Background(), `
UPDATE download_jobs
SET state = 'canceled', finished_at = datetime('now')
WHERE id = ? AND state IN ('pending', 'running')`,
		id,
	)
	if err != nil {
		return fmt.Errorf("cancel job %d: %w", id, err)
	}
	return nil
}

// ResetOrphans returns every running job to pending. It is called once at
// worker boot: a job left in running can only be a leftover from a previous
// process that crashed or was killed mid-download, and must be reclaimable.
func (s *Store) ResetOrphans() error {
	_, err := s.db.ExecContext(context.Background(), `
UPDATE download_jobs SET state = 'pending', started_at = NULL WHERE state = 'running'`)
	if err != nil {
		return fmt.Errorf("reset orphan jobs: %w", err)
	}
	return nil
}

// List returns all jobs in claim order (priority DESC, enqueued_at ASC, id
// ASC), regardless of state.
func (s *Store) List() ([]Job, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT `+selectColumns+` FROM download_jobs
		 ORDER BY priority DESC, enqueued_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		j, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return out, nil
}
