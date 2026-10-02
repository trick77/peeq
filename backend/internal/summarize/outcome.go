package summarize

import (
	"context"
	"fmt"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
)

// finishNoTranscript closes out a video that has nothing to summarize. It is a
// clean terminal state, not an error, but it must still be visible: otherwise
// a video simply disappears from the queue with no explanation.
func (w *Worker) finishNoTranscript(job *summaryjobs.Job, video *videos.Video, reason string) {
	_ = w.d.Videos.SetSummaryStatus(video.ID, videos.SummaryNoTranscript, "")
	w.emit(video.ID, videos.SummaryNoTranscript, "")
	_ = w.d.Jobs.Finish(job.ID, summaryjobs.StateDone, "")
	w.d.Logger.Info("summarize worker: no transcript", "video_id", video.ID, "title", video.Title,
		"channel", video.ChannelName, "reason", reason)
}

// failJob records the failure on both the video and the job, and always
// returns a non-nil error so the caller (processOne) surfaces the failure
// to the Run loop, which logs it. Jobs.Fail's own return is often nil on
// the common path, so it must never be returned as-is.
func (w *Worker) failJob(ctx context.Context, job *summaryjobs.Job, video *videos.Video, run *analysisRun, msg string) error {
	if err := w.interrupted(ctx, job, run); err != nil {
		return err
	}
	videoID := video.ID
	if err := w.d.Videos.SetSummaryStatus(videoID, videos.SummaryError, msg); err != nil {
		w.d.Logger.Error("summarize worker: set error status", "video_id", videoID, "err", err)
	}
	w.emit(videoID, videos.SummaryError, "")
	run.finished("error")
	terminal, ferr := w.d.Jobs.Fail(job.ID, job.Attempts, msg)
	if ferr != nil {
		return fmt.Errorf("summarize job %d failed (%s); also fail-record error: %w", job.ID, msg, ferr)
	}
	// Record an Activity row only when the job is genuinely terminal (moved to
	// 'failed'). Most failJob calls requeue to 'pending' — a retry, not news; a
	// row on every one would flood the feed.
	if terminal {
		activity.Record(w.d.Activity, activity.Event{
			Kind: activity.KindSummary, Outcome: activity.OutcomeFail,
			SubjectID: video.ID, Subject: video.Title, Summary: "summary failed",
			Detail: msg,
		})
	}
	return fmt.Errorf("summarize job %d failed: %s", job.ID, msg)
}

// requeueJob records a retryable failure and requeues the job WITHOUT touching
// summary_status. It is used for the steps that run AFTER the summary is marked
// done — key points and embedding — where a failure must retry only that step
// and must NOT regress a usable summary to "error". If retries run out the job
// is marked failed but the video keeps the summary it has.
//
// step names which one failed, in both the log and the Activity row. Passing it
// in rather than hardcoding one is what lets embedding share this path: before,
// embedding called failJob and so reported "Summarization failed" on a video
// whose summary was finished and on screen.
func (w *Worker) requeueJob(ctx context.Context, job *summaryjobs.Job, video *videos.Video, run *analysisRun, step, msg string) error {
	if err := w.interrupted(ctx, job, run); err != nil {
		return err
	}
	// will_retry=false means Jobs.Fail is about to mark this failed for good
	// rather than requeue it — same vocabulary as the finished line.
	w.d.Logger.Warn("summarize worker: "+step+" step failed",
		append(run.ident(), "attempt", attemptLabel(job),
			"will_retry", job.Attempts < job.MaxAttempts,
			"step_duration_ms", run.stepElapsedMs(), "err", msg)...)
	run.finished(step + "_failed")
	terminal, ferr := w.d.Jobs.Fail(job.ID, job.Attempts, msg)
	if ferr != nil {
		return fmt.Errorf("summarize job %d %s failed (%s); also fail-record error: %w", job.ID, step, msg, ferr)
	}
	// One row, only once retries are genuinely exhausted — a row per retry would
	// flood the feed, which is why the terminal flag exists.
	//
	// OutcomeWarn, not OutcomeFail: the summary is finished and readable, so this
	// is not the "summary failed" event failJob records. But it does need to be
	// SOMEWHERE. A job that dies here leaves summary_status="done", drops off the
	// active queue, and is skipped by the boot sweep — so without this the video
	// reads as complete forever while its chapters, highlights or search index are
	// permanently missing, with no trace anywhere but the log.
	if terminal {
		activity.Record(w.d.Activity, activity.Event{
			Kind: activity.KindSummary, Outcome: activity.OutcomeWarn,
			SubjectID: video.ID, Subject: video.Title,
			Summary: step + " failed", Detail: msg,
		})
	}
	return fmt.Errorf("summarize job %d %s failed: %s", job.ID, step, msg)
}

// interrupted reports a process shutdown that landed mid-analysis, which is
// not this video's summary failing. Nothing terminal is written: no
// summary_status=error for the card to show, no burned attempt with its
// backoff, no Activity row. The job stays 'running' and the boot-time orphan
// sweep reclaims it, the same rule the download worker applies. The run still
// gets its terminal line: the endpoint has billed the tokens the partial
// stream spent, so they are banked against the video like a panic's are.
func (w *Worker) interrupted(ctx context.Context, job *summaryjobs.Job, run *analysisRun) error {
	if ctx.Err() == nil {
		return nil
	}
	run.finished("interrupted")
	w.d.Logger.Debug("summarize worker: interrupted by shutdown", "job_id", job.ID, "video_id", job.VideoID)
	return ctx.Err()
}
