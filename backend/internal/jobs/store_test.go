package jobs

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/trick77/vark/internal/store"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// insertVideo inserts a minimal video row so a job's video_id foreign key
// (foreign_keys is ON) is satisfied.
func insertVideo(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO videos (id, url) VALUES (?, ?)`, id, "https://youtu.be/"+id)
	if err != nil {
		t.Fatalf("insert video %s: %v", id, err)
	}
}

// TestActiveIDsForVideos asserts only pending/running jobs for the given
// video ids are returned: v1's pending job is included, v2's done job is
// excluded, and an unknown id (v3) contributes nothing.
func TestActiveIDsForVideos(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	insertVideo(t, db, "v1")
	insertVideo(t, db, "v2")

	id1, err := s.Enqueue("v1", 0) // pending
	if err != nil {
		t.Fatalf("enqueue v1: %v", err)
	}
	if _, err := s.Enqueue("v2", 0); err != nil {
		t.Fatalf("enqueue v2: %v", err)
	}
	// Mark v2's job done so the pending|running filter must exclude it.
	if _, err := db.Exec(`UPDATE download_jobs SET state='done' WHERE video_id='v2'`); err != nil {
		t.Fatalf("mark v2 done: %v", err)
	}

	ids, err := s.ActiveIDsForVideos([]string{"v1", "v2", "v3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != id1 {
		t.Fatalf("ids = %v, want [%d]", ids, id1)
	}

	// Empty input returns nil without querying.
	if ids, err := s.ActiveIDsForVideos(nil); err != nil || ids != nil {
		t.Fatalf("ActiveIDsForVideos(nil) = %v err=%v, want nil,nil", ids, err)
	}
}

func TestClaimNext_priorityThenFIFO(t *testing.T) {
	db := openTestDB(t)
	s := New(db)

	insertVideo(t, db, "a")
	insertVideo(t, db, "b")
	insertVideo(t, db, "c")

	// Enqueue in order a(0), b(10), c(0). The priority-10 job (b) must come
	// first; the two priority-0 jobs then come in FIFO (enqueue) order a, c.
	if _, err := s.Enqueue("a", 0); err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	if _, err := s.Enqueue("b", 10); err != nil {
		t.Fatalf("enqueue b: %v", err)
	}
	if _, err := s.Enqueue("c", 0); err != nil {
		t.Fatalf("enqueue c: %v", err)
	}

	want := []string{"b", "a", "c"}
	for i, wantVideo := range want {
		job, err := s.ClaimNext()
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if job == nil {
			t.Fatalf("claim %d: got nil, want video %q", i, wantVideo)
		}
		if job.VideoID != wantVideo {
			t.Fatalf("claim %d: got video %q, want %q", i, job.VideoID, wantVideo)
		}
		if job.State != "running" {
			t.Fatalf("claim %d: state = %q, want running", i, job.State)
		}
		if job.StartedAt == "" {
			t.Fatalf("claim %d: started_at not stamped", i)
		}
	}

	// Queue is now empty: ClaimNext returns (nil, nil), not an error.
	job, err := s.ClaimNext()
	if err != nil {
		t.Fatalf("claim empty: %v", err)
	}
	if job != nil {
		t.Fatalf("claim empty: got job %+v, want nil", job)
	}
}

func TestBump_returnsToPendingWithIncrementedAttempts(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	insertVideo(t, db, "a")

	id, err := s.Enqueue("a", 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, err := s.ClaimNext()
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v (job %v)", err, claimed)
	}

	if err := s.Bump(id, claimed.Attempts+1, "network hiccup"); err != nil {
		t.Fatalf("bump: %v", err)
	}

	// Bumped job is claimable again with attempts incremented.
	reclaimed, err := s.ClaimNext()
	if err != nil || reclaimed == nil {
		t.Fatalf("reclaim: %v (job %v)", err, reclaimed)
	}
	if reclaimed.ID != id {
		t.Fatalf("reclaim id = %d, want %d", reclaimed.ID, id)
	}
	if reclaimed.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", reclaimed.Attempts)
	}
	if reclaimed.LastError != "network hiccup" {
		t.Fatalf("last_error = %q, want %q", reclaimed.LastError, "network hiccup")
	}
}

func TestFinish_marksFailedTerminally(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	insertVideo(t, db, "a")

	id, err := s.Enqueue("a", 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := s.ClaimNext(); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.Finish(id, "failed", "gone for good", "log tail"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// A failed job is not claimable.
	job, err := s.ClaimNext()
	if err != nil {
		t.Fatalf("claim after finish: %v", err)
	}
	if job != nil {
		t.Fatalf("claim after finish: got %+v, want nil (failed is terminal)", job)
	}

	jobs, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("list len = %d, want 1", len(jobs))
	}
	if jobs[0].State != "failed" {
		t.Fatalf("state = %q, want failed", jobs[0].State)
	}
	if jobs[0].FinishedAt == "" {
		t.Fatalf("finished_at not stamped")
	}
}

// A row that was moved to 'canceled' out from under the worker must never be
// resurrected: the state = 'running' guard makes Finish, Bump, and Fail
// no-ops (0 rows), reported as ErrNotRunning, leaving the canceled state and
// attempts count untouched.
func TestGuardedWrites_noOpWhenNotRunning(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	insertVideo(t, db, "a")

	id, err := s.Enqueue("a", 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := s.ClaimNext(); err != nil { // -> running
		t.Fatalf("claim: %v", err)
	}
	if ok, err := s.Cancel(id); err != nil || !ok { // running -> canceled (store path)
		t.Fatalf("cancel: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	if err := s.Finish(id, "done", "", ""); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Finish on canceled row = %v, want ErrNotRunning", err)
	}
	if err := s.Bump(id, 7, "requeue"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Bump on canceled row = %v, want ErrNotRunning", err)
	}
	if err := s.Fail(id, 7, "boom"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("Fail on canceled row = %v, want ErrNotRunning", err)
	}

	jobs, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("list len = %d, want 1", len(jobs))
	}
	if jobs[0].State != "canceled" {
		t.Fatalf("state = %q, want canceled (guarded writes must not resurrect it)", jobs[0].State)
	}
	if jobs[0].Attempts != 0 {
		t.Fatalf("attempts = %d, want 0 (no-op writes must not mutate the row)", jobs[0].Attempts)
	}
}

// Fail records the final attempts count and marks a running job failed in one
// guarded write (the max-attempts path relies on this).
func TestFail_recordsAttemptsAndFails(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	insertVideo(t, db, "a")

	id, err := s.Enqueue("a", 0)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := s.ClaimNext(); err != nil { // -> running
		t.Fatalf("claim: %v", err)
	}
	if err := s.Fail(id, 3, "gave up"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	// Not claimable and recorded terminally with the final attempt count.
	if job, err := s.ClaimNext(); err != nil || job != nil {
		t.Fatalf("claim after fail: job=%v err=%v, want nil,nil", job, err)
	}
	jobs, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if jobs[0].State != "failed" {
		t.Fatalf("state = %q, want failed", jobs[0].State)
	}
	if jobs[0].Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", jobs[0].Attempts)
	}
	if jobs[0].LastError != "gave up" {
		t.Fatalf("last_error = %q, want %q", jobs[0].LastError, "gave up")
	}
	if jobs[0].FinishedAt == "" {
		t.Fatalf("finished_at not stamped")
	}
}

func TestResetOrphans_runningBackToPending(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	insertVideo(t, db, "a")

	if _, err := s.Enqueue("a", 0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claimed, err := s.ClaimNext()
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.ResetOrphans(); err != nil {
		t.Fatalf("reset orphans: %v", err)
	}

	reclaimed, err := s.ClaimNext()
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if reclaimed == nil {
		t.Fatalf("orphan not returned to pending")
	}
	if reclaimed.ID != claimed.ID {
		t.Fatalf("reclaim id = %d, want %d", reclaimed.ID, claimed.ID)
	}
}

func TestCancel_onlyPendingOrRunning(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	insertVideo(t, db, "a")
	insertVideo(t, db, "b")

	// Pending job: cancelable.
	pendingID, err := s.Enqueue("a", 0)
	if err != nil {
		t.Fatalf("enqueue a: %v", err)
	}
	if ok, err := s.Cancel(pendingID); err != nil || !ok {
		t.Fatalf("cancel pending: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	// Already-done job: Cancel is a no-op (state unchanged) and reports false.
	doneID, err := s.Enqueue("b", 0)
	if err != nil {
		t.Fatalf("enqueue b: %v", err)
	}
	if _, err := s.ClaimNext(); err != nil {
		t.Fatalf("claim b: %v", err)
	}
	if err := s.Finish(doneID, "done", "", ""); err != nil {
		t.Fatalf("finish b: %v", err)
	}
	if ok, err := s.Cancel(doneID); err != nil || ok {
		t.Fatalf("cancel done: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	// Unknown job id: Cancel is a no-op and reports false, not an error.
	if ok, err := s.Cancel(999999); err != nil || ok {
		t.Fatalf("cancel unknown: ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	jobs, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	states := map[int64]string{}
	for _, j := range jobs {
		states[j.ID] = j.State
	}
	if states[pendingID] != "canceled" {
		t.Fatalf("pending job state = %q, want canceled", states[pendingID])
	}
	if states[doneID] != "done" {
		t.Fatalf("done job state = %q, want done (Cancel must not touch terminal jobs)", states[doneID])
	}
}
