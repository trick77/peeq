// Package download drives the single-concurrency download worker: the one
// goroutine that claims queued jobs, runs them through the yt-dlp Runner,
// and classifies the outcome into retry / fail / pause. It is serial by
// design — YouTube tolerates only so many calls, and the Runner already
// enforces a 20s+ floor between them, so there is no benefit to (and real
// risk in) downloading two videos at once.
package download

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/failmonitor"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/mediaprobe"
	"github.com/trick77/peeq/internal/sched"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// metadataPreflightTimeout bounds the worker's pre-download metadata fetch so a
// hung yt-dlp probe can't stall the single-threaded queue forever. Generous
// enough for a slow-but-legit resolve; a real stall is killed well before it
// starves the rest of the queue.
const metadataPreflightTimeout = 2 * time.Minute

// autoDownloadPriority is the priority the scan scheduler enqueues with — work
// nobody is sitting in front of. Anything above it was asked for by a person
// (the Inbox approve, the re-download button, the channel handler all use 10),
// and process() puts those on the pacer's interactive lane.
//
// Deliberately re-stated here rather than imported from internal/scan: the
// worker must not depend on the scheduler, and the contract this encodes is
// "0 means automatic", not "whatever the scheduler happens to pass today".
const autoDownloadPriority = 0

// Runner is the subset of *ytdlp.Runner the worker needs. Declaring it here
// (rather than importing the concrete type) keeps the worker testable with
// a fake that never shells out to yt-dlp; the real *ytdlp.Runner satisfies
// it.
type Runner interface {
	Download(ctx context.Context, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error)
	// Metadata resolves a video's title/channel/etc. Used by the preflight
	// step for videos added by URL, which are now enqueued without metadata
	// (POST /api/downloads no longer blocks on this call).
	Metadata(ctx context.Context, rawURL string) (*ytdlp.Meta, error)
}

// SummaryEnqueuer is the subset of *summaryjobs.Store the worker needs to
// queue a summary job after a successful download. Declared here rather than
// imported because importing summaryjobs would be an import cycle. The
// failure monitor and the activity recorder come from their own packages
// (failmonitor.Sink, activity.Recorder) — both import nothing of ours, so
// they can be shared without one.
type SummaryEnqueuer interface {
	Enqueue(videoID string) (int64, error)
}

// ScanLedger is the slice of *channelvideos.Store the worker needs to hand a
// walled-off video back to the scan ledger. Narrow on purpose — the worker
// only ever moves a row INTO the unavailable state; deciding when it comes
// back out belongs to the scan scheduler, which is the thing that re-lists the
// channel.
//
// Get rather than Exists: the answer is needed twice over. It says whether
// there is a row to park at all, and it carries the title the Activity row
// should name — a gated video usually has no title on its videos row, because
// the metadata preflight hits the same wall the download does, so the ledger
// row written by the scan is the only place the human-readable name survives.
type ScanLedger interface {
	Get(videoID string) (*channelvideos.Entry, error)
	SetUnavailable(videoID, reason string) error
}

// MediaProber reads the container/codec/resolution facts out of a finished
// download. Declared as an interface so the worker's tests can drive a stub
// instead of needing a real ffprobe binary.
type MediaProber interface {
	Probe(ctx context.Context, path string) (mediaprobe.Info, error)
}

// ChannelCache is the slice of channels.Store the worker needs: caching the
// identity of a downloaded video's channel. Narrow on purpose — the worker
// has no business adding, subscribing or deleting anything.
type ChannelCache interface {
	Upsert(channels.Channel) error
}

