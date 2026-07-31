// Package download drives the single-concurrency download worker: the one
// goroutine that claims queued jobs, runs them through the yt-dlp Runner,
// and classifies the outcome into retry / fail / pause. It is serial by
// design — YouTube tolerates only so many calls, and the Runner already
// enforces a 20s+ floor between them, so there is no benefit to (and real
// risk in) downloading two videos at once.
package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/media"
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

// FailMonitor is the subset of *failmonitor.Monitor the worker uses to feed
// the auto-pause heuristic. Nil disables it (tests that don't care).
type FailMonitor interface {
	Fail(entityID string)
	Reset()
}

// ActivityRecorder records a download outcome for the Activity feed. Narrow and
// nil-safe like FailMonitor; nil in tests, the shared *activity.Store in prod.
type ActivityRecorder interface {
	Record(activity.Event)
}

// SummaryEnqueuer is the subset of *summaryjobs.Store the worker needs to
// queue a summary job after a successful download. Declaring it here (rather
// than importing the concrete type) keeps the worker testable with a spy and
// avoids an import cycle back to the summaryjobs package.
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
	// Backoff returns how long to wait before requeueing a job after a
	// retryable failure, given the job's new attempts count.
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
	FailMonitor FailMonitor
	// Activity, when set, records each terminal download for the Activity feed.
	Activity ActivityRecorder
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

