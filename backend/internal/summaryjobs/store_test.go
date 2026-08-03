package summaryjobs

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/store"
)

func TestEnqueueClaimFinishResetOrphans(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u')`)
	s := New(db)

	if _, err := s.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimNext()
	if err != nil || job == nil || job.VideoID != "v1" || job.State != "running" {
		t.Fatalf("claim: %+v %v", job, err)
	}
	if n, _ := s.ClaimNext(); n != nil {
		t.Fatal("second claim should be nil (job already running)")
	}
	if err := s.Finish(job.ID, "done", ""); err != nil {
		t.Fatal(err)
	}
	// orphan reset: a stuck running job returns to pending.
	db.Exec(`INSERT INTO summary_jobs (video_id, state) VALUES ('v1','running')`)
	if err := s.ResetOrphans(); err != nil {
		t.Fatal(err)
	}
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM summary_jobs WHERE state='pending'`).Scan(&cnt)
	if cnt != 1 {
		t.Fatalf("orphan not reset, pending=%d", cnt)
	}
}

func TestFail_terminalVsRequeue(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	db.Exec(`INSERT INTO videos (id, url) VALUES ('term','u'),('retry','u')`)
	// 'term' has spent its last attempt (attempts >= max_attempts) → Fail marks
	// it 'failed' and reports terminal=true. 'retry' still has attempts left →
	// Fail requeues it to 'pending' and reports terminal=false.
	db.Exec(`INSERT INTO summary_jobs (id, video_id, state, attempts, max_attempts) VALUES
		(1,'term','running',3,3),
		(2,'retry','running',1,3)`)
	s := New(db)

	terminal, err := s.Fail(1, 3, "boom")
	if err != nil {
		t.Fatalf("Fail(term): %v", err)
	}
	if !terminal {
		t.Fatal("Fail on an exhausted job should report terminal=true")
	}
	var state string
	db.QueryRow(`SELECT state FROM summary_jobs WHERE id=1`).Scan(&state)
	if state != "failed" {
		t.Fatalf("exhausted job state=%q, want failed", state)
	}

	terminal, err = s.Fail(2, 1, "boom")
	if err != nil {
		t.Fatalf("Fail(retry): %v", err)
	}
	if terminal {
		t.Fatal("Fail on a job with attempts left should report terminal=false")
	}
	db.QueryRow(`SELECT state FROM summary_jobs WHERE id=2`).Scan(&state)
	if state != "pending" {
		t.Fatalf("retryable job state=%q, want pending", state)
	}
}

func TestListActive(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	db.Exec(`INSERT INTO videos (id, url) VALUES ('a','u'),('b','u'),('c','u'),('d','u')`)
	// pending 'a' and 'c', running 'b', and terminal 'd' (done) — only the
	// first three are in flight and should come back, oldest enqueued first.
	db.Exec(`INSERT INTO summary_jobs (video_id, state, enqueued_at) VALUES
		('a','pending','2026-01-01 00:00:01'),
		('b','running','2026-01-01 00:00:02'),
		('c','pending','2026-01-01 00:00:03'),
		('d','done','2026-01-01 00:00:00')`)
	s := New(db)

	active, err := s.ListActive()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(active))
	for i, j := range active {
		got[i] = j.VideoID
	}
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("ListActive returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ListActive order = %v, want %v", got, want)
		}
	}
}

func TestListActiveEmpty(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	s := New(db)
	active, err := s.ListActive()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("empty queue returned %d jobs", len(active))
	}
}

func TestEnqueueMissing(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	s := New(db)

	// gap        — downloaded, never enqueued: the taimport crash window.
	// enqueued   — already has a pending job.
	// finished   — job done and summary written.
	// silent     — done without a transcript, so no job is wanted.
	// poison     — job exhausted its attempts; must stay manual.
	// notyet     — not downloaded yet; the download worker enqueues it later.
	db.Exec(`INSERT INTO videos (id, url, status, summary_status) VALUES
		('gap','u','downloaded','pending'),
		('enqueued','u','downloaded','pending'),
		('finished','u','downloaded','done'),
		('silent','u','downloaded','no_transcript'),
		('poison','u','downloaded','error'),
		('notyet','u','queued','pending')`)
	db.Exec(`INSERT INTO summary_jobs (video_id, state) VALUES ('enqueued','pending'), ('finished','done'), ('poison','failed')`)

	n, err := s.EnqueueMissing()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("backfilled %d jobs, want 1", n)
	}
	// The one it added is claimable; the others were left exactly as they were.
	var state string
	if err := db.QueryRow(`SELECT state FROM summary_jobs WHERE video_id='gap'`).Scan(&state); err != nil {
		t.Fatalf("gap got no job: %v", err)
	}
	if state != "pending" {
		t.Fatalf("gap job state=%q, want pending", state)
	}
	var poison string
	db.QueryRow(`SELECT state FROM summary_jobs WHERE video_id='poison'`).Scan(&poison)
	if poison != "failed" {
		t.Fatalf("poison job state=%q, want it left failed", poison)
	}

	// Idempotent: the row it just added stops it from adding another.
	if again, err := s.EnqueueMissing(); err != nil || again != 0 {
		t.Fatalf("second backfill added %d jobs (err %v), want 0", again, err)
	}
}

// A requeued job is not claimable again until its next_attempt_at arrives.
// Without this the row keeps its original enqueued_at, so ClaimNext — which
// orders by that column — hands the just-failed job straight back, and a
// fast-failing endpoint spends all three attempts in about a minute.
func TestFailDefersTheNextAttempt(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u')`)
	s := New(db)

	id, _ := s.Enqueue("v1")
	job, _ := s.ClaimNext()
	terminal, err := s.Fail(job.ID, job.Attempts, "endpoint down")
	if err != nil {
		t.Fatal(err)
	}
	if terminal {
		t.Fatal("terminal on the first of three attempts")
	}

	// Requeued, but not yet due.
	var state string
	var next sql.NullString
	db.QueryRow(`SELECT state, next_attempt_at FROM summary_jobs WHERE id=?`, id).Scan(&state, &next)
	if state != StatePending {
		t.Fatalf("state=%q, want pending", state)
	}
	if !next.Valid {
		t.Fatal("next_attempt_at is NULL — the job is immediately claimable again")
	}
	if got, _ := s.ClaimNext(); got != nil {
		t.Fatalf("claimed job %d before its next_attempt_at", got.ID)
	}

	// Once due, it is claimable — and this is attempt 2, not a fresh one.
	db.Exec(`UPDATE summary_jobs SET next_attempt_at = datetime('now','-1 second') WHERE id=?`, id)
	got, err := s.ClaimNext()
	if err != nil || got == nil {
		t.Fatalf("claim after the wait: %v, %v", got, err)
	}
	if got.Attempts != 2 {
		t.Fatalf("attempts=%d, want 2", got.Attempts)
	}
}

