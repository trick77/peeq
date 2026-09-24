package videos

import (
	"errors"
	"testing"
)

func countJobs(t *testing.T, s *Store, id string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM download_jobs WHERE video_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	return n
}

func videoStatus(t *testing.T, s *Store, id string) string {
	t.Helper()
	v, err := s.Get(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	if v == nil {
		return ""
	}
	return v.Status
}

func TestEnqueueDownload_marksQueuedAndInsertsOneJob(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	jobID, err := s.EnqueueDownload("v1", 5)
	if err != nil {
		t.Fatalf("EnqueueDownload: %v", err)
	}
	if jobID == 0 {
		t.Fatal("job id should be returned")
	}
	if got := videoStatus(t, s, "v1"); got != StatusQueued {
		t.Fatalf("status = %q, want queued", got)
	}
	if n := countJobs(t, s, "v1"); n != 1 {
		t.Fatalf("jobs = %d, want 1", n)
	}
	var prio int
	if err := s.db.QueryRow(`SELECT priority FROM download_jobs WHERE id = ?`, jobID).Scan(&prio); err != nil || prio != 5 {
		t.Fatalf("priority = %d err=%v, want 5", prio, err)
	}
}

// TestEnqueueDownload_jobInsertFails_statusUnchanged is the reason the
// operation exists: the status flip and the job row are one fact, and a
// failed insert must not leave a 'queued' video that no worker will ever
// pick up.
func TestEnqueueDownload_jobInsertFails_statusUnchanged(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER block_job BEFORE INSERT ON download_jobs BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if _, err := s.EnqueueDownload("v1", 0); err == nil {
		t.Fatal("expected the enqueue to fail")
	}
	if got := videoStatus(t, s, "v1"); got != StatusNew {
		t.Fatalf("status = %q, want new (rolled back)", got)
	}
}

func TestEnqueueDownload_unknownID(t *testing.T) {
	s := newTestStore(t)
	_, err := s.EnqueueDownload("nope", 0)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if n := countJobs(t, s, "nope"); n != 0 {
		t.Fatalf("jobs = %d, want 0", n)
	}
}

func TestUpsertAndEnqueueDownload_rollsBackTheUpsert(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`CREATE TRIGGER block_job BEFORE INSERT ON download_jobs BEGIN SELECT RAISE(ABORT, 'forced failure'); END;`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if _, err := s.UpsertAndEnqueueDownload(Video{ID: "v1", URL: "u"}, 0); err == nil {
		t.Fatal("expected the enqueue to fail")
	}
	if got := videoStatus(t, s, "v1"); got != "" {
		t.Fatalf("video row should not exist after rollback, status = %q", got)
	}
}

func TestUpsertAndEnqueueDownload_keepsExistingMetadata(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Video{ID: "v1", URL: "u", Title: "Known", ChannelName: "Chan", Description: "desc"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := s.UpsertAndEnqueueDownload(Video{ID: "v1", URL: "u", Title: "Known"}, 0); err != nil {
		t.Fatalf("UpsertAndEnqueueDownload: %v", err)
	}
	v, _ := s.Get("v1")
	if v.Status != StatusQueued || v.ChannelName != "Chan" || v.Description != "desc" {
		t.Fatalf("row = %+v, want queued with metadata kept", v)
	}
}

// TestEnqueueDownload_movesPendingLedgerRowToQueued closes the gap the
// add-by-URL and extension paths left: a video the scan had put on the
// Inbox stayed 'pending' there forever after it was queued another way, so
// the Inbox kept offering it and approving it enqueued a second job.
func TestEnqueueDownload_movesPendingLedgerRowToQueued(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedLedger := func(id, state string) {
		t.Helper()
		if _, err := s.db.Exec(`INSERT INTO channels (id, name) VALUES ('UC1', 'C') ON CONFLICT DO NOTHING`); err != nil {
			t.Fatalf("seed channel: %v", err)
		}
		if _, err := s.db.Exec(`INSERT INTO channel_videos (video_id, channel_id, title, url, state) VALUES (?, 'UC1', 'T', 'u', ?)`, id, state); err != nil {
			t.Fatalf("seed ledger %s: %v", id, err)
		}
	}
	seedLedger("v1", "pending")
	if _, err := s.EnqueueDownload("v1", 0); err != nil {
		t.Fatalf("EnqueueDownload: %v", err)
	}
	var state string
	if err := s.db.QueryRow(`SELECT state FROM channel_videos WHERE video_id = 'v1'`).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("ledger state = %q err=%v, want queued", state, err)
	}

	// An ignored row is a user decision and stays as it is.
	if err := s.Upsert(Video{ID: "v2", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	seedLedger("v2", "ignored")
	if _, err := s.EnqueueDownload("v2", 0); err != nil {
		t.Fatalf("EnqueueDownload: %v", err)
	}
	if err := s.db.QueryRow(`SELECT state FROM channel_videos WHERE video_id = 'v2'`).Scan(&state); err != nil || state != "ignored" {
		t.Fatalf("ledger state = %q err=%v, want ignored", state, err)
	}
}