// process runs one claimed job end to end: build the request, run the
// download under a watchdog, then classify the result into
// success / cancel / pause / terminal-fail / retry.
func (w *Worker) process(ctx context.Context, job *jobs.Job) {
	// Register this job as the running one IMMEDIATELY — before any metadata
	// or settings reads — so a Cancel arriving during that preflight window
	// targets this job (taking the in-process flag path) and aborts the
	// download promptly, rather than racing the store and being overwritten
	// to done. Create the cancellable job context up front for the same
	// reason: an early Cancel cancels it, so a slow preflight read or the
	// Download call aborts.
	jobCtx, cancel := context.WithCancel(ctx)
	// Approving something in the Inbox is a person clicking, even though a
	// worker is what carries it out. Without this the approved download takes
	// the background lane and can queue behind a channel scan that happened to
	// start first — the person waits out a full pacer gap for work they asked
	// for by hand, while a robot's scan goes first.
	//
	// The distinction is already in the data and needs no schema change: every
	// user-initiated enqueue uses priority 10 (the Inbox approve, the
	// re-download button, the channel handler), while the scan scheduler uses 0.
	if job.Priority > autoDownloadPriority {
		jobCtx = ytdlp.WithInteractive(jobCtx)
	}
	// Panic-safe context cleanup: even if a later step panics (recovered at
	// the loop level), the child context is always cancelled rather than
	// leaked. cancel is idempotent, so the explicit teardown below is fine.
	defer cancel()
	w.mu.Lock()
	w.curJobID = job.ID
	w.curCancel = cancel
	w.cancelRequested = false
	w.mu.Unlock()
	// Panic backstop (finding 4): if a later step panics and is recovered at
	// the loop level, this clears the registration so curJobID can never be
	// left pointing at a finished job. The normal path clears it explicitly
	// (under the same lock as the cancel read) BEFORE any terminal write, so a
	// late Cancel takes the store path where the guarded write cannot
	// resurrect the row; this defer only covers the panic case.
	defer w.unregister(job.ID)

	// Test seam: fired after registration, before the preflight reads, so a
	// test can deterministically inject a Cancel into the early window.
	if w.deps.onClaim != nil {
		w.deps.onClaim(job.ID)
	}

	video, verr := w.deps.Videos.Get(job.VideoID)
	set, serr := w.deps.Settings.Get(ctx)

	// If a Cancel landed during preflight it took the flag path; honor it
	// before writing any other state (single writer, cancel wins) and before
	// SetStatus/Download, so a canceled job never starts downloading.
	if w.wasCanceled() {
		w.settleCanceled(job, video)
		return
	}
	if verr != nil {
		w.deps.Logger.Error("download worker: load video failed", "job_id", job.ID, "err", verr)
		w.fail(job, video, job.Attempts, "load video: "+verr.Error(), "")
		return
	}
	if video == nil {
		w.fail(job, nil, job.Attempts, "video row missing", "")
		return
	}
	if serr != nil {
		// Requeue without burning an attempt: this is our fault, not the job's.
		w.deps.Logger.Error("download worker: load settings failed", "job_id", job.ID, "err", serr)
		switch err := w.deps.Jobs.Bump(job.ID, job.Attempts, "load settings: "+serr.Error()); {
		case errors.Is(err, jobs.ErrNotRunning):
			w.settleCanceled(job, video)
		case err != nil:
			w.deps.Logger.Error("download worker: requeue after settings error failed", "job_id", job.ID, "err", err)
		}
		return
	}

	// Metadata preflight. A video added by URL is now enqueued instantly with
	// no title/channel (POST /api/downloads stopped blocking on yt-dlp), so a
	// row with an empty title has never been resolved — fetch it here, before
	// the download, so the queue/Library/Activity show a real title while it
	// runs. Videos discovered via a channel scan already have a title and skip
	// this. Route any error through the same classify taxonomy as a download
	// error: a missing/expired cookie pauses the job and an unavailable video
	// fails it, surfacing on Activity instead of blocking the user at add time.
	if video.Title == "" {
		// Bound the probe: the Download path has --socket-timeout + the
		// inactivity watchdog, but this one-shot metadata fetch has neither, and
		// jobCtx carries no deadline — a hung yt-dlp probe would stall the
		// single-threaded queue indefinitely.
		//
		// The cap runs from when yt-dlp starts, not from when Metadata is
		// entered, for the same reason the download watchdog does: the pacer
		// makes this call queue behind everything else in flight, and two
		// minutes of that is ordinary on a busy Runner. Timing from entry made a
		// queued probe fail for being patient — and with a shorter fuse than the
		// download path's ten minutes, so it bit first.
		metaCtx, metaCancel := context.WithCancel(jobCtx)
		metaCap := ytdlp.NewDeferredTimer(w.deps.MetadataTimeout, metaCancel)
		meta, merr := w.deps.Runner.Metadata(ytdlp.WithStartHook(metaCtx, metaCap.Start), video.URL)
		// stop() reports false once the timer has fired, which is how the cap is
		// told apart from any other error: the message the user sees on Activity
		// should say the probe stalled, not repeat a bare "context canceled".
		//
		// Called unconditionally and BEFORE the && chain rather than inside it:
		// stop is what disarms the timer, so short-circuiting past it on the
		// success path would leave an AfterFunc holding metaCancel alive for the
		// rest of the cap.
		stoppedInTime := metaCap.Stop()
		// merr != nil is part of the test because a cap that genuinely fired
		// killed the process, so Metadata cannot also have succeeded. Without it,
		// a timer expiring in the sliver between a successful return and stop()
		// would throw away good metadata and retry a job that was already done.
		capFired := merr != nil && !stoppedInTime && metaCtx.Err() != nil && jobCtx.Err() == nil
		metaCancel()
		if w.wasCanceled() {
			w.settleCanceled(job, video)
			return
		}
		if capFired {
			w.retry(ctx, job, video, "metadata preflight timeout: no progress")
			return
		}
		if merr != nil {
			// countFail=false: a preflight blip for one URL must not nudge the
			// global auto-pause breaker. Cookie/blocked still pause; unavailable
			// still fails.
			w.classify(ctx, job, video, merr, false)
			return
		}
		video.Title = meta.Title
		video.ChannelID = meta.ChannelID
		video.ChannelName = meta.Channel
		video.DurationSeconds = int64(meta.DurationSeconds)
		video.PublishedAt = meta.PublishedAt
		video.Description = meta.Description
		// Only when the row has nothing: meta.Thumbnail is a REMOTE CDN url, and
		// letting it displace a local path would point the thumbnail import at a
		// file it can never open. It is kept for the brand-new row, where it is
		// the only hint that exists before the download runs — hence a guard
		// rather than a deletion.
		if video.ThumbnailPath == "" {
			video.ThumbnailPath = meta.Thumbnail
		}
		video.Availability = videos.NormalizeAvailability(meta.Availability)
		if err := w.deps.Videos.Upsert(*video); err != nil {
			// Retry, don't fail: a write that could not land is our
			// infrastructure's problem, not the video's — the same reasoning as
			// the settings-load requeue above. A transient SQLITE_BUSY under a
			// concurrent writer must not park the video in 'error' and make the
			// user re-add it by hand.
			w.deps.Logger.Error("download worker: save metadata failed", "job_id", job.ID, "err", err)
			w.retry(ctx, job, video, "save metadata: "+err.Error())
			return
		}
		// Cache the channel's identity so the Channels list has a row to join
		// against. videos has no FK to channels, so a video added by URL would
		// otherwise leave its channel with no row at all and no way of ever
		// appearing in the list — see the "From downloads" filter.
		//
		// This does NOT add the channel: Upsert never writes added_at, so the
		// row stays out of the scan scheduler's reach, and Upsert's never-blank
		// COALESCE rules mean a re-download cannot clobber a channel whose
		// metadata is already resolved. Best-effort: failing to cache the
		// channel must not fail the download.
		if w.deps.Channels != nil && video.ChannelID != "" {
			if err := w.deps.Channels.Upsert(channels.Channel{
				ID:   video.ChannelID,
				Name: video.ChannelName,
			}); err != nil {
				w.deps.Logger.Warn("download worker: cache channel failed",
					"job_id", job.ID, "channel_id", video.ChannelID, "err", err)
			}
		}
	}

	_ = w.deps.Videos.SetStatus(video.ID, videos.StatusDownloading, "")

	format := set.FormatPreset
	custom := set.FormatCustom
	if video.RequestedFormat != "" {
		// A per-channel format override is a preset id. Rows written before
		// the channel picker existed hold a free-form yt-dlp selector instead
		// and still have to download, so both shapes are accepted; a raw
		// selector is never a preset id, which is what keeps them apart. The
		// free-form one goes through the "custom" slot, where
		// ytdlp.Resolve("custom", x) == x.
		if ytdlp.IsPreset(video.RequestedFormat) {
			format = video.RequestedFormat
			custom = ""
		} else {
			format = "custom"
			custom = video.RequestedFormat
		}
	}
	subLang := video.AudioLanguage
	if subLang == "" {
		subLang = w.deps.DefaultSubLang
	}
	req := ytdlp.DownloadReq{
		URL:          video.URL,
		VideoID:      video.ID,
		Format:       format,
		CustomFormat: custom,
		LimitRate:    set.LimitRate,
		SubLang:      subLang,
	}

	// Inactivity watchdog: armed when yt-dlp actually starts, reset on every
	// progress update; if it fires, it cancels jobCtx (killing the child),
	// which surfaces as a retry below.
	//
	// Armed on the start hook rather than here, because Download does not run
	// yt-dlp immediately: the shared pacer makes the call wait its turn first,
	// and there are no progress lines until the process exists. A timer started
	// here therefore counts the queueing wait as "no progress", and a job with a
	// deep enough queue in front of it was killed before it ever downloaded
	// anything — reported as a failure when it was doing exactly what the pacer
	// is for. Arming on the hook makes the watchdog mean what it says: the
	// process is running and has gone quiet.
	//
	// A Cancel during the pre-call wait does not need the watchdog and never
	// did: throttle's own wait is cancellable.
	watchdog := ytdlp.NewDeferredTimer(w.deps.Watchdog, cancel)
	onProgress := func(p ytdlp.Progress) {
		watchdog.Reset()
		if w.deps.OnProgress != nil {
			w.deps.OnProgress(job.ID, p)
		}
	}

	res, dlErr := w.deps.Runner.Download(ytdlp.WithStartHook(jobCtx, watchdog.Start), req, onProgress)

	watchdog.Stop()
	// Capture whether jobCtx was cancelled (by the watchdog or a user
	// Cancel) BEFORE our own cleanup cancel() below, so the check reflects
	// only a real interruption, not our teardown.
	ctxInterrupted := jobCtx.Err() != nil
	// Read the cancel flag and clear the registration in the SAME critical
	// section, BEFORE any terminal write: after this a late Cancel can no
	// longer find curJobID and must take the store path, where the guarded
	// Finish/Bump/Fail refuses to resurrect the canceled row (which we then
	// observe as ErrNotRunning and settle as canceled).
	w.mu.Lock()
	canceled := w.cancelRequested
	w.curJobID = 0
	w.curCancel = nil
	w.mu.Unlock()
	cancel()

	// Order matters: settle user-cancel and shutdown BEFORE interpreting
	// dlErr, because a killed child returns an unclassified/context error
	// that must not be run through the ytdlp taxonomy.
	switch {
	case canceled:
		w.settleCanceled(job, video)
	case ctx.Err() != nil:
		// Parent shutdown mid-download: leave the job 'running' so the next
		// boot's ResetOrphans reclaims it. Do not write a terminal state.
		return
	case dlErr == nil:
		w.succeed(job, video, res)
	case ctxInterrupted:
		// Not a user cancel, not shutdown, yet the context was cancelled →
		// the watchdog fired. Treat as a retryable timeout.
		w.retry(ctx, job, video, "watchdog timeout: no progress")
	default:
		w.classify(ctx, job, video, dlErr, true)
	}
}

