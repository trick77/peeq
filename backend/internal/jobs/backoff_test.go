package jobs

import (
	"database/sql"
	"testing"
	"time"
)

// TestBumpAfter_defersTheNextClaim: a retry recorded with a delay is back in
// 'pending' but not claimable until its next_attempt_at passes, so the worker
// can move on to other jobs instead of sleeping the backoff itself.
func TestBumpAfter_defersTheNextClaim(t *testing.T) {
	db := openTestDB(t)
	insertVideo(t, db, "v1")
	s := New(db)
	id, err := s.Enqueue("v1", 0)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimNext()
	if err != nil || job == nil {
		t.Fatalf("claim: %v, %v", job, err)
	}
	if err := s.BumpAfter(job.ID, job.Attempts+1, "rate limited", time.Hour); err != nil {
		t.Fatal(err)
	}

	var state string
	var next sql.NullString
	if err := db.QueryRow(`SELECT state, next_attempt_at FROM download_jobs WHERE id = ?`, id).Scan(&state, &next); err != nil {
		t.Fatal(err)
	}
	if state != StatePending {
		t.Fatalf("state = %q, want pending", state)
	}
	if !next.Valid {
		t.Fatal("next_attempt_at is NULL — the job is immediately claimable again")
	}
	if got, _ := s.ClaimNext(); got != nil {
		t.Fatalf("claimed job %d before its next_attempt_at", got.ID)
	}
	if _, err := db.Exec(`UPDATE download_jobs SET next_attempt_at = datetime('now', '-1 second') WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimNext()
	if err != nil || got == nil || got.ID != id {
		t.Fatalf("after the stamp passed: claimed %+v, %v; want job %d", got, err, id)
	}
	if got.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", got.Attempts)
	}
}

// TestBump_clearsAnEarlierDeferral: a plain Bump (a pause the loop's own gate
// parks) must not inherit a stale next_attempt_at from a previous retry.
func TestBump_clearsAnEarlierDeferral(t *testing.T) {
	db := openTestDB(t)
	insertVideo(t, db, "v1")
	s := New(db)
	id, _ := s.Enqueue("v1", 0)
	job, _ := s.ClaimNext()
	if err := s.BumpAfter(job.ID, 1, "rate limited", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE download_jobs SET state = 'running' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Bump(job.ID, 1, "paused"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ClaimNext(); got == nil || got.ID != id {
		t.Fatalf("a plain Bump must leave the job claimable now, got %+v", got)
	}
}

// TestClaimNext_skipsDeferredJobForADueOne: a deferred job does not block the
// queue; the next claimable job behind it runs.
func TestClaimNext_skipsDeferredJobForADueOne(t *testing.T) {
	db := openTestDB(t)
	insertVideo(t, db, "v1")
	insertVideo(t, db, "v2")
	s := New(db)
	first, _ := s.Enqueue("v1", 0)
	second, _ := s.Enqueue("v2", 0)
	job, _ := s.ClaimNext()
	if job.ID != first {
		t.Fatalf("claimed %d first, want %d", job.ID, first)
	}
	if err := s.BumpAfter(job.ID, 1, "rate limited", time.Hour); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimNext()
	if err != nil || got == nil || got.ID != second {
		t.Fatalf("claimed %+v, %v; want the second job %d", got, err, second)
	}
}
