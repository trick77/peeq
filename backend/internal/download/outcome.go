package download

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

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
func (w *Worker) classify(job *jobs.Job, video *videos.Video, err error, countFail bool) {
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
		// bounded retry treatment. A RetryableError is YouTube's answer (429 or
		// 5xx), which the next job would get too, so it also cools the loop.
		var retryable *ytdlp.RetryableError
		w.retry(job, video, err.Error(), errors.As(err, &retryable))
	}
}

// coolDown stops the loop from claiming for d, on top of the failed job's own
// row stamp. Extending, never shortening: two failures in a row keep the later
// deadline.
func (w *Worker) coolDown(d time.Duration) {
	if d <= 0 {
		return
	}
	until := time.Now().Add(d)
	w.mu.Lock()
	if until.After(w.cooldownUntil) {
		w.cooldownUntil = until
	}
	w.mu.Unlock()
}

// cooldownRemaining reports how much of a YouTube-side cool-off is left, or
// zero when the loop may claim.
func (w *Worker) cooldownRemaining() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return time.Until(w.cooldownUntil)
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

// retry records a failed attempt and requeues the job, or fails it on the
// last attempt. The wait before the job can be claimed again is a stamp on
// its row (jobs.Store.BumpAfter), so this goroutine goes straight on to the
// next claimable job instead of sleeping the backoff with the whole queue
// behind it — which is what it used to do, up to five minutes at a time.
//
// hostSide says the failure was YouTube's answer (a 429 or 5xx) rather than
// this job's own fault (a watchdog timeout, a failed persist): then the next
// job would only get the same answer, so the loop cools down for the same
// duration as well. That keeps the old queue-wide breather exactly where it
// protected something, and drops it where it only stalled unrelated work.
func (w *Worker) retry(job *jobs.Job, video *videos.Video, msg string, hostSide bool) {
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
	backoff := w.deps.Backoff(newAttempts)
	switch err := w.deps.Jobs.BumpAfter(job.ID, newAttempts, msg, backoff); {
	case errors.Is(err, jobs.ErrNotRunning):
		// Canceled out from under us: settle as canceled and do not requeue.
		w.settleCanceled(job, video)
		return
	case err != nil:
		w.deps.Logger.Error("download worker: bump failed", "job_id", job.ID, "err", err)
	}
	if hostSide {
		w.coolDown(backoff)
	}
}

// succeed persists a finished download and marks the job done — in ONE
// transaction. The job's 'done' write is guarded (state = 'running'): if a
// Cancel raced in after our cancel-flag read — taking the store path and
// marking the row canceled — it reports ErrNotRunning, nothing is committed,
// and we settle as canceled instead of persisting the download, so a canceled
// job is never resurrected to done.
//
// One transaction because the two writes used to be separate, and a failed
// second one left the job 'done' with the video stuck in 'downloading' — a
// state nothing revisits: EnqueueMissing wants 'downloaded', the Library's
// re-download button wants 'error'. Now both land or neither does. When
// neither does, the job is still 'running', so it goes through the ordinary
// retry ladder: the file just written is removed (the row does not point at
// it, and a retry re-downloads into the same place), and the last attempt
// leaves the video in 'error' where the user can retry it by hand.
func (w *Worker) succeed(ctx context.Context, job *jobs.Job, video *videos.Video, res *ytdlp.Result) {
	switch err := w.complete(ctx, job, video, res); {
	case errors.Is(err, jobs.ErrNotRunning):
		w.settleCanceled(job, video)
		return
	case err != nil:
		w.deps.Logger.Error("download worker: persist download failed", "job_id", job.ID, "video_id", video.ID, "err", err)
		media.RemoveVideoFiles(w.deps.MediaDir, res.MediaPath)
		w.retry(job, video, err.Error(), false)
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
	// loss, and never worth failing a download that otherwise succeeded. With
	// the import worker retired (0024) nothing retries it, so the file is left
	// where it is on a failure rather than unlinked.
	w.storeThumbnail(video.ID, res.ThumbnailPath)

	// Probe the finished file so the player can show what it actually is.
	// Deliberately after SetDownloaded and never gating anything below: the
	// media facts are decoration, and a missing or broken ffprobe must not
	// cost the user a summary.
	// The caption peeq fetched to help decide on this video has been superseded:
	// storeTranscript above overwrote the stored text with the copy that came
	// with the media, so the .summaries/ file is referenced by nothing. Nothing else
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
	activity.Record(w.deps.Activity, activity.Event{
		Kind: activity.KindDownload, Outcome: activity.OutcomeOK,
		SubjectID: video.ID, Subject: firstNonEmpty(video.Title, video.ID), Summary: "downloaded",
		Detail: humanSize(res.FilesizeBytes),
	})
}

// complete commits the job's 'done' and the video's 'downloaded' as one unit.
// Both stores share w.deps.DB, so a transaction on it covers both writes.
func (w *Worker) complete(ctx context.Context, job *jobs.Job, video *videos.Video, res *ytdlp.Result) error {
	tx, err := w.deps.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := w.deps.Jobs.FinishIn(ctx, tx, job.ID, jobs.StateDone, "", ""); err != nil {
		return err
	}
	if err := w.deps.Videos.SetDownloadedIn(ctx, tx, video.ID, videos.DownloadedResult{
		MediaPath:            res.MediaPath,
		FilesizeBytes:        res.FilesizeBytes,
		FormatUsed:           res.FormatUsed,
		SponsorblockSegments: marshalSegments(res.SponsorblockSegments),
		AudioLanguage:        res.AudioLanguage,
		ChaptersJSON:         res.ChaptersJSON,
		PublishedAt:          res.PublishedAt,
		Description:          res.Description,
		MediaType:            res.MediaType,
		LiveStatus:           res.LiveStatus,
		YTTags:               marshalStrings(res.Tags),
		YTCategories:         marshalStrings(res.Categories),
	}); err != nil {
		return err
	}
	return tx.Commit()
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
		activity.Record(w.deps.Activity, activity.Event{
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
	activity.Record(w.deps.Activity, activity.Event{
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