// wasCanceled reports whether a Cancel has been requested for the running job.
func (w *Worker) wasCanceled() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cancelRequested
}

// unregister clears the running-job registration, but only if it still points
// at jobID. The normal path clears it explicitly before the terminal write, so
// this deferred call is a no-op there; it only matters when a panic skipped
// the explicit clear (finding 4). The jobID guard keeps it from clobbering a
// later job's registration.
func (w *Worker) unregister(jobID int64) {
	w.mu.Lock()
	if w.curJobID == jobID {
		w.curJobID = 0
		w.curCancel = nil
	}
	w.mu.Unlock()
}

// settleCanceled writes the canceled outcome for a job: it marks the job
// canceled in the store (idempotent — a store-path Cancel may already have
// done so) and returns its video to 'new'. It is the single funnel for every
// cancel path — the flag path and every ErrNotRunning from a guarded write —
// so the job/video end state can never diverge across those paths.
func (w *Worker) settleCanceled(job *jobs.Job, video *videos.Video) {
	if _, err := w.deps.Jobs.Cancel(job.ID); err != nil {
		w.deps.Logger.Error("download worker: mark canceled failed", "job_id", job.ID, "err", err)
	}
	if video != nil {
		if err := w.deps.Videos.SetStatus(video.ID, videos.StatusNew, ""); err != nil {
			w.deps.Logger.Error("download worker: reset video status failed", "video_id", video.ID, "err", err)
		}
	}
}

