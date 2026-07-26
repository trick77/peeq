package download

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/mediaprobe"
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
	// metaFn scripts the worker's metadata preflight. Nil returns an empty meta
	// (a no-op: the video row already had no title, so nothing changes) so
	// tests that only care about the download path are unaffected.
	metaN  int
	metaFn func(ctx context.Context, rawURL string) (*ytdlp.Meta, error)
	// manualStart hands the scripted fn responsibility for calling
	// ytdlp.SignalStart. The real Runner fires it once the pacer lets the call
	// through and yt-dlp is about to run, so by default the fake fires it up
	// front — a fake that never fired it would leave the worker's timers
	// permanently unarmed, and the watchdog tests would pass by doing nothing.
	// Set it when the test is ABOUT that gap: the pacer wait between entering
	// the call and the process starting.
	manualStart bool
}

func (f *fakeRunner) Download(ctx context.Context, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
	f.mu.Lock()
	call := f.n
	f.n++
	manual := f.manualStart
	f.mu.Unlock()
	if !manual {
		ytdlp.SignalStart(ctx)
	}
	return f.fn(ctx, call, req, onProgress)
}

func (f *fakeRunner) Metadata(ctx context.Context, rawURL string) (*ytdlp.Meta, error) {
	f.mu.Lock()
	f.metaN++
	fn := f.metaFn
	manual := f.manualStart
	f.mu.Unlock()
	if !manual {
		ytdlp.SignalStart(ctx)
	}
	if fn == nil {
		return &ytdlp.Meta{}, nil
	}
	return fn(ctx, rawURL)
}

func (f *fakeRunner) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

func (f *fakeRunner) metaCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.metaN
}