// Deps are the worker's collaborators and tunables. The stores and Runner
// are required; the rest have safe defaults applied in New.
type Deps struct {
	Jobs     *jobs.Store
	Videos   *videos.Store
	Settings *settings.Store
	Runner   Runner
	// DB is the handle the Jobs and Videos stores share. succeed opens one
	// transaction on it so a job's 'done' and its video's 'downloaded' land
	// together; production and the test harness always set it.
	DB *sql.DB

	// Prober, when set, is run against the finished file right after
	// SetDownloaded persists, so a new download shows its media facts on
	// first play without waiting for the backfill loop. Nil skips the probe
	// entirely (the backfill loop then picks the video up); production
	// always sets it.
	Prober MediaProber

	// Channels, when set, caches the identity of the channel a downloaded
	// video came from, so a video added by URL leaves its channel reachable in
	// the Channels list under "From downloads". Nil (the default in tests that
	// do not care) skips the write; production always sets it. Caching a
	// channel never adds it — see channels.Store.Upsert.
	Channels ChannelCache

	// Ledger, when set, is the scan ledger a walled-off video is handed back
	// to instead of being left as a dead 'error' row in the Library. Nil (the
	// default in tests that do not care) makes every terminal failure take the
	// plain error path; production always sets it. See Worker.park.
	Ledger ScanLedger

	// SummaryJobs, when set, is enqueued for every successful download
	// (initial or re-download) right after SetDownloaded persists. Nil
	// (the default in tests that don't care about summaries) skips the
	// enqueue entirely; production always sets it.
	SummaryJobs SummaryEnqueuer
	// DefaultSubLang is the --sub-langs value used when a video's
	// AudioLanguage is not yet known (e.g. its first download). Once a video
	// has a resolved AudioLanguage, that takes precedence.
	DefaultSubLang string

	// Watchdog is the inactivity timeout: if a running download produces no
	// progress for this long, its context is cancelled (killing the child)
	// and the job is retried. Zero selects the 10-minute default; a negative
	// value disables the watchdog entirely.
	Watchdog time.Duration
	// MetadataTimeout is the same idea for the pre-download metadata probe:
	// how long a running yt-dlp resolve may go without finishing before it is
	// killed and the job retried. Zero selects metadataPreflightTimeout; a
	// negative value disables the cap. Like Watchdog it runs from when the
	// process starts, not from when the call is made, so time spent queueing
	// on the shared pacer does not count against it.
	MetadataTimeout time.Duration
	// PollInterval is how long the loop waits before re-checking the queue
	// when it found nothing to claim.
	PollInterval time.Duration
	// Backoff returns how long a job waits before it can be claimed again
	// after a retryable failure, given the job's new attempts count. The job
	// is requeued at once with that wait stamped on its row (next_attempt_at,
	// see jobs.Store.BumpAfter); for a failure that says something about
	// YouTube rather than the job (a 429 or 5xx, ytdlp.RetryableError) the
	// loop also stops claiming for the same duration — see retry.
	Backoff func(attempts int) time.Duration
	// OnProgress, if set, is called for every progress update of every job
	// (the SSE fan-out hooks in here later).
	OnProgress func(jobID int64, p ytdlp.Progress)
	// Logger is used for recovered panics and unexpected store errors.
	Logger *slog.Logger

	// onClaim, if set, is invoked in the worker goroutine right after a
	// claimed job has been registered as the running job and BEFORE its
	// preflight reads (Videos.Get / Settings.Get). It is a test seam for
	// exercising the early-cancel window deterministically; it is unset in
	// production.
	onClaim func(jobID int64)

	// MediaDir is the directory the disk-space guard checks free space on
	// before claiming a job (config.MediaDir in production). Empty disables
	// the guard entirely (used by tests that don't care about it).
	MediaDir string
	// FreeBytes reports free space on dir; defaults to the real statfs-backed
	// freeBytes. Overridable so tests can simulate a full disk without
	// actually filling one.
	FreeBytes func(dir string) (uint64, error)

	// YoutubePaused, when set and returning true, parks the loop before
	// claiming a job (the kill-switch poll-gate). It reads the settings flag
	// each poll, so clearing it resumes automatically — decoupled from the
	// cookie/disk in-memory pause.
	YoutubePaused func() bool
	// FailMonitor, when set, is fed a Fail(videoID) on each count-worthy
	// failure and Reset() on each success, driving auto-pause.
	FailMonitor failmonitor.Sink
	// Activity, when set, records each terminal download for the Activity feed.
	Activity activity.Recorder
}

// Worker is the download loop. Construct with New and drive with Run; other
// goroutines (the API layer) may call Cancel and Resume concurrently.
type Worker struct {
	deps Deps

	mu       sync.Mutex
	paused   bool
	resumeCh chan struct{}
	// lowDisk mirrors the outcome of the most recent disk-space precheck
	// (see waitWhileLowDisk / LowDisk). Distinct from paused: paused is the
	// cookie-blocked banner, lowDisk is the disk-space banner, and unlike
	// paused, a low-disk condition does not require an explicit Resume() —
	// it self-clears the moment a later precheck sees enough free space.
	lowDisk bool
	// The single running job's control. curJobID is 0 when idle.
	curJobID        int64
	curCancel       context.CancelFunc
	cancelRequested bool
	// cooldownUntil is the instant before which the loop claims nothing: set
	// by a YouTube-side retryable failure (429/5xx), which is a signal about
	// the host, not about the job that happened to see it. See retry.
	cooldownUntil time.Time
}