// classify maps a real download error to an outcome. countFail gates whether an
// unclassified/retryable error feeds the auto-pause FailMonitor: true for a real
// download failure (the signal the breaker exists for), false for the metadata
// preflight, where a single freshly-added URL's transient blip must not nudge
// the global breaker. Cookie/blocked errors pause regardless (they never touch
// the monitor); terminal errors fail regardless.
func (w *Worker) classify(ctx context.Context, job *jobs.Job, video *videos.Video, err error, countFail bool) {
	var terminal *ytdlp.TerminalError
	switch {
	case errors.Is(err, ytdlp.ErrBlocked):
		w.pause(job, settings.CookieBlocked, err.Error())
	case errors.Is(err, ytdlp.ErrCookieExpired):
		w.pause(job, settings.CookieStale, err.Error())
	case errors.Is(err, ytdlp.ErrNoCookie):
		// No cookie at all: pausing (rather than failing the job) lets the
		// user paste a cookie and resume without losing the queue. Cookie
		// status is already 'absent'; leave it.
		w.pause(job, "", err.Error())
	case errors.Is(err, ytdlp.ErrPaused):
		// Kill-switch tripped mid-download: requeue without burning an
		// attempt and WITHOUT the cookie-pause flag — the loop's
		// YoutubePaused gate parks it next iteration; a resume clears the
		// flag and it proceeds.
		w.requeuePaused(job)
	case errors.As(err, &terminal):
		// Terminal ytdlp error: fail without changing the attempt count. The
		// reason travels with it so fail can park the video in the scan ledger
		// rather than stranding it as an un-retryable Library row.
		w.fail(job, video, job.Attempts, err.Error(), terminal.Reason)
	default:
		// Count-worthy (unclassified exec/extractor + RetryableError) for
		// auto-pause; per-video terminal errors above never reach here. A
		// preflight failure (countFail=false) still retries but does not feed
		// the breaker.
		if countFail && w.deps.FailMonitor != nil {
			w.deps.FailMonitor.Fail(video.ID)
		}
		// RetryableError and any unexpected error (network, exec) get the
		// bounded retry treatment.
		w.retry(ctx, job, video, err.Error())
	}
}

// pause requeues the job without burning an attempt, flips cookie_status
// (when status != ""), and pauses the loop so nothing else is claimed until
// Resume is called.
func (w *Worker) pause(job *jobs.Job, cookieStatus, msg string) {
	if cookieStatus != "" {
		if err := w.deps.Settings.SetCookie(context.Background(), "", cookieStatus); err != nil {
			w.deps.Logger.Error("download worker: set cookie status failed", "status", cookieStatus, "err", err)
		}
	}
	// Finding 5 (lost-wakeup ordering): set the paused flag BEFORE the requeue
	// write, so a Resume() arriving between the write and the flag-set is not
	// lost (Resume only wakes the loop when it observes paused == true).
	w.mu.Lock()
	w.paused = true
	w.mu.Unlock()
	// Same attempts count: a pause is not the job's fault.
	switch err := w.deps.Jobs.Bump(job.ID, job.Attempts, msg); {
	case errors.Is(err, jobs.ErrNotRunning):
		// Canceled out from under us: nothing to requeue, but the pause (a
		// cookie-state signal, not about this job) still stands.
	case err != nil:
		w.deps.Logger.Error("download worker: requeue on pause failed", "job_id", job.ID, "err", err)
	}
	w.deps.Logger.Warn("download worker: paused", "reason", msg)
}

