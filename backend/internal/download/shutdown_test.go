package download

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/ytdlp"
)

// TestWorker_shutdownDuringPreflightLeavesJobRunning: a process shutdown that
// lands while the metadata preflight is in flight is not this job failing.
// The download path already leaves the job 'running' for ResetOrphans to
// reclaim at boot; the preflight must do the same instead of burning an
// attempt through classify/retry and, on the last attempt, failing the video.
func TestWorker_shutdownDuringPreflightLeavesJobRunning(t *testing.T) {
	entered := make(chan struct{})
	var once sync.Once
	runner := &fakeRunner{
		metaFn: func(ctx context.Context, rawURL string) (*ytdlp.Meta, error) {
			once.Do(func() { close(entered) })
			<-ctx.Done()
			return nil, ctx.Err()
		},
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, nil)
	job := h.enqueue(t, "vid1", 0)
	// A single attempt: with the old behaviour the shutdown would have
	// exhausted it and failed the video outright.
	if _, err := h.db.Exec(`UPDATE download_jobs SET max_attempts = 1 WHERE id = ?`, job); err != nil {
		t.Fatalf("set max_attempts: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.worker.Run(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("preflight never started")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop after ctx cancel")
	}

	j := h.jobState(t, job)
	if j.State != "running" {
		t.Fatalf("job state = %q, want running (left for ResetOrphans)", j.State)
	}
	if j.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0 — a shutdown must not burn one", j.Attempts)
	}
	v, err := h.videos.Get("vid1")
	if err != nil {
		t.Fatal(err)
	}
	if v.Status == "error" {
		t.Fatalf("video status = error (%q); a shutdown is not a failure", v.ErrorMessage)
	}
	if c := runner.calls(); c != 0 {
		t.Fatalf("download called %d times after a shutdown mid-preflight", c)
	}
}
