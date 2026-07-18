package download

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/trick77/vark/internal/jobs"
	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/store"
	"github.com/trick77/vark/internal/videos"
	"github.com/trick77/vark/internal/ytdlp"
)

// fakeRunner is a Runner whose behavior is scripted per call index, so a
// test can make the first Download fail and later ones succeed without ever
// touching yt-dlp.
type fakeRunner struct {
	mu sync.Mutex
	n  int
	fn func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error)
}

func (f *fakeRunner) Download(ctx context.Context, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
	f.mu.Lock()
	call := f.n
	f.n++
	f.mu.Unlock()
	return f.fn(ctx, call, req, onProgress)
}

func (f *fakeRunner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

type harness struct {
	db       *sql.DB
	jobs     *jobs.Store
	videos   *videos.Store
	settings *settings.Store
	runner   *fakeRunner
	worker   *Worker
}

func newHarness(t *testing.T, runner *fakeRunner, tune func(*Deps)) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	h := &harness{
		db:       db,
		jobs:     jobs.New(db),
		videos:   videos.New(db),
		settings: settings.New(db),
		runner:   runner,
	}
	deps := Deps{
		Jobs:         h.jobs,
		Videos:       h.videos,
		Settings:     h.settings,
		Runner:       runner,
		PollInterval: 2 * time.Millisecond,
		Watchdog:     0, // off by default; watchdog test enables it
		Backoff:      func(int) time.Duration { return 0 },
	}
	if tune != nil {
		tune(&deps)
	}
	h.worker = New(deps)
	return h
}

// enqueue seeds a video row and enqueues a job for it.
func (h *harness) enqueue(t *testing.T, videoID string, priority int) int64 {
	t.Helper()
	if err := h.videos.Upsert(videos.Video{ID: videoID, URL: "https://youtu.be/" + videoID}); err != nil {
		t.Fatalf("upsert %s: %v", videoID, err)
	}
	id, err := h.jobs.Enqueue(videoID, priority)
	if err != nil {
		t.Fatalf("enqueue %s: %v", videoID, err)
	}
	return id
}

func (h *harness) jobState(t *testing.T, id int64) jobs.Job {
	t.Helper()
	all, err := h.jobs.List()
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	for _, j := range all {
		if j.ID == id {
			return j
		}
	}
	t.Fatalf("job %d not found", id)
	return jobs.Job{}
}

// waitFor polls cond until it is true or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(1 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func runWorker(t *testing.T, w *Worker) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("worker did not stop after ctx cancel")
		}
	})
	return cancel
}

// --- Scenario A: happy path -------------------------------------------------

func TestWorker_success(t *testing.T) {
	var gotProgress []ytdlp.Progress
	var pmu sync.Mutex

	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			onProgress(ytdlp.Progress{Percent: 50})
			return &ytdlp.Result{
				MediaPath:            "/media/vid/vid.mp4",
				FilesizeBytes:        4242,
				FormatUsed:           "bv*+ba",
				SponsorblockSegments: []ytdlp.Segment{{Category: "sponsor", StartTime: 1, EndTime: 2}},
			}, nil
		},
	}
	h := newHarness(t, runner, func(d *Deps) {
		d.OnProgress = func(_ int64, p ytdlp.Progress) {
			pmu.Lock()
			gotProgress = append(gotProgress, p)
			pmu.Unlock()
		}
	})
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done", func() bool { return h.jobState(t, id).State == "done" })

	v, err := h.videos.Get("vid")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.Status != "downloaded" {
		t.Fatalf("video status = %q, want downloaded", v.Status)
	}
	if v.MediaPath != "/media/vid/vid.mp4" || v.FilesizeBytes != 4242 || v.FormatUsed != "bv*+ba" {
		t.Fatalf("download not persisted: %+v", v)
	}
	if v.SponsorblockSegments == "" || v.SponsorblockSegments == "[]" {
		t.Fatalf("sponsorblock segments not persisted: %q", v.SponsorblockSegments)
	}
	pmu.Lock()
	defer pmu.Unlock()
	if len(gotProgress) == 0 || gotProgress[0].Percent != 50 {
		t.Fatalf("progress not streamed: %+v", gotProgress)
	}
}

// --- Scenario B: block pauses and stops claiming ----------------------------

func TestWorker_blockPausesAndStopsClaiming(t *testing.T) {
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			if call == 0 {
				return nil, ytdlp.ErrBlocked
			}
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, nil)
	// Give the valid cookie so we can observe it flip to 'blocked'.
	const validCookie = "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1789000000\tSID\tabc\n"
	if err := h.settings.SetCookie(context.Background(), validCookie, "valid"); err != nil {
		t.Fatalf("set cookie: %v", err)
	}

	job1 := h.enqueue(t, "one", 0)
	job2 := h.enqueue(t, "two", 0)
	runWorker(t, h.worker)

	// The block pauses the worker.
	waitFor(t, "worker paused", func() bool { return h.worker.Paused() })

	// job1 is back to pending with attempts NOT burned; cookie flipped.
	j1 := h.jobState(t, job1)
	if j1.State != "pending" {
		t.Fatalf("job1 state = %q, want pending", j1.State)
	}
	if j1.Attempts != 0 {
		t.Fatalf("job1 attempts = %d, want 0 (pause must not burn an attempt)", j1.Attempts)
	}
	if got := h.settings.CookieStatus(context.Background()); got != "blocked" {
		t.Fatalf("cookie_status = %q, want blocked", got)
	}

	// Claiming has stopped: job2 stays pending and the runner was called
	// exactly once (only job1's attempt). Give the paused loop a moment to
	// (not) do anything.
	time.Sleep(30 * time.Millisecond)
	if h.jobState(t, job2).State != "pending" {
		t.Fatalf("job2 was claimed while paused")
	}
	if c := runner.calls(); c != 1 {
		t.Fatalf("runner called %d times while paused, want 1", c)
	}

	// Resume: both jobs now complete.
	h.worker.Resume()
	waitFor(t, "job1 done", func() bool { return h.jobState(t, job1).State == "done" })
	waitFor(t, "job2 done", func() bool { return h.jobState(t, job2).State == "done" })
}

