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