// requeuePaused requeues a job that hit the youtube_paused kill-switch: same
// attempts (a pause is not the job's fault), no cookie flip, no in-memory
// paused flag. The loop's YoutubePaused gate does the parking.
func (w *Worker) requeuePaused(job *jobs.Job) {
	switch err := w.deps.Jobs.Bump(job.ID, job.Attempts, "youtube paused"); {
	case errors.Is(err, jobs.ErrNotRunning):
	case err != nil:
		w.deps.Logger.Error("download worker: requeue on youtube-pause failed", "job_id", job.ID, "err", err)
	}
}

// retry either requeues the job after backoff (attempts++), or, once the
// per-job max is reached, fails it terminally.
func (w *Worker) retry(ctx context.Context, job *jobs.Job, video *videos.Video, msg string) {
	newAttempts := job.Attempts + 1
	if newAttempts >= job.MaxAttempts {
		// Terminal failure: record the final attempt count AND fail in one
		// guarded write (via fail → Jobs.Fail), so there is no intermediate
		// 'pending' window in which another claimer could grab a job that is
		// about to be failed (finding 3).
		//
		// No gate reason: exhausting the retries means something transient kept
		// going wrong, which is exactly the case the Library's re-download
		// button exists for. Parking it would take that away.
		w.fail(job, video, newAttempts, msg, "")
		return
	}
	// Record the attempt, then wait out the backoff before it can be
	// reclaimed. (The job is already pending after Bump, but the same
	// goroutine won't reclaim it until this returns.)
	switch err := w.deps.Jobs.Bump(job.ID, newAttempts, msg); {
	case errors.Is(err, jobs.ErrNotRunning):
		// Canceled out from under us: settle as canceled and do not requeue.
		w.settleCanceled(job, video)
		return
	case err != nil:
		w.deps.Logger.Error("download worker: bump failed", "job_id", job.ID, "err", err)
	}
	w.sleep(ctx, w.deps.Backoff(newAttempts))
}

// succeed persists a finished download and marks the job done. The job's 'done'
// write is attempted FIRST and is guarded (state = 'running'): if a Cancel
// raced in after our cancel-flag read — taking the store path and marking the
// row canceled — Finish returns ErrNotRunning and we settle as canceled
// instead of persisting the download, so a canceled job is never resurrected
// to done.
func (w *Worker) succeed(job *jobs.Job, video *videos.Video, res *ytdlp.Result) {
	switch err := w.deps.Jobs.Finish(job.ID, jobs.StateDone, "", ""); {
	case errors.Is(err, jobs.ErrNotRunning):
		w.settleCanceled(job, video)
		return
	case err != nil:
		w.deps.Logger.Error("download worker: finish done failed", "job_id", job.ID, "err", err)
		return
	}
	if err := w.deps.Videos.SetDownloaded(video.ID, videos.DownloadedResult{
		MediaPath:            res.MediaPath,
		ThumbnailPath:        res.ThumbnailPath,
		FilesizeBytes:        res.FilesizeBytes,
		FormatUsed:           res.FormatUsed,
		SponsorblockSegments: marshalSegments(res.SponsorblockSegments),
		SubtitleRelPath:      res.SubtitleRelPath,
		AudioLanguage:        res.AudioLanguage,
		ChaptersJSON:         res.ChaptersJSON,
		PublishedAt:          res.PublishedAt,
		Description:          res.Description,
		MediaType:            res.MediaType,
		LiveStatus:           res.LiveStatus,
		YTTags:               marshalStrings(res.Tags),
		YTCategories:         marshalStrings(res.Categories),
	}); err != nil {
		w.deps.Logger.Error("download worker: set downloaded failed", "video_id", video.ID, "err", err)
		// Do not enqueue a summary job: the video row was not updated with
		// subtitle_path/audio_language/chapters, so a summary job would run
		// against stale/incomplete data.
		return
	}

	// Take the transcript into the database and drop every .vtt beside the
	// media file. The chunk tables answer searches, but until now the .vtt was
	// the only thing they could be rebuilt from — which is why a tombstone had
	// to spare it by name. In a row it cannot be swept by accident.
	w.storeTranscript(video.ID, res.SubtitleRelPath, res.MediaPath)

	// Take the poster into the database. yt-dlp wrote it to disk beside the
	// media file; from here on that file is only an import source, and the bytes
	// in video_thumbnails are what every card and player renders (migration
	// 0022). Best-effort and never gating: a video with no poster is a cosmetic
	// loss, and the import worker retries this on its own schedule.
	w.storeThumbnail(video.ID, res.ThumbnailPath)

	// Probe the finished file so the player can show what it actually is.
	// Deliberately after SetDownloaded and never gating anything below: the
	// media facts are decoration, and a missing or broken ffprobe must not
	// cost the user a summary.
	// The caption peeq fetched to help decide on this video has been superseded:
	// SetDownloaded above repointed subtitle_path at the copy that came with the
	// media, so the .summaries/ one is now referenced by nothing. Nothing else
	// would ever collect it — retention works from database rows, and no row
	// points here any more — so it has to go on this path or not at all.
	//
	// Best-effort and unconditional: the directory is absent for every video
	// that was downloaded without being read first, which is the ordinary case,
	// and RemoveAll is happy either way.
	if w.deps.MediaDir != "" {
		if safe, err := media.SafeMediaPath(w.deps.MediaDir, filepath.Join(ytdlp.SummaryDirName, video.ID)); err == nil {
			_ = os.RemoveAll(safe)
		}
	}

	w.probeDownloaded(video.ID, res.MediaPath)

	// Enqueue a summary job as a downstream consequence of every successful
	// download (initial or re-download). SummaryJobs is nil in tests that
	// don't care about summaries; production always sets it. Only reached
	// when SetDownloaded above succeeded.
	if w.deps.SummaryJobs != nil {
		if _, err := w.deps.SummaryJobs.Enqueue(video.ID); err != nil {
			w.deps.Logger.Error("download worker: enqueue summary job failed", "video_id", video.ID, "err", err)
		}
	}
	if w.deps.FailMonitor != nil {
		w.deps.FailMonitor.Reset()
	}
	w.recordActivity(activity.Event{
		Kind: activity.KindDownload, Outcome: activity.OutcomeOK,
		SubjectID: video.ID, Subject: video.Title, Summary: "downloaded",
		Detail: humanSize(res.FilesizeBytes),
	})
}