type harness struct {
	db       *sql.DB
	jobs     *jobs.Store
	videos   *videos.Store
	settings *settings.Store
	channels *channels.Store
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
		channels: channels.New(db),
		runner:   runner,
	}
	// Channels is wired by default so the harness matches production, where
	// the worker always caches a downloaded video's channel. Tests that want
	// the nil path set it back to nil in tune.
	deps := Deps{
		Jobs:         h.jobs,
		Videos:       h.videos,
		Settings:     h.settings,
		Channels:     h.channels,
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

// fakeMonitor is a FailMonitor test double: it records Fail/Reset calls via
// caller-supplied callbacks (nil callbacks are simply no-ops).
type fakeMonitor struct {
	onFail  func(string)
	onReset func()
}

func (f *fakeMonitor) Fail(id string) {
	if f.onFail != nil {
		f.onFail(id)
	}
}

func (f *fakeMonitor) Reset() {
	if f.onReset != nil {
		f.onReset()
	}
}

// withFailMonitor is a newTestWorker option that injects a FailMonitor.
func withFailMonitor(fm FailMonitor) func(*Deps) {
	return func(d *Deps) { d.FailMonitor = fm }
}

// newTestWorker builds a worker via the standard harness (real store, fake
// Runner that always succeeds), applying opts to Deps. It also seeds video
// rows "v1"/"v2" so classify tests that pass hand-built *videos.Video structs
// (not fetched from the store) can still Get() the row back afterward.
func newTestWorker(t *testing.T, opts ...func(*Deps)) *Worker {
	t.Helper()
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, func(d *Deps) {
		for _, opt := range opts {
			opt(d)
		}
	})
	for _, id := range []string{"v1", "v2"} {
		if err := h.videos.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	return h.worker
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
	// The worker writes the job terminal state BEFORE the video row (see
	// waitForVideoStatus), so the job reading "done" does not mean the
	// download's own writes have landed yet.
	waitForVideoStatus(t, h, "vid", "downloaded")
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

// --- Metadata preflight (instant scheduling) --------------------------------

// TestWorker_metadataPreflightPopulatesTitle proves the preflight added for
// instant scheduling: a video enqueued by URL has no title (POST no longer
// fetches metadata), so the worker resolves it before downloading and the row
// ends up with a real title/channel — normalizing yt-dlp's "public" too.
func TestWorker_metadataPreflightPopulatesTitle(t *testing.T) {
	runner := &fakeRunner{
		metaFn: func(ctx context.Context, rawURL string) (*ytdlp.Meta, error) {
			return &ytdlp.Meta{Title: "Resolved Title", ChannelID: "UC123", Channel: "Some Channel", Availability: "public"}, nil
		},
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, nil)
	job := h.enqueue(t, "vid1", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done", func() bool { return h.jobState(t, job).State == "done" })
	// The worker writes the job terminal state BEFORE the video row (see
	// waitForVideoStatus), so the job reading "done" does not mean the
	// download's own writes have landed yet.
	waitForVideoStatus(t, h, "vid1", "downloaded")

	v, err := h.videos.Get("vid1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.Title != "Resolved Title" {
		t.Fatalf("video title = %q, want %q (preflight must populate it)", v.Title, "Resolved Title")
	}
	if v.ChannelName != "Some Channel" {
		t.Fatalf("video channel = %q, want %q", v.ChannelName, "Some Channel")
	}
	if v.Availability != "available" {
		t.Fatalf("availability = %q, want normalized %q", v.Availability, "available")
	}
	if n := runner.metaCalls(); n != 1 {
		t.Fatalf("metadata calls = %d, want 1", n)
	}
}

// TestWorker_metadataPreflightCachesChannel asserts the preflight also leaves
// a channels row behind. videos has no FK to channels, so without this a video
// added by URL would leave its channel with no row at all and no way of ever
// reaching the Channels list.
//
// The row must stay cache-only: added_at NULL, or the channel would silently
// become a scan target the user never asked for.
func TestWorker_metadataPreflightCachesChannel(t *testing.T) {
	runner := &fakeRunner{
		metaFn: func(ctx context.Context, rawURL string) (*ytdlp.Meta, error) {
			return &ytdlp.Meta{Title: "T", ChannelID: "UC123", Channel: "Some Channel", Availability: "public"}, nil
		},
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, nil)
	job := h.enqueue(t, "vid1", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done", func() bool { return h.jobState(t, job).State == "done" })
	// The worker writes the job terminal state BEFORE the video row (see
	// waitForVideoStatus), so the job reading "done" does not mean the
	// download's own writes have landed yet.
	waitForVideoStatus(t, h, "vid1", "downloaded")

	c, err := h.channels.Get("UC123")
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	if c == nil {
		t.Fatal("preflight left no channels row for the downloaded video's channel")
	}
	if c.Name != "Some Channel" {
		t.Fatalf("cached channel name = %q, want %q", c.Name, "Some Channel")
	}
	if c.AddedAt != "" {
		t.Fatalf("caching the channel added it (added_at = %q)", c.AddedAt)
	}
	// It is listed now — under "downloaded", never under the added filters.
	items, err := h.channels.List("downloaded")
	if err != nil {
		t.Fatalf("list downloaded: %v", err)
	}
	if len(items) != 1 || items[0].ID != "UC123" {
		t.Fatalf("downloaded filter = %+v, want just UC123", items)
	}
}

// TestWorker_metadataPreflightWithoutChannelStore proves Deps.Channels is
// optional: a nil store skips the cache write instead of panicking mid-
// download, which is what keeps every test that does not care about channels
// working unchanged.
func TestWorker_metadataPreflightWithoutChannelStore(t *testing.T) {
	runner := &fakeRunner{
		metaFn: func(ctx context.Context, rawURL string) (*ytdlp.Meta, error) {
			return &ytdlp.Meta{Title: "T", ChannelID: "UC123", Channel: "Some Channel"}, nil
		},
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, func(d *Deps) { d.Channels = nil })
	job := h.enqueue(t, "vid1", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done", func() bool { return h.jobState(t, job).State == "done" })
	// The worker writes the job terminal state BEFORE the video row (see
	// waitForVideoStatus), so the job reading "done" does not mean the
	// download's own writes have landed yet.
	waitForVideoStatus(t, h, "vid1", "downloaded")
	c, err := h.channels.Get("UC123")
	if err != nil {
		t.Fatalf("get channel: %v", err)
	}
	if c != nil {
		t.Fatal("a nil Deps.Channels still wrote a channels row")
	}
}

// TestWorker_metadataPreflightPausesOnNoCookie proves the preflight routes a
// cookie failure through the same taxonomy as a download error: a missing
// cookie pauses the worker and requeues the job WITHOUT ever downloading, so
// the problem surfaces on Activity instead of blocking the user at add time.
func TestWorker_metadataPreflightPausesOnNoCookie(t *testing.T) {
	runner := &fakeRunner{
		metaFn: func(ctx context.Context, rawURL string) (*ytdlp.Meta, error) {
			return nil, ytdlp.ErrNoCookie
		},
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			t.Errorf("Download called, but the preflight should have paused before downloading")
			return &ytdlp.Result{MediaPath: "/m.mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, nil)
	job := h.enqueue(t, "vid1", 0)
	runWorker(t, h.worker)

	waitFor(t, "worker paused", func() bool { return h.worker.Paused() })

	j := h.jobState(t, job)
	if j.State != "pending" {
		t.Fatalf("job state = %q, want pending (requeued, not failed)", j.State)
	}
	if j.Attempts != 0 {
		t.Fatalf("job attempts = %d, want 0 (cookie pause must not burn an attempt)", j.Attempts)
	}
	if c := runner.calls(); c != 0 {
		t.Fatalf("Download called %d times, want 0 (preflight paused first)", c)
	}
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

// A call that sits on the shared pacer for longer than the whole watchdog
// window before yt-dlp starts must NOT be killed. This is the bug the watchdog
// had: it was armed when Download was entered, but the pacer's wait happens
// inside that call and emits no progress, so a job with a deep enough queue in
// front of it was cancelled for being patient and reported as a failure.
//
// manualStart is what makes the test about that gap: the fake waits before
// signalling the start hook, exactly as the real Runner waits on throttle
// before launching the process.
func TestWorker_pacerWaitDoesNotTripWatchdog(t *testing.T) {
	const watchdog = 20 * time.Millisecond
	runner := &fakeRunner{
		manualStart: true,
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			// Queue for several watchdog windows with nothing to report, the
			// way a background call waits its turn behind others.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * watchdog):
			}
			// The pacer let it through: yt-dlp starts here.
			ytdlp.SignalStart(ctx)
			onProgress(ytdlp.Progress{Percent: 50})
			return &ytdlp.Result{MediaPath: "/m/vid.mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, func(d *Deps) {
		d.Watchdog = watchdog
	})
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done after a long pacer wait", func() bool { return h.jobState(t, id).State == "done" })
	if c := runner.calls(); c != 1 {
		t.Fatalf("runner called %d times, want 1 (queueing is not a stalled download)", c)
	}
}

// The same guarantee for the metadata preflight, which has its own (shorter)
// cap and so tripped first: a preflight that queues past the cap before yt-dlp
// starts must still resolve, not retry.
func TestWorker_pacerWaitDoesNotTripPreflightCap(t *testing.T) {
	runner := &fakeRunner{
		manualStart: true,
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			ytdlp.SignalStart(ctx)
			return &ytdlp.Result{MediaPath: "/m/vid.mp4", FormatUsed: "f"}, nil
		},
		metaFn: func(ctx context.Context, rawURL string) (*ytdlp.Meta, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
			ytdlp.SignalStart(ctx)
			return &ytdlp.Meta{Title: "Resolved late", Channel: "Chan"}, nil
		},
	}
	// A cap far shorter than the fake's queueing wait: timed from entry it
	// would always fire, timed from the start hook it never does. The absolute
	// numbers matter as well as the ratio — the cap has to be comfortably longer
	// than the sliver between SignalStart and the fake's return, or a scheduling
	// hiccup in that gap fires it for real and the job retries for no reason.
	h := newHarness(t, runner, func(d *Deps) {
		d.Watchdog = -1
		d.MetadataTimeout = 25 * time.Millisecond
	})
	// enqueue seeds the video with no title, which is what puts the job
	// through the preflight at all.
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done after a long preflight wait", func() bool { return h.jobState(t, id).State == "done" })
	v, err := h.videos.Get("vid")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.Title != "Resolved late" {
		t.Fatalf("title = %q, want the preflight result (the cap must not have fired)", v.Title)
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

// --- youtube_paused kill-switch: poll-gate, ErrPaused, failmonitor (Task 8) -

// TestClassifyErrPausedRequeuesWithoutAttempt asserts the kill-switch pause
// path is fully decoupled from the cookie/disk in-memory pause: it must
// requeue at the SAME attempts count, must NOT mark the video 'error', and
// must NOT set the worker's in-memory Paused() flag (that flag is reserved
// for the cookie machinery; the kill-switch is parked solely by the
// YoutubePaused poll-gate in Run).
func TestClassifyErrPausedRequeuesWithoutAttempt(t *testing.T) {
	w := newTestWorker(t)
	job := &jobs.Job{ID: 1, VideoID: "v1", Attempts: 0}
	video := &videos.Video{ID: "v1"}
	w.classify(context.Background(), job, video, ytdlp.ErrPaused, true)

	if w.Paused() {
		t.Error("kill-switch pause must not set the cookie-pause flag")
	}
	v, err := w.deps.Videos.Get("v1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.Status == "error" {
		t.Error("ErrPaused must not mark the video error")
	}
}

// TestClassifyErrPaused_StoreBacked drives the same case against a real
// claimed store row (rather than a hand-built *jobs.Job that Bump would just
// no-op against as ErrNotRunning), so it actually proves requeuePaused's core
// contract: the job goes back to 'pending' with attempts UNCHANGED — a
// kill-switch pause never burns an attempt.
func TestClassifyErrPaused_StoreBacked(t *testing.T) {
	h := newHarness(t, &fakeRunner{fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
		return &ytdlp.Result{MediaPath: "/x.mp4", FormatUsed: "f"}, nil
	}}, nil)
	id := h.enqueue(t, "vid", 0)

	claimed, err := h.jobs.ClaimNext()
	if err != nil || claimed == nil {
		t.Fatalf("claim: job=%v err=%v", claimed, err)
	}
	video, err := h.videos.Get("vid")
	if err != nil || video == nil {
		t.Fatalf("get video: video=%v err=%v", video, err)
	}

	h.worker.classify(context.Background(), claimed, video, ytdlp.ErrPaused, true)

	j := h.jobState(t, id)
	if j.State != "pending" {
		t.Fatalf("job state = %q, want pending", j.State)
	}
	if j.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0 (kill-switch pause must not burn an attempt)", j.Attempts)
	}
	v, _ := h.videos.Get("vid")
	if v.Status == "error" {
		t.Fatal("ErrPaused must not mark the video error")
	}
}

// TestFailMonitorFailedOnCountWorthy_ResetOnSuccess asserts the FailMonitor
// hooks: an unclassified error (the default/count-worthy branch) feeds
// Fail(videoID), while a terminal error (per-video, not a systemic YouTube
// problem) must not count towards auto-pause.
func TestFailMonitorFailedOnCountWorthy_ResetOnSuccess(t *testing.T) {
	var fails []string
	reset := 0
	fm := &fakeMonitor{
		onFail:  func(id string) { fails = append(fails, id) },
		onReset: func() { reset++ },
	}
	w := newTestWorker(t, withFailMonitor(fm))

	// An unclassified exec error (default branch) -> Fail(videoID).
	w.classify(context.Background(), &jobs.Job{ID: 1, VideoID: "v1", MaxAttempts: 3}, &videos.Video{ID: "v1"}, errors.New("boom: some new extractor error"), true)
	if len(fails) != 1 || fails[0] != "v1" {
		t.Fatalf("fails=%v, want [v1]", fails)
	}

	// A terminal error must NOT count.
	w.classify(context.Background(), &jobs.Job{ID: 2, VideoID: "v2"}, &videos.Video{ID: "v2"}, &ytdlp.TerminalError{Reason: "private"}, true)
	if len(fails) != 1 {
		t.Fatalf("terminal error counted: fails=%v", fails)
	}
}

// TestClassify_preflightDoesNotFeedFailMonitor asserts countFail=false (the
// metadata preflight) still retries an unclassified error but does NOT nudge
// the auto-pause breaker — one freshly-added URL's transient blip can't pause
// the whole queue.
func TestClassify_preflightDoesNotFeedFailMonitor(t *testing.T) {
	var fails []string
	fm := &fakeMonitor{onFail: func(id string) { fails = append(fails, id) }}
	w := newTestWorker(t, withFailMonitor(fm))

	w.classify(context.Background(), &jobs.Job{ID: 1, VideoID: "v1", MaxAttempts: 3}, &videos.Video{ID: "v1"}, errors.New("boom: transient metadata blip"), false)

	if len(fails) != 0 {
		t.Fatalf("preflight failure fed the FailMonitor: fails=%v, want none", fails)
	}
}

// TestWorker_success_ResetsFailMonitor drives a real successful download
// end-to-end through succeed() and asserts FailMonitor.Reset() is called.
func TestWorker_success_ResetsFailMonitor(t *testing.T) {
	var resetCount int
	var mu sync.Mutex
	fm := &fakeMonitor{onReset: func() {
		mu.Lock()
		resetCount++
		mu.Unlock()
	}}

	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/m/vid.mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, func(d *Deps) {
		d.FailMonitor = fm
	})
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done", func() bool { return h.jobState(t, id).State == "done" })
	waitFor(t, "fail monitor reset", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return resetCount == 1
	})
}

// TestWorker_youtubePausedGateBlocksClaiming asserts the poll-gate: while
// YoutubePaused() returns true, the loop never claims a pending job (it stays
// 'pending', the runner is never invoked), and it does NOT set the worker's
// in-memory Paused() flag — the kill-switch and the cookie pause are
// independent signals. Clearing the predicate lets the queue drain.
func TestWorker_youtubePausedGateBlocksClaiming(t *testing.T) {
	runner := &fakeRunner{
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	var mu sync.Mutex
	paused := true
	h := newHarness(t, runner, func(d *Deps) {
		d.YoutubePaused = func() bool {
			mu.Lock()
			defer mu.Unlock()
			return paused
		}
	})
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	// Give the gated loop a moment to (not) do anything.
	time.Sleep(30 * time.Millisecond)
	if got := h.jobState(t, id).State; got != "pending" {
		t.Fatalf("job state = %q, want pending (must not be claimed while youtube_paused)", got)
	}
	if c := runner.calls(); c != 0 {
		t.Fatalf("runner called %d times while youtube_paused, want 0", c)
	}
	if h.worker.Paused() {
		t.Fatal("YoutubePaused gate must not set the cookie-pause flag")
	}

	// Clear the kill-switch: the loop resumes claiming on its own, no Resume()
	// call needed (decoupled from the cookie machinery).
	mu.Lock()
	paused = false
	mu.Unlock()

	waitFor(t, "job done after youtube_paused cleared", func() bool { return h.jobState(t, id).State == "done" })
}

// stubProber answers every path with one canned result, or fails.
type stubProber struct {
	info  mediaprobe.Info
	err   error
	mu    sync.Mutex
	calls []string
}

func (s *stubProber) Probe(_ context.Context, path string) (mediaprobe.Info, error) {
	s.mu.Lock()
	s.calls = append(s.calls, path)
	s.mu.Unlock()
	return s.info, s.err
}

func (s *stubProber) called() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// probeRunner is the minimal successful download the probe tests need.
func probeRunner() *fakeRunner {
	return &fakeRunner{
		fn: func(context.Context, int, ytdlp.DownloadReq, func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/media/vid/vid.mp4", FilesizeBytes: 42, FormatUsed: "bv*+ba"}, nil
		},
	}
}

func TestWorker_probesTheFinishedFile(t *testing.T) {
	prober := &stubProber{info: mediaprobe.Info{
		Container: "mp4", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "aac",
	}}
	h := newHarness(t, probeRunner(), func(d *Deps) { d.Prober = prober })
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done", func() bool { return h.jobState(t, id).State == "done" })
	// The worker writes the job terminal state BEFORE the video row (see
	// waitForVideoStatus), so the job reading "done" does not mean the
	// download's own writes have landed yet.
	waitForVideoStatus(t, h, "vid", "downloaded")
	waitForVideoStatus(t, h, "vid", "downloaded")
	waitFor(t, "probe persisted", func() bool {
		v, err := h.videos.Get("vid")
		return err == nil && v != nil && v.ProbedAt != ""
	})

	v, err := h.videos.Get("vid")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.MediaContainer != "mp4" || v.VideoCodec != "h264" || v.VideoHeight != 1080 || v.AudioCodec != "aac" {
		t.Fatalf("probe not persisted: %+v", v)
	}
	if got := prober.called(); len(got) != 1 || got[0] != "/media/vid/vid.mp4" {
		t.Fatalf("probed %v, want the finished media path once", got)
	}
}

// The media facts are decoration. A broken or missing ffprobe must cost the
// user nothing: the download still lands, and the summary is still queued.
func TestWorker_probeFailureIsNotFatal(t *testing.T) {
	spy := &spySummaryJobs{}
	h := newHarness(t, probeRunner(), func(d *Deps) {
		d.Prober = &stubProber{err: errors.New("ffprobe: not found")}
		d.SummaryJobs = spy
	})
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done", func() bool { return h.jobState(t, id).State == "done" })
	// The worker writes the job terminal state BEFORE the video row (see
	// waitForVideoStatus), so the job reading "done" does not mean the
	// download's own writes have landed yet.
	waitForVideoStatus(t, h, "vid", "downloaded")
	waitForVideoStatus(t, h, "vid", "downloaded")
	waitFor(t, "summary enqueued", func() bool { return len(spy.enqueued()) == 1 })

	// Nothing is written, so probed_at stays NULL and the backfill sweep picks
	// the video up. The sweep IS the retry; stamping here would suppress it.
	v, err := h.videos.Get("vid")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.ProbedAt != "" {
		t.Fatalf("failed probe stamped probed_at; the sweep will never retry it: %+v", v)
	}
	if v.MediaContainer != "" || v.VideoCodec != "" {
		t.Fatalf("failed probe wrote values: %+v", v)
	}
}

// A re-download re-probes a row that may ALREADY hold good facts. A transient
// ffprobe failure must not blank them — and must not stamp probed_at either,
// or the sweep could never repair what it wiped.
func TestWorker_probeFailureKeepsTheValuesFromAnEarlierProbe(t *testing.T) {
	h := newHarness(t, probeRunner(), func(d *Deps) {
		d.Prober = &stubProber{err: errors.New("ffprobe: temporarily unavailable")}
	})
	id := h.enqueue(t, "vid", 0)

	// Stand in for a successful probe on the first download.
	if err := h.videos.SetProbed("vid", videos.ProbeResult{
		Container: "mp4", VideoCodec: "h264", VideoHeight: 1080, AudioCodec: "aac",
	}); err != nil {
		t.Fatalf("seed earlier probe: %v", err)
	}
	before, err := h.videos.Get("vid")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}

	runWorker(t, h.worker)
	waitFor(t, "job done", func() bool { return h.jobState(t, id).State == "done" })
	// The worker writes the job terminal state BEFORE the video row (see
	// waitForVideoStatus), so the job reading "done" does not mean the
	// download's own writes have landed yet.
	waitForVideoStatus(t, h, "vid", "downloaded")
	waitForVideoStatus(t, h, "vid", "downloaded")

	v, err := h.videos.Get("vid")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.MediaContainer != "mp4" || v.VideoCodec != "h264" || v.VideoHeight != 1080 || v.AudioCodec != "aac" {
		t.Fatalf("a failed re-probe wiped good values: %+v", v)
	}
	if v.ProbedAt != before.ProbedAt {
		t.Errorf("probed_at moved on a failed probe: %q -> %q", before.ProbedAt, v.ProbedAt)
	}
}

func TestWorker_nilProberSkipsTheProbe(t *testing.T) {
	h := newHarness(t, probeRunner(), func(d *Deps) { d.Prober = nil })
	id := h.enqueue(t, "vid", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done", func() bool { return h.jobState(t, id).State == "done" })
	// The worker writes the job terminal state BEFORE the video row (see
	// waitForVideoStatus), so the job reading "done" does not mean the
	// download's own writes have landed yet.
	waitForVideoStatus(t, h, "vid", "downloaded")
	waitForVideoStatus(t, h, "vid", "downloaded")

	v, err := h.videos.Get("vid")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	// Left unprobed on purpose: the backfill loop is what picks it up.
	if v.ProbedAt != "" {
		t.Fatalf("probed_at stamped with no prober: %+v", v)
	}
}

// TestMarshalStrings_emptyMeansLeaveAlone pins a contract that looks like a
// bug to anyone reading it cold: marshalStrings returns "" for an empty list,
// NOT the "[]" its name suggests.
//
// That is what makes SetDownloaded's `COALESCE(NULLIF(?, ”), yt_tags)` mean
// "leave what is stored". "[]" is a value, so returning it would make every
// re-download whose extractor happened to omit tags silently erase the tags
// already on the row. Tidying this to "[]" must fail here rather than in
// someone's library six months later.
func TestMarshalStrings_emptyMeansLeaveAlone(t *testing.T) {
	if got := marshalStrings(nil); got != "" {
		t.Fatalf("marshalStrings(nil) = %q, want \"\" so SetDownloaded keeps the stored value", got)
	}
	if got := marshalStrings([]string{}); got != "" {
		t.Fatalf("marshalStrings([]) = %q, want \"\"", got)
	}
	if got := marshalStrings([]string{"physics", "education"}); got != `["physics","education"]` {
		t.Fatalf("marshalStrings = %q, want a JSON array", got)
	}
	// Values yt-dlp really emits: quotes and non-ASCII must survive as valid
	// JSON, since this string goes into the column verbatim.
	if got := marshalStrings([]string{`say "hi"`, "café"}); got != `["say \"hi\"","café"]` {
		t.Fatalf("marshalStrings = %q, want escaped JSON", got)
	}
}

// --- Scenario: which lane a job takes on the yt-dlp pacer -------------------

// laneHarness runs one job through the worker and reports whether the context
// that reached yt-dlp was on the interactive lane.
func laneHarness(t *testing.T, priority int) bool {
	t.Helper()
	var interactive bool
	var mu sync.Mutex
	runner := &fakeRunner{
		fn: func(ctx context.Context, _ int, req ytdlp.DownloadReq, _ func(ytdlp.Progress)) (*ytdlp.Result, error) {
			mu.Lock()
			interactive = ytdlp.IsInteractive(ctx)
			mu.Unlock()
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	h := newHarness(t, runner, func(*Deps) {})
	id := h.enqueue(t, "lane1", priority)
	job := h.jobState(t, id)
	h.worker.process(context.Background(), &job)
	mu.Lock()
	defer mu.Unlock()
	return interactive
}

// Approving in the Inbox is a person clicking. Before this, the approved
// download took the background lane and could sit through a full pacer gap
// behind a channel scan that happened to start first.
func TestProcess_userAskedForItTakesTheInteractiveLane(t *testing.T) {
	if !laneHarness(t, 10) {
		t.Fatal("a priority-10 job reached yt-dlp on the background lane")
	}
}

// The other half of the rule, and the one the old "never for worker calls"
// comment was really protecting: scan-driven work must not crowd out clicks.
func TestProcess_scheduledWorkStaysOnTheBackgroundLane(t *testing.T) {
	if laneHarness(t, autoDownloadPriority) {
		t.Fatal("a scheduler-priority job jumped the interactive lane")
	}
}

// TestWorker_metadataWriteFailureRetries pins the preflight's save-metadata
// branch to retry rather than fail. The download itself has not run yet at
// that point, so a write that could not land says nothing about the video —
// failing it parked a perfectly good video in "error" and made the user re-add
// it by hand after a transient SQLITE_BUSY under a concurrent writer.
//
// The failure is injected with a trigger rather than a stub because Deps.Videos
// is a concrete *videos.Store. Aborting only UPDATEs that carry a non-empty
// title hits exactly the preflight's ON CONFLICT DO UPDATE write and nothing
// else — reads are untouched, so the retry's Videos.Get at the top of process
// still succeeds, which is what lets the second attempt get as far as the
// preflight again.
func TestWorker_metadataWriteFailureRetries(t *testing.T) {
	var h *harness
	runner := &fakeRunner{
		metaFn: func(ctx context.Context, rawURL string) (*ytdlp.Meta, error) {
			// Drop the guard on the second probe so the retry can complete;
			// the first probe leaves it armed and its Upsert aborts.
			if h.runner.metaCalls() > 1 {
				if _, err := h.db.Exec(`DROP TRIGGER IF EXISTS test_block_meta_write`); err != nil {
					t.Errorf("drop trigger: %v", err)
				}
			}
			return &ytdlp.Meta{Title: "Resolved Title", Availability: "public"}, nil
		},
		fn: func(ctx context.Context, call int, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error) {
			return &ytdlp.Result{MediaPath: "/m/" + req.VideoID + ".mp4", FormatUsed: "f"}, nil
		},
	}
	h = newHarness(t, runner, nil)
	if _, err := h.db.Exec(`
CREATE TRIGGER test_block_meta_write BEFORE UPDATE ON videos
WHEN NEW.title != ''
BEGIN SELECT RAISE(ABORT, 'database is locked'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	job := h.enqueue(t, "vid1", 0)
	runWorker(t, h.worker)

	waitFor(t, "job done", func() bool { return h.jobState(t, job).State == "done" })
	waitForVideoStatus(t, h, "vid1", "downloaded")

	// Two probes: the first attempt's write aborted and the job was requeued,
	// so the preflight ran again. One probe would mean it never retried.
	if got := h.runner.metaCalls(); got != 2 {
		t.Fatalf("metadata probes = %d, want 2 (one per attempt)", got)
	}
	if got := h.jobState(t, job).Attempts; got != 1 {
		t.Fatalf("attempts = %d, want 1 recorded by the requeue", got)
	}
	v, err := h.videos.Get("vid1")
	if err != nil {
		t.Fatalf("get video: %v", err)
	}
	if v.Status == videos.StatusError {
		t.Fatalf("a failed metadata write parked the video in %q", v.Status)
	}
	if v.Title != "Resolved Title" {
		t.Fatalf("title = %q, want the retry's resolved title", v.Title)
	}
}
