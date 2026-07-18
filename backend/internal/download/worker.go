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
	"log/slog"
	"sync"
	"time"

	"github.com/trick77/vark/internal/jobs"
	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/videos"
	"github.com/trick77/vark/internal/ytdlp"
)

// Runner is the subset of *ytdlp.Runner the worker needs. Declaring it here
// (rather than importing the concrete type) keeps the worker testable with
// a fake that never shells out to yt-dlp; the real *ytdlp.Runner satisfies
// it.
type Runner interface {
	Download(ctx context.Context, req ytdlp.DownloadReq, onProgress func(ytdlp.Progress)) (*ytdlp.Result, error)
}

// Deps are the worker's collaborators and tunables. The stores and Runner
// are required; the rest have safe defaults applied in New.
type Deps struct {
	Jobs     *jobs.Store
	Videos   *videos.Store
	Settings *settings.Store
	Runner   Runner

	// Watchdog is the inactivity timeout: if a running download produces no
	// progress for this long, its context is cancelled (killing the child)
	// and the job is retried. Zero selects the 10-minute default; a negative
	// value disables the watchdog entirely.
	Watchdog time.Duration
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
}

// Worker is the download loop. Construct with New and drive with Run; other
// goroutines (the API layer) may call Cancel and Resume concurrently.
type Worker struct {
	deps Deps

	mu       sync.Mutex
	paused   bool
	resumeCh chan struct{}
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
	if deps.Backoff == nil {
		deps.Backoff = defaultBackoff
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
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
		w.fail(job, video, job.Attempts, "load video: "+verr.Error())
		return
	}
	if video == nil {
		w.fail(job, nil, job.Attempts, "video row missing")
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

	_ = w.deps.Videos.SetStatus(video.ID, "downloading", "")

	req := ytdlp.DownloadReq{
		URL:          video.URL,
		VideoID:      video.ID,
		Format:       set.FormatPreset,
		CustomFormat: set.FormatCustom,
		LimitRate:    set.LimitRate,
	}

	// Inactivity watchdog: reset on every progress update; if it fires, it
	// cancels jobCtx (killing the child), which surfaces as a retry below.
	var watchdog *time.Timer
	if w.deps.Watchdog > 0 {
		watchdog = time.AfterFunc(w.deps.Watchdog, cancel)
	}
	onProgress := func(p ytdlp.Progress) {
		if watchdog != nil {
			watchdog.Reset(w.deps.Watchdog)
		}
		if w.deps.OnProgress != nil {
			w.deps.OnProgress(job.ID, p)
		}
	}

	res, dlErr := w.deps.Runner.Download(jobCtx, req, onProgress)

	if watchdog != nil {
		watchdog.Stop()
	}
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
		w.classify(ctx, job, video, dlErr)
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
	if err := w.deps.Jobs.Cancel(job.ID); err != nil {
		w.deps.Logger.Error("download worker: mark canceled failed", "job_id", job.ID, "err", err)
	}
	if video != nil {
		if err := w.deps.Videos.SetStatus(video.ID, "new", ""); err != nil {
			w.deps.Logger.Error("download worker: reset video status failed", "video_id", video.ID, "err", err)
		}
	}
}

// classify maps a real download error to an outcome.
func (w *Worker) classify(ctx context.Context, job *jobs.Job, video *videos.Video, err error) {
	var terminal *ytdlp.TerminalError
	switch {
	case errors.Is(err, ytdlp.ErrBlocked):
		w.pause(job, "blocked", err.Error())
	case errors.Is(err, ytdlp.ErrCookieExpired):
		w.pause(job, "stale", err.Error())
	case errors.Is(err, ytdlp.ErrNoCookie):
		// No cookie at all: pausing (rather than failing the job) lets the
		// user paste a cookie and resume without losing the queue. Cookie
		// status is already 'absent'; leave it.
		w.pause(job, "", err.Error())
	case errors.As(err, &terminal):
		// Terminal ytdlp error: fail without changing the attempt count.
		w.fail(job, video, job.Attempts, err.Error())
	default:
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

// retry either requeues the job after backoff (attempts++), or, once the
// per-job max is reached, fails it terminally.
func (w *Worker) retry(ctx context.Context, job *jobs.Job, video *videos.Video, msg string) {
	newAttempts := job.Attempts + 1
	if newAttempts >= job.MaxAttempts {
		// Terminal failure: record the final attempt count AND fail in one
		// guarded write (via fail → Jobs.Fail), so there is no intermediate
		// 'pending' window in which another claimer could grab a job that is
		// about to be failed (finding 3).
		w.fail(job, video, newAttempts, msg)
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
	switch err := w.deps.Jobs.Finish(job.ID, "done", "", ""); {
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
	}); err != nil {
		w.deps.Logger.Error("download worker: set downloaded failed", "video_id", video.ID, "err", err)
	}
}

// fail marks both the job and its video terminally failed, recording attempts
// in the same guarded write. If the job was canceled out from under us
// (ErrNotRunning), it settles as canceled instead — leaving the video in 'new'
// rather than 'error'.
func (w *Worker) fail(job *jobs.Job, video *videos.Video, attempts int, msg string) {
	switch err := w.deps.Jobs.Fail(job.ID, attempts, msg); {
	case errors.Is(err, jobs.ErrNotRunning):
		w.settleCanceled(job, video)
		return
	case err != nil:
		w.deps.Logger.Error("download worker: finish failed", "job_id", job.ID, "err", err)
	}
	if video != nil {
		if err := w.deps.Videos.SetStatus(video.ID, "error", msg); err != nil {
			w.deps.Logger.Error("download worker: set error status failed", "video_id", video.ID, "err", err)
		}
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

// Cancel stops job jobID. If it is the running job — registered from the
// moment it is claimed, through preflight and the download — the cancel flag
// is set and its context is cancelled (killing any child), and the worker's
// completion path writes the canceled state; the single-writer rule avoids a
// double write racing the loop. Otherwise (a merely pending job, or the tiny
// window after the worker has cleared its registration but before its guarded
// terminal write) the job is canceled directly in the store, where the
// state = 'running' guard on Finish/Bump/Fail stops that write from
// resurrecting the now-canceled row.
func (w *Worker) Cancel(jobID int64) {
	w.mu.Lock()
	if jobID == w.curJobID && w.curCancel != nil {
		w.cancelRequested = true
		cancel := w.curCancel
		w.mu.Unlock()
		// Kill the child; the loop's completion path is the single writer
		// that will mark the job canceled once Download returns.
		cancel()
		return
	}
	w.mu.Unlock()
	if err := w.deps.Jobs.Cancel(jobID); err != nil {
		w.deps.Logger.Error("download worker: cancel pending job failed", "job_id", jobID, "err", err)
	}
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
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