// probeProbeTimeout bounds the inline probe of a just-finished download.
// Deliberately short: this call sits inside the single-concurrency download
// loop, before the job is settled, so the timeout is also the worst case for
// how long a wedged ffprobe can stop the queue claiming work. The file is
// local and already written, so a healthy probe takes milliseconds; anything
// slower than this is left to the backfill sweep, which blocks nothing.
const probeProbeTimeout = 5 * time.Second

// probeDownloaded reads the finished file's media facts and stores them.
// Nothing here is fatal.
//
// A failure writes NOTHING — unlike the backfill sweep, which stores a zero
// result to stamp probed_at and stop retrying. The two differ because their
// starting points differ: the sweep only ever sees rows with no values, so a
// zero write loses nothing, while this runs after re-downloads too, where the
// row may already hold good facts from an earlier probe. Overwriting those
// with blanks on a transient ffprobe failure would also stamp probed_at,
// which is exactly what stops the sweep from ever repairing the row.
//
// Writing nothing leaves probed_at NULL on a first download, so the sweep
// picks the video up and retries — and leaves the previous values intact on a
// re-download. Both are the recoverable outcome.
func (w *Worker) probeDownloaded(videoID, mediaPath string) {
	if w.deps.Prober == nil || mediaPath == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeProbeTimeout)
	defer cancel()

	info, err := w.deps.Prober.Probe(ctx, mediaPath)
	if err != nil {
		w.deps.Logger.Warn("download worker: probe failed; leaving it to the backfill sweep",
			"video_id", videoID, "err", err)
		return
	}
	if err := w.deps.Videos.SetProbed(videoID, mediaprobe.StoreResult(info)); err != nil {
		w.deps.Logger.Error("download worker: store probe failed", "video_id", videoID, "err", err)
	}
}

// storeTranscript reads the .vtt yt-dlp wrote into the row, then unlinks every
// .vtt beside the media file.
//
// All of them, not just the one subtitle_path names: yt-dlp may write several
// language and auto-caption variants, and both globs that look for them take
// the FIRST match, so the rest were already unreferenced. Best-effort at every
// step — the download succeeded either way, and the import worker retries a
// transcript that did not land.
func (w *Worker) storeTranscript(videoID, subtitleRelPath, mediaPath string) {
	if w.deps.Videos == nil {
		return
	}
	if subtitleRelPath != "" {
		if safe, err := media.SafeMediaPath(w.deps.MediaDir, subtitleRelPath); err == nil {
			if data, rerr := os.ReadFile(safe); rerr == nil {
				if serr := w.deps.Videos.SetTranscript(videoID, videos.TranscriptSourceDownload, string(data)); serr != nil {
					w.deps.Logger.Warn("download worker: store transcript failed", "video_id", videoID, "err", serr)
					return
				}
			} else {
				w.deps.Logger.Warn("download worker: read transcript failed", "video_id", videoID, "err", rerr)
				return
			}
		}
	}
	if mediaPath == "" {
		return
	}
	if safe, err := media.SafeMediaPath(w.deps.MediaDir, mediaPath); err == nil {
		media.RemoveSubtitleSidecars(safe)
	}
}

