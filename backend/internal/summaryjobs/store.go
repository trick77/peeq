// Package summaryjobs persists the offline summarization+embedding queue
// (summary_jobs). It mirrors internal/jobs but is simpler: no priority, no
// log tail, no cancel — a summary either completes or fails with bounded retries.
package summaryjobs

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Backoff is the retry ladder, indexed by the number of attempts already made,
// and written to next_attempt_at when a job is requeued.
//
// Without it a requeued job keeps its original enqueued_at, so ClaimNext — which
// orders by enqueued_at — picks the just-failed job straight back up. A chat
// endpoint that fails fast burns all three attempts inside two minutes, which
// turns a transient outage into a permanent failure for every video that was in
// the queue at the time.
//
// The rungs are sized against the thing that actually goes wrong: a chat
// endpoint that is down, overloaded, or crawling. Fifteen minutes covers a
// deploy or a rate-limit window. The second rung is hours rather than minutes
// because the failure this ladder exists for is an endpoint having a bad day,
// and spending the last attempt inside the first hour of one wastes it — the
// job is marked failed for good at that point, and the only way back is someone
// noticing the list and pressing Retry all.
//
// Waiting costs nothing worth saving: ClaimNext skips a deferred job rather than
// blocking on it, so nothing else in the queue is held up, and a video nobody
// has watched yet is not made worse by being summarized four hours later. The
// case this trades against is a genuinely poison video, which now takes ~4h15m
// to reach the failed list instead of ~35m — it was never going to succeed
// either way, and nothing waits on it.
var Backoff = []time.Duration{
	15 * time.Minute,
	4 * time.Hour,
	// The 3rd attempt has no successor — a failure there marks the job failed.
}

// backoffFor returns the wait before the next attempt, given how many attempts
// have already been made. Attempts past the ladder reuse its last rung, so
// raising max_attempts above len(ladder)+1 degrades to a fixed wait rather than
// to no wait at all. An empty ladder means retry immediately, which is what
// tests that exercise retry semantics rather than pacing want.
func backoffFor(ladder []time.Duration, attempts int) time.Duration {
	if len(ladder) == 0 {
		return 0
	}
	if attempts < 1 {
		attempts = 1
	}
	if attempts > len(ladder) {
		return ladder[len(ladder)-1]
	}
	return ladder[attempts-1]
}

type Job struct {
	ID          int64
	VideoID     string
	State       string
	Attempts    int
	MaxAttempts int
	LastError   string
}

type Store struct {
	db *sql.DB
	// failSQL carries this store's retry ladder, rendered once at construction.
	failSQL string
}

// New returns a store using the package Backoff ladder.
func New(db *sql.DB) *Store { return NewWithBackoff(db, Backoff) }

// NewWithBackoff returns a store whose requeues use the given ladder. A nil or
// empty ladder retries immediately — the shape tests want when they drive
// several attempts in a row and are asserting on retry behaviour rather than on
// the pacing that protects a struggling endpoint.
func NewWithBackoff(db *sql.DB, ladder []time.Duration) *Store {
	return &Store{db: db, failSQL: buildFailSQL(ladder)}
}

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
// is being summarized right now. 'done' is omitted because the queue is about
// work still to happen; 'failed' has ListFailed, which is a different question
// asked at a different time.
func (s *Store) ListActive() ([]Job, error) {
	return s.list(`WHERE state IN ('pending','running') ORDER BY enqueued_at, id`, "list active")
}

// ListFailed returns the jobs that exhausted max_attempts, newest first.
//
// These are invisible without it. They are gone from ListActive, EnqueueMissing
// skips them on purpose so a poison video is not retried every boot, and a job
// that failed at the key-points step left summary_status='done' behind — so the
// video reads as complete everywhere in the UI while its chapters and highlights
// are permanently missing. LastError is what distinguishes the cases, and it is
// the only place the failing bound ("stream idle for 1m30s") is recorded.
func (s *Store) ListFailed() ([]Job, error) {
	// By id, not finished_at: Fail marks a row failed without stamping
	// finished_at (only Finish does), so a retry-exhausted job has it NULL while
	// the "video missing" short-circuit has it set. Ordering by that column would
	// float every missing-video row above every real failure forever, whatever
	// their ages. id is monotonic and always present.
	return s.list(`WHERE state='failed' ORDER BY id DESC`, "list failed")
}