// A due job must not be stuck behind a deferred one: the ladder paces one
// video's retries, it does not pause the worker.
func TestClaimSkipsDeferredJobForADueOne(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	db.Exec(`INSERT INTO videos (id, url) VALUES ('old','u'), ('new','u')`)
	s := New(db)

	oldID, _ := s.Enqueue("old")
	s.Enqueue("new")
	// The older job is deferred; the newer one has never been tried.
	db.Exec(`UPDATE summary_jobs SET state='pending', next_attempt_at=datetime('now','+1 hour') WHERE id=?`, oldID)

	got, err := s.ClaimNext()
	if err != nil || got == nil {
		t.Fatalf("claim: %v, %v", got, err)
	}
	if got.VideoID != "new" {
		t.Fatalf("claimed %q, want new — a deferred job must not block the queue", got.VideoID)
	}
}

// The last attempt is terminal, and a terminal row carries no next_attempt_at:
// nothing is waiting for it, RetryFailed is.
func TestFailIsTerminalOnTheLastAttempt(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u')`)
	s := NewWithBackoff(db, nil)

	id, _ := s.Enqueue("v1")
	var terminal bool
	for i := 0; i < 3; i++ {
		job, err := s.ClaimNext()
		if err != nil || job == nil {
			t.Fatalf("claim %d: %v, %v", i+1, job, err)
		}
		terminal, _ = s.Fail(job.ID, job.Attempts, "endpoint down")
	}
	if !terminal {
		t.Fatal("third failure of three was not terminal")
	}
	var state string
	var next sql.NullString
	db.QueryRow(`SELECT state, next_attempt_at FROM summary_jobs WHERE id=?`, id).Scan(&state, &next)
	if state != StateFailed {
		t.Fatalf("state=%q, want failed", state)
	}
	if next.Valid {
		t.Fatalf("next_attempt_at=%q on a failed row, want NULL", next.String)
	}
	if got, _ := s.ClaimNext(); got != nil {
		t.Fatalf("claimed a failed job: %+v", got)
	}
}