// storeThumbnail reads the poster yt-dlp wrote and stores its bytes on the
// video row. Best-effort at every step: no poster, an unreadable file, an
// oversized image or a failed insert are all logged and shrugged off — the
// download itself succeeded, and the thumbimport worker will retry the import
// on its next pass since the row still has no stored poster.
func (w *Worker) storeThumbnail(videoID, thumbPath string) {
	if thumbPath == "" || w.deps.Videos == nil {
		return
	}
	safe, err := media.SafeMediaPath(w.deps.MediaDir, thumbPath)
	if err != nil {
		w.deps.Logger.Warn("download worker: thumbnail path rejected", "video_id", videoID, "err", err)
		return
	}
	data, err := os.ReadFile(safe)
	if err != nil {
		w.deps.Logger.Warn("download worker: read thumbnail failed", "video_id", videoID, "err", err)
		return
	}
	if err := w.deps.Videos.SetThumbnail(videoID, media.ThumbnailMime(safe), data); err != nil {
		w.deps.Logger.Warn("download worker: store thumbnail failed", "video_id", videoID, "err", err)
		return
	}
	// The file has served its purpose. Removing it only after a successful
	// store is what makes the failure mode "retry the import later" rather than
	// "the poster is gone".
	_ = os.Remove(safe)
}

// recordActivity records a download event for the Activity feed, nil-safe.
func (w *Worker) recordActivity(e activity.Event) {
	if w.deps.Activity != nil {
		w.deps.Activity.Record(e)
	}
}

// humanSize renders a byte count as a compact KB/MB/GB string for a download's
// activity detail. A sub-megabyte file must not read as "0 MB" (that looks like
// an empty/failed download), so it falls through to KB. Zero bytes (yt-dlp did
// not report a size) yields "".
func humanSize(b int64) string {
	switch {
	case b <= 0:
		return ""
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%.0f KB", float64(b)/(1<<10))
	}
}

// fail marks both the job and its video terminally failed, recording attempts
// in the same guarded write. If the job was canceled out from under us
// (ErrNotRunning), it settles as canceled instead — leaving the video in 'new'
// rather than 'error'.
//
// gateReason is the ytdlp.TerminalError reason when the download failed
// because the video is walled off from us (members / age / geo / private /
// deleted), and "" for every other failure. It selects between the two very
// different meanings of "failed": a retryable thing that went wrong, versus a
// video peeq is not allowed to have. See park.
func (w *Worker) fail(job *jobs.Job, video *videos.Video, attempts int, msg string, gateReason string) {
	// A download that simply fails logged NOTHING at any level before this:
	// every other Logger call on this path fires only when a database write
	// fails, so the container log was silent about the failure itself and the
	// job row was the only trace. One line per terminal failure is cheap and
	// makes the log the first place to look, matching what pause() already does.
	w.deps.Logger.Warn("download worker: download failed",
		"video_id", videoID(video), "job_id", job.ID, "attempts", attempts,
		"gate", gateReason, "err", msg)
	switch err := w.deps.Jobs.Fail(job.ID, attempts, msg); {
	case errors.Is(err, jobs.ErrNotRunning):
		w.settleCanceled(job, video)
		return
	case err != nil:
		w.deps.Logger.Error("download worker: finish failed", "job_id", job.ID, "err", err)
	}
	if gateReason != "" && w.park(video, gateReason) {
		return
	}
	if video != nil {
		if err := w.deps.Videos.SetStatus(video.ID, videos.StatusError, msg); err != nil {
			w.deps.Logger.Error("download worker: set error status failed", "video_id", video.ID, "err", err)
		}
		// A preflight-failed video has no title yet, so fall back to its id
		// rather than emitting a blank, unidentifiable Activity row.
		subject := video.Title
		if subject == "" {
			subject = video.ID
		}
		w.recordActivity(activity.Event{
			Kind: activity.KindDownload, Outcome: activity.OutcomeFail,
			SubjectID: video.ID, Subject: subject, Summary: "download failed",
			Detail: msg,
		})
	}
}

// videoID is video.ID, or "" when the video could not be loaded at all — so
// the log line above can name the video without a nil check at the call site.
func videoID(video *videos.Video) string {
	if video == nil {
		return ""
	}
	return video.ID
}

