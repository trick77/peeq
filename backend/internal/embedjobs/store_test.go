package embedjobs

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// seedVideo inserts a video in the state EnqueueStale looks for, so each test
// can vary exactly one field away from eligible.
func seedVideo(t *testing.T, db *sql.DB, id string, cols map[string]any) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO videos (id, url, status, subtitle_path, embed_model, embed_dim, embed_rev)
		VALUES (?, 'u', 'downloaded', 'c/v/x.vtt', 'e5', 1536, 0)`, id); err != nil {
		t.Fatal(err)
	}
	for col, val := range cols {
		if _, err := db.Exec(`UPDATE videos SET `+col+` = ? WHERE id = ?`, val, id); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEnqueueClaimFinish(t *testing.T) {
	db := openDB(t)
	seedVideo(t, db, "v1", nil)
	s := New(db)

	if _, err := s.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimNext()
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.VideoID != "v1" || job.State != StateRunning || job.Attempts != 1 {
		t.Fatalf("unexpected claim: %+v", job)
	}
	// A claimed job is no longer claimable.
	if again, err := s.ClaimNext(); err != nil || again != nil {
		t.Fatalf("second claim = %+v, %v; want nil, nil", again, err)
	}
	if err := s.Finish(job.ID, StateDone, ""); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PendingCount(); err != nil || n != 0 {
		t.Fatalf("pending = %d, %v; want 0", n, err)
	}
}

func TestFailRequeuesUntilAttemptsExhausted(t *testing.T) {
	db := openDB(t)
	seedVideo(t, db, "v1", nil)
	s := New(db)
	if _, err := s.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}

	// max_attempts defaults to 3, so the first two failures requeue.
	for i := 1; i <= 2; i++ {
		job, err := s.ClaimNext()
		if err != nil || job == nil {
			t.Fatalf("attempt %d: claim = %+v, %v", i, job, err)
		}
		terminal, err := s.Fail(job.ID, "endpoint down")
		if err != nil {
			t.Fatal(err)
		}
		if terminal {
			t.Fatalf("attempt %d should not be terminal", i)
		}
	}
	job, err := s.ClaimNext()
	if err != nil || job == nil {
		t.Fatalf("third claim = %+v, %v", job, err)
	}
	terminal, err := s.Fail(job.ID, "endpoint down")
	if err != nil {
		t.Fatal(err)
	}
	if !terminal {
		t.Error("third failure should be terminal")
	}
	if next, _ := s.ClaimNext(); next != nil {
		t.Errorf("a terminally failed job must not be claimable again: %+v", next)
	}
}

func TestResetOrphans(t *testing.T) {
	db := openDB(t)
	seedVideo(t, db, "v1", nil)
	s := New(db)
	if _, err := s.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimNext(); err != nil {
		t.Fatal(err)
	}
	// Simulates a crash mid-job: the row is left 'running' with nothing working it.
	n, err := s.ResetOrphans()
	if err != nil || n != 1 {
		t.Fatalf("ResetOrphans = %d, %v; want 1", n, err)
	}
	job, err := s.ClaimNext()
	if err != nil || job == nil {
		t.Fatalf("orphan should be claimable again: %+v, %v", job, err)
	}
}

func TestEnqueueStaleSelectsOnlyRebuildableVideos(t *testing.T) {
	db := openDB(t)
	s := New(db)

	seedVideo(t, db, "stale", nil)                                   // the one that should be picked
	seedVideo(t, db, "current", map[string]any{"embed_rev": 2})      // already at the recipe
	seedVideo(t, db, "never", map[string]any{"embed_model": ""})     // summarize queue's job
	seedVideo(t, db, "nosubs", map[string]any{"subtitle_path": ""})  // nothing to rebuild from
	seedVideo(t, db, "gone", map[string]any{"status": "tombstoned"}) // media removed

	n, err := s.EnqueueStale(2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("EnqueueStale = %d, want 1", n)
	}
	job, err := s.ClaimNext()
	if err != nil || job == nil || job.VideoID != "stale" {
		t.Fatalf("claimed %+v, want the stale video", job)
	}
}

// A poison video that exhausted its attempts must not be resurrected on every
// restart — the boot sweep runs on each boot, and re-adding it would retry it
// forever while logging a failure each time.
func TestEnqueueStaleDoesNotRequeueAnExistingJob(t *testing.T) {
	db := openDB(t)
	seedVideo(t, db, "v1", nil)
	s := New(db)

	if n, err := s.EnqueueStale(2); err != nil || n != 1 {
		t.Fatalf("first sweep = %d, %v; want 1", n, err)
	}
	job, _ := s.ClaimNext()
	if _, err := s.Fail(job.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE embed_jobs SET state='failed' WHERE id=?`, job.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.EnqueueStale(2); err != nil || n != 0 {
		t.Fatalf("second sweep = %d, %v; want 0 (job already exists)", n, err)
	}
}

// Both workers write the same three chunk tables, so the sweep must not hand a
// video to the re-embed worker while the summarize worker still has it.
func TestEnqueueStaleSkipsVideosWithSummaryJobInFlight(t *testing.T) {
	db := openDB(t)
	seedVideo(t, db, "busy", nil)
	seedVideo(t, db, "free", nil)
	if _, err := db.Exec(`INSERT INTO summary_jobs (video_id, state) VALUES ('busy','running')`); err != nil {
		t.Fatal(err)
	}
	s := New(db)

	if n, err := s.EnqueueStale(2); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v; want 1", n, err)
	}
	job, _ := s.ClaimNext()
	if job == nil || job.VideoID != "free" {
		t.Fatalf("claimed %+v, want the video with no summary job", job)
	}
}

// A finished summary job is not in flight and must not block a rebuild.
func TestEnqueueStaleAllowsVideosWithFinishedSummaryJob(t *testing.T) {
	db := openDB(t)
	seedVideo(t, db, "v1", nil)
	if _, err := db.Exec(`INSERT INTO summary_jobs (video_id, state) VALUES ('v1','done')`); err != nil {
		t.Fatal(err)
	}
	if n, err := New(db).EnqueueStale(2); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v; want 1", n, err)
	}
}