// ListFailed is the only surface these jobs have: they are gone from
// ListActive and EnqueueMissing skips them on purpose. RetryFailed is the
// bounded, explicit revival — a full attempt budget back, because the budget
// was spent on an outage rather than on anything about the video.
func TestListFailedAndRetryFailed(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "t.db"))
	defer db.Close()
	store.Migrate(db)
	db.Exec(`INSERT INTO videos (id, url) VALUES ('a','u'), ('b','u'), ('c','u')`)
	s := New(db)

	db.Exec(`INSERT INTO summary_jobs (video_id, state, attempts, last_error) VALUES
		('a','failed',3,'stream idle for 1m30s'),
		('b','pending',0,''),
		('c','done',1,'')`)

	failed, err := s.ListFailed()
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].VideoID != "a" {
		t.Fatalf("ListFailed = %+v, want just a", failed)
	}
	if failed[0].LastError != "stream idle for 1m30s" {
		t.Errorf("last_error = %q — it is the only record of which bound failed", failed[0].LastError)
	}
	if active, _ := s.ListActive(); len(active) != 1 || active[0].VideoID != "b" {
		t.Fatalf("ListActive = %+v, want just b", active)
	}

	n, err := s.RetryFailed()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("RetryFailed moved %d rows, want 1", n)
	}
	// Claimable right now, with the whole budget back.
	job, err := s.ClaimNext()
	if err != nil || job == nil {
		t.Fatalf("claim after retry: %v, %v", job, err)
	}
	if job.VideoID != "a" {
		t.Fatalf("claimed %q, want a (enqueued first)", job.VideoID)
	}
	if job.Attempts != 1 {
		t.Fatalf("attempts=%d on the revived job, want 1 — RetryFailed resets the budget", job.Attempts)
	}
	// 'done' is left alone: RetryFailed revives failures, not finished work.
	var doneState string
	db.QueryRow(`SELECT state FROM summary_jobs WHERE video_id='c'`).Scan(&doneState)
	if doneState != StateDone {
		t.Fatalf("done job state=%q, want it untouched", doneState)
	}
	if again, _ := s.RetryFailed(); again != 0 {
		t.Fatalf("second RetryFailed moved %d rows, want 0", again)
	}
}

// The ladder has one reading, and an attempt past its end reuses the last rung
// rather than falling back to no wait at all.
func TestBackoffFor(t *testing.T) {
	ladder := []time.Duration{time.Minute, time.Hour}
	for _, tc := range []struct {
		attempts int
		want     time.Duration
	}{{0, time.Minute}, {1, time.Minute}, {2, time.Hour}, {3, time.Hour}, {99, time.Hour}} {
		if got := backoffFor(ladder, tc.attempts); got != tc.want {
			t.Errorf("backoffFor(%d) = %v, want %v", tc.attempts, got, tc.want)
		}
	}
	if got := backoffFor(nil, 1); got != 0 {
		t.Errorf("backoffFor(nil) = %v, want 0", got)
	}
}