// park hands a walled-off video back to the scan ledger and removes its videos
// row, reporting whether it did so.
//
// The Inbox's Download button flips the ledger row to 'queued' at CLICK time,
// before yt-dlp has run, and nothing ever writes 'pending' back. So without
// this, a members-only video was gone from the Inbox AND sitting in the
// Library as an 'error' row whose re-download button could never succeed —
// two wrong places at once, and no way for the user to be offered it again if
// the channel later made it public.
//
// Parking the LEDGER row (not the videos row) is what makes the memory
// re-checkable: channelvideos.StateUnavailable is revisited by every scan
// pass, so a lifted gate returns the video to the Inbox on its own.
//
// It returns false — leaving the ordinary 'error' path to run — whenever there
// is no ledger row to park: a video added by URL by hand has no scan ledger
// behind it, and discarding its row would erase the only record that the user
// ever asked for it. That row stays in the Library, which for a hand-added
// video is the honest place for it.
func (w *Worker) park(video *videos.Video, reason string) bool {
	if video == nil || w.deps.Ledger == nil {
		return false
	}
	// Never discard a video that has ever finished downloading. Re-download is
	// offered for error AND tombstoned rows (handleRedownloadVideo), so a
	// channel that gates a previously-public video turns one click into a
	// terminal 'members' failure on a row holding watch history, a resume
	// position, favorites, a summary, transcript chunks, share links and a
	// thumbnail file on disk. Discarding that is silent data loss — and the
	// file would be orphaned besides, since nothing here unlinks it.
	//
	// DownloadedAt is the signal rather than status or media_path: it is
	// stamped once on the first success and never cleared, so it stays true
	// through the tombstone that clears media_path and through the 'error'
	// status a failed re-download writes. Such a row falls through to the plain
	// error path, which is right — it stays visible and keeps offering the
	// re-download that will work again if the channel ever ungates it.
	if video.DownloadedAt != "" {
		return false
	}
	row, err := w.deps.Ledger.Get(video.ID)
	if err != nil {
		w.deps.Logger.Error("download worker: ledger lookup failed", "video_id", video.ID, "err", err)
		return false
	}
	if row == nil {
		return false
	}
	if err := w.deps.Ledger.SetUnavailable(video.ID, reason); err != nil {
		w.deps.Logger.Error("download worker: park unavailable failed", "video_id", video.ID, "err", err)
		return false
	}
	// Best title wins, then the id. The ledger's title is preferred over the
	// videos row's because a gated video's metadata preflight fails the same
	// way its download does, so the videos row is usually still blank here
	// while the ledger carries what the channel listing said.
	subject := firstNonEmpty(row.Title, video.Title, video.ID)
	w.recordActivity(activity.Event{
		Kind: activity.KindDownload, Outcome: activity.OutcomeFail,
		SubjectID: video.ID, Subject: subject, Summary: "not available",
		Detail: gateDetail(reason),
	})
	// A failed discard is not worth undoing the park: the row is already
	// recorded as unavailable, and a stale 'error' row in the Library is a
	// cosmetic problem next to losing the ledger memory. Log and move on.
	if err := w.deps.Videos.Discard(video.ID); err != nil {
		w.deps.Logger.Error("download worker: discard video failed", "video_id", video.ID, "err", err)
	}
	return true
}

// firstNonEmpty returns the first non-empty string, or "" if there is none.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// gateDetail renders a ytdlp.TerminalError reason as the Activity feed's
// one-line explanation. The raw reason words ("members", "geo") are peeq's
// internal vocabulary and read as jargon in a user-facing row.
func gateDetail(reason string) string {
	switch reason {
	case "members":
		return "members-only video"
	case "age":
		return "age-restricted video"
	case "geo":
		return "not available in this region"
	case "premium":
		return "YouTube Premium video"
	case "private":
		return "private video"
	case "deleted":
		return "video was removed"
	default:
		return "not available to download"
	}
}

// segmentJSON is the stored shape of one SponsorBlock segment in the
// sponsorblock_segments TEXT column (ytdlp.Segment carries no json tags).
type segmentJSON struct {
	Category  string  `json:"category"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
}

// marshalSegments renders the download's SponsorBlock segments as the JSON
// array text stored in videos.sponsorblock_segments. It always returns a
// valid JSON array ("[]" when there are none).
func marshalSegments(segs []ytdlp.Segment) string {
	out := make([]segmentJSON, 0, len(segs))
	for _, s := range segs {
		out = append(out, segmentJSON{Category: s.Category, StartTime: s.StartTime, EndTime: s.EndTime})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// marshalStrings renders yt-dlp's tags/categories as the JSON array text
// stored in videos.yt_tags / videos.yt_categories, matching how
// marshalSegments stores segments.
//
// It returns "" — not "[]" — for an empty list, because SetDownloaded treats
// empty as "leave what is there". "[]" would be a value, and a re-download of
// a video whose extractor happened to omit tags would wipe the ones already
// stored.
func marshalStrings(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	b, err := json.Marshal(vals)
	if err != nil {
		return ""
	}
	return string(b)
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
