package download

import (
	"context"
	"errors"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

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
		// Spaced by a poll interval so a persistent settings fault does not
		// spin the loop re-claiming the same job with no wait in between.
		w.deps.Logger.Error("download worker: load settings failed", "job_id", job.ID, "err", serr)
		switch err := w.deps.Jobs.BumpAfter(job.ID, job.Attempts, "load settings: "+serr.Error(), w.deps.PollInterval); {
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
		//
		// capFired is how the cap is told apart from any other error: the
		// message the user sees on Activity should say the probe stalled, not
		// repeat a bare "context canceled". See ytdlp.CallWithCap.
		var meta *ytdlp.Meta
		capFired, merr := ytdlp.CallWithCap(jobCtx, w.deps.MetadataTimeout, func(c context.Context) error {
			var err error
			meta, err = w.deps.Runner.Metadata(c, video.URL)
			return err
		})
		if w.wasCanceled() {
			w.settleCanceled(job, video)
			return
		}
		if ctx.Err() != nil {
			// Parent shutdown mid-preflight: the same rule as the download path
			// below — leave the job 'running' for the next boot's ResetOrphans
			// and write nothing. Classifying the context error would burn an
			// attempt, and on the last one fail the video, for a restart.
			return
		}
		if capFired {
			w.retry(job, video, "metadata preflight timeout: no progress", false)
			return
		}
		if merr != nil {
			// countFail=false: a preflight blip for one URL must not nudge the
			// global auto-pause breaker. Cookie/blocked still pause; unavailable
			// still fails.
			w.classify(job, video, merr, false)
			return
		}
		video.Title = meta.Title
		video.ChannelID = meta.ChannelID
		video.ChannelName = meta.Channel
		video.DurationSeconds = int64(meta.DurationSeconds)
		video.PublishedAt = meta.PublishedAt
		video.Description = meta.Description
		// meta.Thumbnail is deliberately dropped on the floor. It is a REMOTE
		// CDN url, and the only thing that ever read it back was the import
		// worker's fallback; the poster this video ends up showing is the file
		// yt-dlp writes at download time, stored as bytes by storeThumbnail.
		video.Availability = videos.NormalizeAvailability(meta.Availability)
		if err := w.deps.Videos.Upsert(*video); err != nil {
			// Retry, don't fail: a write that could not land is our
			// infrastructure's problem, not the video's — the same reasoning as
			// the settings-load requeue above. A transient SQLITE_BUSY under a
			// concurrent writer must not park the video in 'error' and make the
			// user re-add it by hand.
			w.deps.Logger.Error("download worker: save metadata failed", "job_id", job.ID, "err", err)
			w.retry(job, video, "save metadata: "+err.Error(), false)
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
		w.succeed(ctx, job, video, res)
	case ctxInterrupted:
		// Not a user cancel, not shutdown, yet the context was cancelled →
		// the watchdog fired. Treat as a retryable timeout.
		w.retry(job, video, "watchdog timeout: no progress", false)
	default:
		w.classify(job, video, dlErr, true)
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