// list runs the shared column set against a caller-supplied tail. The tail is a
// literal at every call site — this takes no user input.
func (s *Store) list(tail, what string) ([]Job, error) {
	rows, err := s.db.Query(`
		SELECT id, video_id, state, attempts, max_attempts, last_error
		FROM summary_jobs ` + tail)
	if err != nil {
		return nil, fmt.Errorf("summaryjobs: %s: %w", what, err)
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.VideoID, &j.State, &j.Attempts, &j.MaxAttempts, &j.LastError); err != nil {
			return nil, fmt.Errorf("summaryjobs: %s scan: %w", what, err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// RetryFailed returns every 'failed' job to the queue with a fresh attempt
// budget, and reports how many it moved.
//
// This is the counterpart to EnqueueMissing's deliberate refusal to touch failed
// rows: that one runs on every boot, where reviving a poison video would retry
// it forever, so the revival lives here instead — an explicit command a person
// gives once, after fixing whatever was broken.
//
// attempts=0 rather than a bump, because the ladder is per-incident: a video
// that spent its budget on an endpoint outage should get a full one back now
// that the endpoint is up. This matches what the per-video Reprocess already
// gives, by inserting a brand new row. next_attempt_at is cleared so the retry
// starts now, and last_error with it, so a row that fails again shows why it
// failed this time.
func (s *Store) RetryFailed() (int64, error) {
	res, err := s.db.Exec(`
		UPDATE summary_jobs
		SET state='pending', attempts=0, next_attempt_at=NULL, last_error='', started_at=NULL, finished_at=NULL
		WHERE state='failed'`)
	if err != nil {
		return 0, fmt.Errorf("summaryjobs: retry failed: %w", err)
	}
	return res.RowsAffected()
}

// ClaimNext atomically moves the oldest pending job to running and returns it,
// or (nil, nil) when the queue is empty. A job whose next_attempt_at has not
// arrived is skipped rather than blocking the queue behind it — the ladder
// paces one video's retries, it does not pause the worker.
func (s *Store) ClaimNext() (*Job, error) {
	row := s.db.QueryRow(`
		UPDATE summary_jobs SET state='running', started_at=datetime('now'), attempts=attempts+1
		WHERE id = (
			SELECT id FROM summary_jobs
			WHERE state='pending'
			  AND (next_attempt_at IS NULL OR next_attempt_at <= datetime('now'))
			ORDER BY enqueued_at, id LIMIT 1)
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

// buildFailSQL renders the ladder as a SQL CASE over the row's own attempts
// column, so the wait is chosen from the same value that decides whether the job
// is terminal — one statement, and no read-then-write window in which a claim
// could slip past a next_attempt_at that was still NULL.
//
// Every arm goes through backoffFor, so a ladder has exactly one reading. The
// interpolated numbers come from that slice and never from a caller.
func buildFailSQL(ladder []time.Duration) string {
	// An empty ladder is NULL, not a CASE: SQLite rejects a CASE with no WHEN
	// arms, and NULL is already the value ClaimNext reads as "claimable now".
	next := "NULL"
	if len(ladder) > 0 {
		var b strings.Builder
		b.WriteString("CASE ")
		for i := 1; i <= len(ladder); i++ {
			fmt.Fprintf(&b, "WHEN attempts <= %d THEN datetime('now', '+%d seconds') ", i, int(backoffFor(ladder, i).Seconds()))
		}
		fmt.Fprintf(&b, "ELSE datetime('now', '+%d seconds') END", int(backoffFor(ladder, len(ladder)+1).Seconds()))
		next = b.String()
	}
	return `
	UPDATE summary_jobs
	SET state = CASE WHEN attempts >= max_attempts THEN 'failed' ELSE 'pending' END,
	    last_error = ?,
	    next_attempt_at = CASE WHEN attempts >= max_attempts THEN NULL ELSE ` + next + ` END
	WHERE id = ?
	RETURNING state`
}

// Fail requeues a job as pending after a retryable error, unless it has
// exhausted max_attempts, in which case it is marked failed. terminal reports
// whether this call moved the job to 'failed' (as opposed to requeuing it), so
// a caller can act only on the genuinely-terminal failure and not on every
// retry. The truth comes from the row's own attempts/max_attempts via
// RETURNING (like ClaimNext), not from the passed attempts, which is advisory.
//
// A requeue also sets next_attempt_at from the Backoff ladder, so the job is not
// re-claimed on the very next turn of the worker loop. Without that a fast-
// failing endpoint spends every attempt in about a minute and the outage becomes
// permanent for whatever was in the queue.
func (s *Store) Fail(id int64, attempts int, lastErr string) (terminal bool, err error) {
	var state string
	err = s.db.QueryRow(s.failSQL, lastErr, id).Scan(&state)
	if err != nil {
		return false, err
	}
	return state == StateFailed, nil
}

// EnqueueMissing enqueues a job for every downloaded video that has no
// summary_jobs row at all, and returns how many it added. It closes the gap
// left by a process killed between marking a video downloaded and enqueueing
// its summary.
//
// That window is real and not narrow: download.Worker.succeed marks the video
// downloaded, then stores the transcript, stores the poster, removes the
// caption directory and runs a probe bounded at 5s, and only then enqueues. A
// process killed anywhere in there leaves a downloaded video that nothing else
// would ever revisit — the download queue is finished with it, so this boot
// sweep is the only thing that notices.
//
// (It was written for `peeq import-ta`, whose re-runs skipped anything already
// downloaded. That subcommand is gone; the crash window above is what keeps
// this here.)
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
