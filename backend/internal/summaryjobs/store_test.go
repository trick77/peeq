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