// New builds a Worker, filling in defaults for the optional Deps fields.
func New(deps Deps) *Worker {
	if deps.PollInterval <= 0 {
		deps.PollInterval = 1 * time.Second
	}
	switch {
	case deps.Watchdog == 0:
		deps.Watchdog = 10 * time.Minute
	case deps.Watchdog < 0:
		// Negative disables the watchdog; normalize to 0 so the download path
		// (which starts the timer only when Watchdog > 0) skips it.
		deps.Watchdog = 0
	}
	switch {
	case deps.MetadataTimeout == 0:
		deps.MetadataTimeout = metadataPreflightTimeout
	case deps.MetadataTimeout < 0:
		// Same normalization as Watchdog: negative means "no cap", and
		// ytdlp.DeferredTimer reads a non-positive duration as disabled.
		deps.MetadataTimeout = 0
	}
	if deps.Backoff == nil {
		deps.Backoff = defaultBackoff
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.FreeBytes == nil {
		deps.FreeBytes = freeBytes
	}
	return &Worker{
		deps:     deps,
		resumeCh: make(chan struct{}),
	}
}

// defaultBackoff is a capped exponential backoff: 5s, 10s, 20s, ... up to
// 5 minutes.
func defaultBackoff(attempts int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < attempts; i++ {
		d *= 2
		if d >= 5*time.Minute {
			return 5 * time.Minute
		}
	}
	return d
}

// Run is the worker loop; it blocks until ctx is cancelled. It first resets
// any orphaned running jobs left by a previous process, then repeatedly
// claims and processes the next job, pausing (and not claiming) while a
// blocked/expired cookie is unresolved.
func (w *Worker) Run(ctx context.Context) {
	if err := w.deps.Jobs.ResetOrphans(); err != nil {
		w.deps.Logger.Error("download worker: reset orphans failed", "err", err)
	}

	for {
		if ctx.Err() != nil {
			return
		}
		if !w.waitWhilePaused(ctx) {
			return
		}
		if !w.checkDiskSpace(ctx) {
			return
		}
		if w.LowDisk() {
			// Refuse to start a job while below the configured free-space
			// floor: skip claiming entirely (the job stays pending, so
			// nothing needs to be un-claimed) and re-check after a beat.
			if !w.sleep(ctx, w.deps.PollInterval) {
				return
			}
			continue
		}
		if w.deps.YoutubePaused != nil && w.deps.YoutubePaused() {
			// Kill-switch engaged: don't claim or run anything. Re-check each
			// poll so a resume proceeds automatically.
			if !w.sleep(ctx, w.deps.PollInterval) {
				return
			}
			continue
		}
		if remaining := w.cooldownRemaining(); remaining > 0 {
			// YouTube asked for a breather: hitting it with the next job would
			// only spend that job's attempts on the same answer.
			if !w.sleep(ctx, min(remaining, w.deps.PollInterval)) {
				return
			}
			continue
		}

		job, err := w.deps.Jobs.ClaimNext()
		if err != nil {
			w.deps.Logger.Error("download worker: claim failed", "err", err)
			if !w.sleep(ctx, w.deps.PollInterval) {
				return
			}
			continue
		}
		if job == nil {
			// Queue empty: wait a beat and re-check.
			if !w.sleep(ctx, w.deps.PollInterval) {
				return
			}
			continue
		}

		w.safely(job.ID, func() { w.process(ctx, job) })
	}
}

// safely runs fn, recovering from any panic so one pathological job can
// never kill the worker goroutine and silently stop all downloads.
func (w *Worker) safely(jobID int64, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			w.deps.Logger.Error("download worker: recovered from panic", "job_id", jobID, "panic", r)
		}
	}()
	fn()
}

