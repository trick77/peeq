package jobs

import (
	"database/sql"
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
	if err := s.Cancel(pendingID); err != nil {
		t.Fatalf("cancel pending: %v", err)
	}

	// Already-done job: Cancel is a no-op (state unchanged).
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
	if err := s.Cancel(doneID); err != nil {
		t.Fatalf("cancel done: %v", err)
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
