package summaryjobs

import (
	"path/filepath"
	"testing"

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