// Cancel stops job jobID. If it is the running job — registered from the
// moment it is claimed, through preflight and the download — the cancel flag
// is set and its context is cancelled (killing any child), and the worker's
// completion path writes the canceled state; the single-writer rule avoids a
// double write racing the loop. Otherwise (a merely pending job, or the tiny
// window after the worker has cleared its registration but before its guarded
// terminal write) the job is canceled directly in the store, where the
// state = 'running' guard on Finish/Bump/Fail stops that write from
// resurrecting the now-canceled row.
//
// The returned bool reports whether a pending/running job was actually
// cancelled: true for the running-job (flag) path, and whatever the store
// reports for the store-fallback path — false for an unknown job id or one
// already in a terminal state, so callers (the HTTP handler) can tell an
// unknown/finished job apart from a real cancel.
func (w *Worker) Cancel(jobID int64) bool {
	w.mu.Lock()
	if jobID == w.curJobID && w.curCancel != nil {
		w.cancelRequested = true
		cancel := w.curCancel
		w.mu.Unlock()
		// Kill the child; the loop's completion path is the single writer
		// that will mark the job canceled once Download returns.
		cancel()
		return true
	}
	w.mu.Unlock()
	canceled, err := w.deps.Jobs.Cancel(jobID)
	if err != nil {
		w.deps.Logger.Error("download worker: cancel pending job failed", "job_id", jobID, "err", err)
		return false
	}
	return canceled
}

// Resume clears a pause (from a blocked/expired cookie) and wakes the loop
// so it starts claiming again — typically called after the user re-validates
// their cookie.
func (w *Worker) Resume() {
	w.mu.Lock()
	if w.paused {
		w.paused = false
		close(w.resumeCh)
		w.resumeCh = make(chan struct{})
	}
	w.mu.Unlock()
}

// Paused reports whether the worker is currently paused.
func (w *Worker) Paused() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.paused
}

// gibibyte is the unit settings.MinFreeGB is expressed in.
const gibibyte = 1024 * 1024 * 1024

// checkDiskSpace runs the disk-space precheck: it reads the current
// min_free_gb setting and free space on MediaDir, updating the lowDisk flag
// to reflect the outcome. A settings-read error or an empty MediaDir (the
// guard is disabled) leaves lowDisk unchanged rather than blocking the
// worker on an unrelated failure; a FreeBytes error is treated the same
// way, failing open, since a broken statfs call must not permanently wedge
// downloads. It returns false only when ctx is already done (the caller
// should stop), matching the other wait* helpers' convention.
func (w *Worker) checkDiskSpace(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if w.deps.MediaDir == "" {
		return true
	}
	set, err := w.deps.Settings.Get(ctx)
	if err != nil {
		w.deps.Logger.Error("download worker: disk check: load settings failed", "err", err)
		return true
	}
	// A non-positive floor disables the guard. Guarding here is defense in
	// depth against a bad settings value slipping past the API validation:
	// uint64(negative) wraps to an enormous floor that would freeze the queue
	// permanently, so treat MinFreeGB <= 0 as "guard disabled" (always enough
	// space) and clear any prior low-disk state rather than wrap.
	if set.MinFreeGB <= 0 {
		w.mu.Lock()
		w.lowDisk = false
		w.mu.Unlock()
		return true
	}

	free, err := w.deps.FreeBytes(w.deps.MediaDir)
	if err != nil {
		w.deps.Logger.Error("download worker: disk check: statfs failed", "dir", w.deps.MediaDir, "err", err)
		return true
	}

	minFree := uint64(set.MinFreeGB) * gibibyte
	low := free < minFree

	w.mu.Lock()
	wasLow := w.lowDisk
	w.lowDisk = low
	w.mu.Unlock()

	if low && !wasLow {
		w.deps.Logger.Warn("download worker: low disk space, pausing claims", "free_bytes", free, "min_free_gb", set.MinFreeGB)
	} else if !low && wasLow {
		w.deps.Logger.Info("download worker: disk space recovered, resuming claims", "free_bytes", free)
	}
	return true
}

// LowDisk reports whether the most recent disk-space precheck found free
// space below the configured min_free_gb floor.
func (w *Worker) LowDisk() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lowDisk
}

// waitWhilePaused blocks until the worker is resumed or ctx is cancelled. It
// returns false only when ctx is done (the caller should then stop).
func (w *Worker) waitWhilePaused(ctx context.Context) bool {
	for {
		w.mu.Lock()
		if !w.paused {
			w.mu.Unlock()
			return true
		}
		ch := w.resumeCh
		w.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-ch:
		}
	}
}

// sleep waits d unless ctx is cancelled first. It returns false if ctx was
// cancelled (the caller should stop), true if the full wait elapsed.
func (w *Worker) sleep(ctx context.Context, d time.Duration) bool {
	return sched.Sleep(ctx, d)
}
