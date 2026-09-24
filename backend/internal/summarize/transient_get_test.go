package summarize

import (
	"context"
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

// loadErrorHarness enqueues a summarizable video whose row cannot be read:
// the title column is renamed, which breaks Videos.Get and nothing else.
func loadErrorHarness(t *testing.T, maxAttempts int) (*workerHarness, *Worker, *fakeActivityRecorder) {
	t.Helper()
	h := newWorkerHarness(t)
	if err := h.videos.Upsert(videos.Video{ID: "v1", URL: "https://youtu.be/v1", Title: "T"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.jobs.Enqueue("v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`UPDATE summary_jobs SET max_attempts = ? WHERE video_id = 'v1'`, maxAttempts); err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.Exec(`ALTER TABLE videos RENAME COLUMN title TO title_x`); err != nil {
		t.Fatal(err)
	}
	rec := &fakeActivityRecorder{}
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Rag: h.rag,
		Summarizer: New(failCompleter{t: t}), Embedder: failEmbedder{t: t},
		EmbedModel: "test-model", EmbedDim: 4, Activity: rec})
	return h, w, rec
}

func jobState(t *testing.T, h *workerHarness) (state string, attempts int) {
	t.Helper()
	if err := h.db.QueryRow(`SELECT state, attempts FROM summary_jobs WHERE video_id = 'v1'`).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	return state, attempts
}

// TestProcessOneTransientVideoLoadErrorRequeues: a database error while
// loading the video is not "the video is gone". It used to be treated as
// missing and the job marked failed for good — and EnqueueMissing skips any
// video that has a job row, so one SQLITE_BUSY lost the summary forever. It
// takes the retry ladder like any other failed step, and once the read works
// again the job completes.
func TestProcessOneTransientVideoLoadErrorRequeues(t *testing.T) {
	h, w, rec := loadErrorHarness(t, 3)

	if _, err := w.processOne(context.Background()); err == nil {
		t.Fatal("processOne must report the failed load")
	}
	if state, attempts := jobState(t, h); state != "pending" || attempts != 1 {
		t.Fatalf("job = (%s, %d), want (pending, 1): a retry, not a permanent failure", state, attempts)
	}
	if n := len(rec.events); n != 0 {
		t.Fatalf("%d activity rows for a retry, want 0", n)
	}

	// The read works again: the job proceeds (to no_transcript, the clean
	// outcome for a video without one) instead of staying lost.
	if _, err := h.db.Exec(`ALTER TABLE videos RENAME COLUMN title_x TO title`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.processOne(context.Background()); err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if state, _ := jobState(t, h); state != "done" {
		t.Fatalf("job state after recovery = %q, want done", state)
	}
	v, err := h.videos.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if v.SummaryStatus == videos.SummaryError {
		t.Fatalf("summary_status = error after a recovered load; want the analysis outcome")
	}
}

// TestProcessOnePersistentVideoLoadErrorFailsVisibly: when the ladder runs
// out the video shows the error and the Activity feed says so — not a
// 'failed' job behind a card that spins forever.
func TestProcessOnePersistentVideoLoadErrorFailsVisibly(t *testing.T) {
	h, w, rec := loadErrorHarness(t, 1)

	if _, err := w.processOne(context.Background()); err == nil {
		t.Fatal("processOne must report the failed load")
	}
	if state, _ := jobState(t, h); state != "failed" {
		t.Fatalf("job state = %q, want failed on the last attempt", state)
	}
	var status string
	if err := h.db.QueryRow(`SELECT summary_status FROM videos WHERE id = 'v1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != videos.SummaryError {
		t.Fatalf("summary_status = %q, want error so the card stops waiting", status)
	}
	if n := len(rec.events); n != 1 {
		t.Fatalf("%d activity rows, want 1 terminal failure", n)
	}
}
