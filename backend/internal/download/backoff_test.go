package download

import (
	"context"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/ytdlp"
)

// TestWorker_perJobBackoffDoesNotBlockTheQueue: a retry caused by one job's
// own fault (here: its row refusing the 'downloaded' write) used to hold the
// only worker goroutine asleep for the backoff, so every job behind it waited
// too. The wait is now a stamp on that row; the worker moves on.
func TestWorker_perJobBackoffDoesNotBlockTheQueue(t *testing.T) {
	runner := &fakeRunner{
		fn: func(_ context.Context, _ int, req ytdlp.DownloadReq, _ func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, func(d *Deps) {
		d.Backoff = func(int) time.Duration { return time.Hour }
	})
	first := h.enqueue(t, "stuck", 0)
	second := h.enqueue(t, "fine", 0)
	// Only the first video's row refuses to be marked downloaded.
	if _, err := h.db.Exec(`CREATE TRIGGER block_one BEFORE UPDATE OF media_path ON videos
		WHEN NEW.id = 'stuck' BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	runWorker(t, h.worker)

	waitFor(t, "second job done", func() bool { return h.jobState(t, second).State == "done" })
	j := h.jobState(t, first)
	if j.State != "pending" || j.Attempts != 1 {
		t.Fatalf("stuck job = (%s, %d), want (pending, 1) waiting out its backoff", j.State, j.Attempts)
	}
	if j.NextAttemptAt == "" {
		t.Fatal("stuck job has no next_attempt_at; the wait must live on the row")
	}
}

// TestWorker_hostSideBackoffCoolsTheWholeQueue: a 429 is YouTube's answer,
// not one job's fault — the next job would get the same answer and spend an
// attempt on it. The loop keeps its queue-wide breather for that case.
func TestWorker_hostSideBackoffCoolsTheWholeQueue(t *testing.T) {
	runner := &fakeRunner{
		fn: func(_ context.Context, _ int, _ ytdlp.DownloadReq, _ func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return nil, &ytdlp.RetryableError{Reason: "rate limited"}
		},
	}
	h := newHarness(t, runner, func(d *Deps) {
		d.Backoff = func(int) time.Duration { return time.Hour }
	})
	first := h.enqueue(t, "v1", 0)
	second := h.enqueue(t, "v2", 0)
	runWorker(t, h.worker)

	waitFor(t, "first job requeued", func() bool {
		j := h.jobState(t, first)
		return j.State == "pending" && j.Attempts == 1
	})
	// Give the loop time to (wrongly) claim the second job.
	time.Sleep(50 * time.Millisecond)
	if c := runner.calls(); c != 1 {
		t.Fatalf("runner calls = %d, want 1: the queue must cool down after a 429", c)
	}
	if j := h.jobState(t, second); j.State != "pending" || j.Attempts != 0 {
		t.Fatalf("second job = (%s, %d), want untouched (pending, 0)", j.State, j.Attempts)
	}
}