// --- Scenario C: terminal error fails immediately, no retry -----------------

func TestWorker_terminalFailsImmediately(t *testing.T) {
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return nil, &ytdlp.TerminalError{Reason: "private"}
		},
	}
	h := newHarness(t, runner, nil)
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job failed", func() bool { return h.jobState(t, id).State == "failed" })

	// No retry: exactly one call, attempts untouched.
	if c := runner.calls(); c != 1 {
		t.Fatalf("runner called %d times, want 1 (terminal must not retry)", c)
	}
	j := h.jobState(t, id)
	if j.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", j.Attempts)
	}
	if j.LastError == "" {
		t.Fatalf("last_error not recorded")
	}
	v, _ := h.videos.Get("vid")
	if v.Status != "error" {
		t.Fatalf("video status = %q, want error", v.Status)
	}
	if v.ErrorMessage == "" {
		t.Fatalf("video error_message not recorded")
	}
}

// --- Scenario D: retryable error retries with backoff then fails ------------

func TestWorker_retryableRetriesThenFails(t *testing.T) {
	var backoffCalls []int
	var bmu sync.Mutex
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return nil, &ytdlp.RetryableError{Reason: "rate limited"}
		},
	}
	h := newHarness(t, runner, func(d *Deps) {
		d.Backoff = func(attempts int) time.Duration {
			bmu.Lock()
			backoffCalls = append(backoffCalls, attempts)
			bmu.Unlock()
			return 0
		}
	})
	id := h.enqueue(t, "vid", 0) // default max_attempts = 3
	runWorker(t, h.worker)

	waitFor(t, "job failed after retries", func() bool { return h.jobState(t, id).State == "failed" })

	// 3 attempts total (max_attempts), so 3 runner calls.
	if c := runner.calls(); c != 3 {
		t.Fatalf("runner called %d times, want 3 (max_attempts)", c)
	}
	j := h.jobState(t, id)
	if j.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", j.Attempts)
	}
	// Backoff computed for the two intermediate retries (attempts 1 and 2),
	// not for the final failing attempt.
	bmu.Lock()
	defer bmu.Unlock()
	if len(backoffCalls) != 2 || backoffCalls[0] != 1 || backoffCalls[1] != 2 {
		t.Fatalf("backoff calls = %v, want [1 2]", backoffCalls)
	}
	v, _ := h.videos.Get("vid")
	if v.Status != "error" {
		t.Fatalf("video status = %q, want error", v.Status)
	}
}

// --- Cancel: kills the running job and marks it canceled --------------------

func TestWorker_cancelRunningJob(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			once.Do(func() { close(started) })
			// Block until the worker cancels this job's context (as a real
			// killed child would), then surface the cancellation.
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	h := newHarness(t, runner, nil)
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	<-started // download is now in-flight
	h.worker.Cancel(id)

	waitFor(t, "job canceled", func() bool { return h.jobState(t, id).State == "canceled" })

	// A canceled job must NOT be reclassified as failed/retried: the video
	// is left out of a terminal error state.
	v, _ := h.videos.Get("vid")
	if v.Status == "error" {
		t.Fatalf("canceled job wrongly marked video as error")
	}
}

// --- Watchdog: a hung download (no progress) is killed and retried ----------

func TestWorker_watchdogKillsHungDownload(t *testing.T) {
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			if call == 0 {
				// Produce no progress and hang until the watchdog cancels the
				// context (killing the child), then surface it.
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return &ytdlp.Result{MediaPath: "/m/vid.mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, func(d *Deps) {
		d.Watchdog = 15 * time.Millisecond
	})
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	// The first (hung) attempt is watchdog-killed and retried; the second
	// succeeds.
	waitFor(t, "job done after watchdog retry", func() bool { return h.jobState(t, id).State == "done" })
	if c := runner.calls(); c < 2 {
		t.Fatalf("runner called %d times, want >= 2 (watchdog retry)", c)
	}
}

// A download that keeps emitting progress faster than the watchdog window
// must NOT be killed: each progress line resets the inactivity timer.
func TestWorker_progressResetsWatchdog(t *testing.T) {
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			// Emit progress every ~4ms for well beyond one 20ms watchdog
			// window; if the reset works the context is never cancelled.
			for i := 0; i < 15; i++ {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(4 * time.Millisecond):
					onProgress(ytdlp.Progress{Percent: float64(i)})
				}
			}
			return &ytdlp.Result{MediaPath: "/m/vid.mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, func(d *Deps) {
		d.Watchdog = 20 * time.Millisecond
	})
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done without watchdog kill", func() bool { return h.jobState(t, id).State == "done" })
	if c := runner.calls(); c != 1 {
		t.Fatalf("runner called %d times, want 1 (steady progress must not trip the watchdog)", c)
	}
}
