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
	"math"
	"time"

	"github.com/trick77/peeq/internal/store"
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
	// NextAttemptAt is the wall-clock floor before which a requeued job is
	// not claimable (see BumpAfter); empty means claimable now.
	NextAttemptAt string
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
	last_error, log_tail, enqueued_at, started_at, finished_at, next_attempt_at`

// scanRow scans one download_jobs row (in selectColumns order) into a Job,
// mapping NULL started_at/finished_at to empty strings.
func scanRow(sc interface{ Scan(...any) error }) (Job, error) {
	var j Job
	var startedAt, finishedAt, nextAttemptAt sql.NullString
	if err := sc.Scan(
		&j.ID, &j.VideoID, &j.State, &j.Priority, &j.Attempts, &j.MaxAttempts,
		&j.LastError, &j.LogTail, &j.EnqueuedAt, &startedAt, &finishedAt, &nextAttemptAt,
	); err != nil {
		return Job{}, err
	}
	j.StartedAt = startedAt.String
	j.FinishedAt = finishedAt.String
	j.NextAttemptAt = nextAttemptAt.String
	return j, nil
}

// EnqueueSQL is the one statement that creates a download job: (video_id,
// priority) in, the new id out. Exported so videos.Store can run it inside
// the transaction that also flips the video to 'queued' — the two are one
// fact, and a status flip without a job row is a video nothing will ever
// pick up. Enqueue below is the same statement on its own.
const EnqueueSQL = `INSERT INTO download_jobs (video_id, priority) VALUES (?, ?) RETURNING id`

// Enqueue inserts a new pending job for videoID at the given priority
// (higher runs first) and returns its autoincrement id. The referenced
// video row must already exist (foreign_keys is ON).
func (s *Store) Enqueue(videoID string, priority int) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(context.Background(), EnqueueSQL, videoID, priority).Scan(&id)
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
	  AND (next_attempt_at IS NULL OR next_attempt_at <= datetime('now'))
	ORDER BY priority DESC, enqueued_at ASC, id ASC
	LIMIT 1
)
RETURNING `+selectColumns)
	j, err := scanRow(row)
	if errors.Is(err, sql.ErrNoRows) {
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
	return s.FinishIn(context.Background(), s.db, id, state, lastErr, logTail)
}

// Execer is the slice of *sql.DB and *sql.Tx a guarded write needs, so a
// caller can run one inside a transaction it owns.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// FinishIn is Finish against the given executor — a transaction when the
// job's terminal state must land together with the video row it describes
// (see download.Worker.succeed). Same guard, same ErrNotRunning.
func (s *Store) FinishIn(ctx context.Context, x Execer, id int64, state, lastErr, logTail string) error {
	res, err := x.ExecContext(ctx, `
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
	return s.BumpAfter(id, attempts, lastErr, 0)
}

// BumpAfter is Bump with a backoff: the job is requeued but not claimable
// until delay has passed, as a wall-clock stamp on the row (next_attempt_at)
// rather than a sleep in the worker — so the only download goroutine moves on
// to the next job instead of holding the whole queue for the wait, and the
// wait survives a restart. A non-positive delay clears the stamp, which is
// also what a plain Bump writes: a requeue that must not wait (a pause the
// loop's own gate parks) never inherits an earlier retry's stamp. The stamp
// has SQLite's one-second granularity, so a positive delay is rounded UP to
// whole seconds — a caller asking for any wait at all gets at least one.
func (s *Store) BumpAfter(id int64, attempts int, lastErr string, delay time.Duration) error {
	var modifier any // NULL clears the stamp
	if delay > 0 {
		modifier = fmt.Sprintf("+%d seconds", int(math.Ceil(delay.Seconds())))
	}
	res, err := s.db.ExecContext(context.Background(), `
UPDATE download_jobs
SET state = 'pending', attempts = ?, last_error = ?, started_at = NULL,
    next_attempt_at = CASE WHEN ? IS NULL THEN NULL ELSE datetime('now', ?) END
WHERE id = ? AND state = 'running'`,
		attempts, lastErr, modifier, modifier, id,
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
// The returned bool reports whether a row was actually transitioned to
// canceled — false for an unknown job id or one already in a terminal state.
func (s *Store) Cancel(id int64) (bool, error) {
	res, err := s.db.ExecContext(context.Background(), `
UPDATE download_jobs
SET state = 'canceled', finished_at = datetime('now')
WHERE id = ? AND state IN ('pending', 'running')`,
		id,
	)
	if err != nil {
		return false, fmt.Errorf("cancel job %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("cancel job %d: rows affected: %w", id, err)
	}
	return n > 0, nil
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

// ActiveIDsForVideos returns the ids of every still-active (pending or
// running) job for the given video ids. The delete-channel handler uses it to
// find the live jobs it must cancel before removing a channel's videos. An
// empty input returns nil without touching the database.
func (s *Store) ActiveIDsForVideos(videoIDs []string) ([]int64, error) {
	if len(videoIDs) == 0 {
		return nil, nil
	}
	ph := store.Placeholders(len(videoIDs))
	args := make([]any, len(videoIDs))
	for i, v := range videoIDs {
		args[i] = v
	}
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id FROM download_jobs WHERE state IN ('pending','running') AND video_id IN (`+ph+`)`, args...) //nolint:gosec // only fixed SQL structure is interpolated (literal conditions, a ?-placeholder list, or a closed switch); every value is a bound ? parameter
	if err != nil {
		return nil, fmt.Errorf("active job ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListQueue returns the download queue in claim order (priority DESC,
// enqueued_at ASC, id ASC): every pending, running and failed job, plus the
// newest finishedWindow done or canceled ones. Nothing prunes download_jobs
// — a row goes only when its video is deleted — so listing every state was a
// read that grew with every job ever created. The page reads pending,
// running and failed; the finished window is a margin for a client that
// wants to show what just completed, not a history.
func (s *Store) ListQueue(finishedWindow int) ([]Job, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT `+selectColumns+` FROM (
		    SELECT `+selectColumns+` FROM download_jobs
		     WHERE state IN (?, ?, ?)
		    UNION ALL
		    SELECT * FROM (
		        SELECT `+selectColumns+` FROM download_jobs
		         WHERE state IN (?, ?)
		         ORDER BY id DESC
		         LIMIT ?
		    )
		 )
		 ORDER BY priority DESC, enqueued_at ASC, id ASC`,
		StatePending, StateRunning, StateFailed, StateDone, StateCanceled, finishedWindow)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

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
