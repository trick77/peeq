package download

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
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
		Watchdog:     -1, // disabled here; the watchdog tests set their own via tune
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

// waitForVideoStatus blocks until the video row reaches the given status. The
// worker writes the job's terminal state (done/failed/canceled) BEFORE the
// matching video status (succeed: Finish then SetDownloaded; fail: Fail then
// SetStatus; settleCanceled: Cancel then SetStatus). Waiting on the job state
// alone therefore races the still-running worker goroutine's follow-up video
// write, so a test that then reads the video can observe the transient
// "downloading". Synchronizing on the video's settled status closes that
// window without reordering the worker (whose guarded-first job write is
// deliberate for cancel-safety).
func waitForVideoStatus(t *testing.T, h *harness, videoID, status string) {
	t.Helper()
	waitFor(t, "video status "+status, func() bool {
		v, err := h.videos.Get(videoID)
		return err == nil && v.Status == status
	})
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
	waitForVideoStatus(t, h, "vid", "downloaded")

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

func TestWorker_prefersRequestedFormat(t *testing.T) {
	// Seed a video with a per-channel override; the fake Runner records the
	// DownloadReq it receives.
	var gotReq ytdlp.DownloadReq
	var rmu sync.Mutex

	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			rmu.Lock()
			gotReq = req
			rmu.Unlock()
			return &ytdlp.Result{MediaPath: "/media/v1/v1.mp4"}, nil
		},
	}
	h := newHarness(t, runner, nil)
	if err := h.videos.Upsert(videos.Video{
		ID:              "v1",
		URL:             "https://www.youtube.com/watch?v=v1",
		RequestedFormat: "bestvideo+bestaudio",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := h.jobs.Enqueue("v1", 0); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	runWorker(t, h.worker)

	waitForVideoStatus(t, h, "v1", "downloaded")

	rmu.Lock()
	req := gotReq
	rmu.Unlock()
	if req.Format != "custom" || req.CustomFormat != "bestvideo+bestaudio" {
		t.Fatalf("req = {Format:%q Custom:%q}, want custom/bestvideo+bestaudio", req.Format, req.CustomFormat)
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

// TestWorker_resumeAfterCookieRepasteUnwedgesQueue is the integration proof
// for finding 1: a download that hits a blocked cookie pauses the worker (the
// queue stalls, nothing else is claimed), and calling Resume() — which the
// cookie PUT handler now does after a valid re-paste — un-wedges the queue so
// the worker claims and processes the next job. Simulates the cookie PUT by
// calling Resume() directly, per the task.
func TestWorker_resumeAfterCookieRepasteUnwedgesQueue(t *testing.T) {
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			if call == 0 {
				// First attempt is blocked (expired/absent cookie); the worker
				// pauses and requeues without burning an attempt.
				return nil, ytdlp.ErrBlocked
			}
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, nil)
	const validCookie = "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1789000000\tSID\tabc\n"
	if err := h.settings.SetCookie(context.Background(), validCookie, "valid"); err != nil {
		t.Fatalf("set cookie: %v", err)
	}

	job1 := h.enqueue(t, "one", 0)
	job2 := h.enqueue(t, "two", 0)
	runWorker(t, h.worker)

	// The block stalls the queue: worker paused, neither job progresses.
	waitFor(t, "worker paused", func() bool { return h.worker.Paused() })
	time.Sleep(30 * time.Millisecond)
	if h.jobState(t, job2).State != "pending" {
		t.Fatal("job2 was claimed while the queue was wedged on a blocked cookie")
	}

	// Simulate the valid cookie re-paste: the handler calls Resume().
	h.worker.Resume()

	// The queue un-wedges: both jobs are now claimed and processed.
	waitFor(t, "job1 done after resume", func() bool { return h.jobState(t, job1).State == "done" })
	waitFor(t, "job2 done after resume", func() bool { return h.jobState(t, job2).State == "done" })
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
	waitForVideoStatus(t, h, "vid", "error")

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
	waitForVideoStatus(t, h, "vid", "error")

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
	if ok := h.worker.Cancel(id); !ok {
		t.Fatalf("Cancel(running job) = false, want true")
	}

	waitFor(t, "job canceled", func() bool { return h.jobState(t, id).State == "canceled" })
	// settleCanceled marks the job canceled BEFORE resetting the video to
	// "new"; wait for that follow-up write before reading the video.
	waitForVideoStatus(t, h, "vid", "new")

	// A canceled job must NOT be reclassified as failed/retried: the video
	// is left out of a terminal error state.
	v, _ := h.videos.Get("vid")
	if v.Status == "error" {
		t.Fatalf("canceled job wrongly marked video as error")
	}
}

// TestWorker_cancelUnknownJob_returnsFalse asserts Cancel reports false (not
// true) for a job id that is neither the currently-running job nor a
// pending/running row in the store — e.g. one that was never enqueued, or
// already finished — so callers (the HTTP handler) can tell an unknown or
// already-settled job apart from a real cancel.
func TestWorker_cancelUnknownJob_returnsFalse(t *testing.T) {
	h := newHarness(t, &fakeRunner{fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
		return &ytdlp.Result{MediaPath: "/x.mp4", FormatUsed: "f"}, nil
	}}, nil)

	if ok := h.worker.Cancel(999999); ok {
		t.Fatalf("Cancel(unknown job) = true, want false")
	}
}

// --- Cancel during the EARLY window: canceled, never overwritten to done ----

// A Cancel issued while the worker is still in preflight (metadata/settings
// reads, before Download starts) must end the job 'canceled' and must NOT be
// overwritten to 'done' — and the download must never run. The onClaim seam
// fires the Cancel deterministically inside that early window.
func TestWorker_cancelDuringEarlyWindowNotOverwritten(t *testing.T) {
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			// Must never be reached: the cancel lands in preflight, before the
			// download starts. If it runs, it would (wrongly) report success.
			return &ytdlp.Result{MediaPath: "/should/not/happen.mp4", FormatUsed: "f"}, nil
		},
	}
	var w *Worker
	var once sync.Once
	h := newHarness(t, runner, func(d *Deps) {
		d.onClaim = func(jobID int64) {
			once.Do(func() { w.Cancel(jobID) })
		}
	})
	w = h.worker
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job canceled", func() bool { return h.jobState(t, id).State == "canceled" })

	// Give the loop time to (wrongly) overwrite the canceled row if the fix
	// regressed; it must stay canceled.
	time.Sleep(30 * time.Millisecond)
	if st := h.jobState(t, id).State; st != "canceled" {
		t.Fatalf("job state = %q, want canceled (early cancel must not be overwritten)", st)
	}

	// The download must never have started: a canceled-in-preflight job does
	// not complete a successful write.
	if c := runner.calls(); c != 0 {
		t.Fatalf("runner called %d times, want 0 (cancel in preflight must abort before download)", c)
	}
	v, err := h.videos.Get("vid")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.Status == "downloaded" || v.Status == "error" {
		t.Fatalf("canceled-in-preflight video status = %q, want neither downloaded nor error", v.Status)
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

// --- Disk-space guard (Task 12) --------------------------------------------

// TestWorker_lowDiskPausesClaimingAndResumesWhenFreed drives the worker's
// disk-space precheck: while FreeBytes reports below settings.min_free_gb,
// the worker must never claim the pending job (it stays 'pending', the
// runner is never called) and LowDisk() must report true. Once FreeBytes
// reports enough free space again, the worker must resume claiming and
// finish the job, and LowDisk() must clear.
func TestWorker_lowDiskPausesClaimingAndResumesWhenFreed(t *testing.T) {
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}

	var freeMu sync.Mutex
	free := uint64(1) // start starved: 1 byte free

	h := newHarness(t, runner, func(d *Deps) {
		d.MediaDir = "/media"
		d.FreeBytes = func(dir string) (uint64, error) {
			freeMu.Lock()
			defer freeMu.Unlock()
			return free, nil
		}
	})
	// min_free_gb defaults to 5 (migration default); 1 byte free is well below it.
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "worker reports low disk", func() bool { return h.worker.LowDisk() })

	// Give the paused loop a moment to (not) do anything, then assert the
	// job was never claimed and the runner was never invoked.
	time.Sleep(30 * time.Millisecond)
	if got := h.jobState(t, id).State; got != "pending" {
		t.Fatalf("job state = %q, want pending (must not be claimed while low on disk)", got)
	}
	if c := runner.calls(); c != 0 {
		t.Fatalf("runner called %d times while low on disk, want 0", c)
	}

	// Free up space: the worker should resume claiming and finish the job.
	freeMu.Lock()
	free = 100 * 1024 * 1024 * 1024 // 100 GiB, comfortably above the 5 GB default
	freeMu.Unlock()

	waitFor(t, "job done after disk freed", func() bool { return h.jobState(t, id).State == "done" })
	waitFor(t, "worker clears low disk", func() bool { return !h.worker.LowDisk() })
}

// TestWorker_nonPositiveMinFreeDisablesGuard is finding 4's defense in depth
// for the disk guard: a non-positive min_free_gb (which uint64() would wrap
// into an enormous floor, freezing the queue forever) must instead disable the
// guard — the worker treats it as "always enough space" and processes the job
// even with almost no free space reported.
func TestWorker_nonPositiveMinFreeDisablesGuard(t *testing.T) {
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, func(d *Deps) {
		d.MediaDir = "/media"
		d.FreeBytes = func(dir string) (uint64, error) { return 1, nil } // 1 byte free
	})
	// Force a non-positive floor directly in the store (the API rejects this,
	// but the worker must not wedge if a bad value ever lands there).
	zero := 0
	if err := h.settings.Update(context.Background(), settings.Patch{MinFreeGB: &zero}); err != nil {
		t.Fatalf("set min_free_gb: %v", err)
	}
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	// The guard is disabled, so the job runs to completion despite ~no free
	// space, and the worker never reports low disk.
	waitFor(t, "job done with guard disabled", func() bool { return h.jobState(t, id).State == "done" })
	if h.worker.LowDisk() {
		t.Fatal("LowDisk() = true with a non-positive min_free_gb, want the guard disabled")
	}
}
