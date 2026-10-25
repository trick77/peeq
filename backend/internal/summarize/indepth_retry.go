package summarize

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/logx"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

// inDepthBackoff is the wait after each failed attempt; the last step repeats.
// There is no last attempt: a row leaves in_depth_retry only when the text is
// written and indexed, or when the video no longer has a summary to build on.
var inDepthBackoff = []time.Duration{2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour}

// inDepthLoudAfter is the attempt from which a failure logs at Error: a video
// that has failed this often is stuck, not unlucky.
const inDepthLoudAfter = 3

func inDepthWait(attempts int) time.Duration {
	return inDepthBackoff[min(max(attempts, 1), len(inDepthBackoff))-1]
}

// deferInDepth queues (or re-queues) the video's in-depth retry after attempts
// earlier ones, keeping err as the row's last_error. Redacted: the row is read
// outside the log handler that would otherwise scrub a URL from it. When the
// write fails, retries pause for the same wait instead.
func (w *Worker) deferInDepth(id string, attempts int, err error) {
	attempts++
	wait := inDepthWait(attempts)
	if derr := w.d.Videos.DeferInDepthRetry(id, logx.RedactErr(err).Error(), attempts, w.d.Now().Add(wait)); derr != nil {
		w.d.Logger.Error("summarize worker: queue in-depth retry failed", "video_id", id, "err", derr)
		w.inDepthPausedUntil = w.d.Now().Add(wait)
	}
}

// deferInDepthFromJob is deferInDepth for a job's own in-depth step: it carries
// on from a retry already queued (a requeued job runs the step again), so its
// count and backoff keep growing instead of starting over.
func (w *Worker) deferInDepthFromJob(id string, err error) {
	attempts, aerr := w.d.Videos.InDepthRetryAttempts(id)
	if aerr != nil {
		w.d.Logger.Warn("summarize worker: read in-depth retry attempts failed", "video_id", id, "err", aerr)
	}
	w.deferInDepth(id, attempts, err)
}

// inDepthOne works one due in-depth retry: writes the text if it is still
// missing, then adds its sections to the video's index if it has one. Each
// half checks the database, so a retry after a failed embed goes straight to
// the embed without paying for the chat call again.
//
// Runs only on an idle turn, after the classify backlog, and outside the job
// path's recover, so it carries its own: the row is persisted, and a panic
// escaping here would kill the process again on every boot. did is true only
// when a chat or embed call was made, so a dropped row keeps the poll gap.
func (w *Worker) inDepthOne(ctx context.Context) (did bool, err error) {
	if w.d.Now().Before(w.inDepthPausedUntil) {
		return false, nil
	}
	id, attempts, err := w.d.Videos.NextInDepthRetry(w.d.Now())
	if err != nil || id == "" {
		return false, err
	}
	defer func() {
		if r := recover(); r != nil {
			w.d.Logger.Error("summarize worker: in-depth retry recovered", "video_id", id, "panic", r)
			w.deferInDepth(id, attempts, fmt.Errorf("panic: %v", r))
			did, err = true, nil
		}
	}()
	drop := func(reason string, attrs ...any) (bool, error) {
		w.d.Logger.Info("summarize worker: in-depth retry dropped", append([]any{"video_id", id, "reason", reason}, attrs...)...)
		return false, w.d.Videos.ClearInDepthRetry(id)
	}
	video, err := w.d.Videos.Get(id)
	if err != nil {
		w.deferInDepth(id, attempts, err)
		return false, nil
	}
	// A wiped summary means a re-analysis owns the video now; its own in-depth
	// step queues it again if that fails too.
	if video == nil {
		return drop("video gone")
	}
	if video.SummaryStatus != videos.SummaryDone || video.Summary == "" {
		return drop("no summary")
	}
	// Ignored by the user and kept only for search: not worth a call a day
	// forever (the backfill leaves these out for the same reason).
	ignored, err := w.d.Videos.IgnoredRead(id)
	if err != nil {
		w.deferInDepth(id, attempts, err)
		return false, nil
	}
	if ignored {
		return drop("ignored")
	}
	transcript, err := w.d.Videos.GetTranscript(id)
	if err != nil {
		w.deferInDepth(id, attempts, err)
		return false, nil
	}
	if transcript == nil {
		return drop("no transcript")
	}
	text, err := w.d.Videos.InDepth(id)
	if err != nil {
		w.deferInDepth(id, attempts, err)
		return false, nil
	}
	// Text already there: index it if that is still missing, else done
	// without parsing anything.
	need := false
	if text != "" {
		if need, err = w.inDepthNeedsIndex(ctx, video, transcript.Source, text); err != nil {
			w.deferInDepth(id, attempts, err)
			return false, nil
		}
		if !need {
			return false, w.d.Videos.ClearInDepthRetry(id)
		}
	}
	prep, err := w.prepareTranscript(video, transcript.VTT)
	if err != nil {
		return drop("unparsable transcript", "err", err)
	}
	if prep.empty != "" {
		return drop(prep.empty)
	}

	ident := []any{"video_id", video.ID, "title", video.Title, "channel", video.ChannelName}
	totals := &llm.Totals{}
	cctx := llm.WithTotals(llm.WithCall(ctx, llm.CallInfo{
		VideoID: video.ID, Title: video.Title, Channel: video.ChannelName,
		Step: "indepth-retry",
	}), totals)
	started := time.Now()
	fail := func(stage string, err error) (bool, error) {
		// A shutdown is not a failure: the row stays as it was, due again at
		// the next boot. Returned like the job path's interrupted: Run does
		// not log an error once ctx is done.
		if cerr := ctx.Err(); cerr != nil {
			w.d.Logger.Debug("summarize worker: in-depth retry interrupted", append(ident, "stage", stage, "err", err)...)
			return true, cerr
		}
		level := slog.LevelWarn
		if attempts+1 >= inDepthLoudAfter {
			level = slog.LevelError
		}
		w.d.Logger.Log(ctx, level, "summarize worker: in-depth retry failed",
			append(ident, "stage", stage, "attempts", attempts+1, "duration_ms", time.Since(started).Milliseconds(), "err", err)...)
		w.deferInDepth(id, attempts, err)
		return true, nil
	}

	wrote := false
	if text == "" {
		text, err = w.d.Summarizer.InDepth(cctx, video.Title, int(video.DurationSeconds), prep.forSummary.Cues)
		if err != nil {
			return fail("indepth", err)
		}
		// Only beside the summary it was written from: the call takes minutes,
		// and a reprocess in between owns the video now.
		stored, err := w.d.Videos.SetInDepthIfSummary(id, text, video.Summary)
		if err != nil {
			return fail("indepth", err)
		}
		if !stored {
			return drop("summary changed during the call")
		}
		wrote = true
		w.emit(id, videos.SummaryDone, "")
		// Re-read: the index is built from the row as it is now, not as it
		// was before the call (a stale summary or embed_rev would be written
		// back over a reprocess).
		if video, err = w.d.Videos.Get(id); err != nil || video == nil || video.Summary == "" {
			if err != nil {
				w.deferInDepth(id, attempts, err)
				return true, nil
			}
			return true, w.d.Videos.ClearInDepthRetry(id)
		}
		if need, err = w.inDepthNeedsIndex(cctx, video, transcript.Source, text); err != nil {
			w.deferInDepth(id, attempts, err)
			return true, nil
		}
	}

	if need {
		inbox := isInboxRead(video, transcript.Source)
		var chapters []Chapter
		if !inbox {
			chapters = decodeChapters(video.Chapters)
		}
		if err := w.embedAndStore(cctx, video, prep.parsed, video.Summary, chapters); err != nil {
			return fail("embedding", err)
		}
		// Asked again after the embed, as the job does: a switch-off during
		// the call has already purged the channel's kept reads, and the embed
		// just wrote this one back.
		if inbox && !w.channelKeepsReads(id) {
			if err := w.d.Videos.DropIndex(id); err != nil {
				w.d.Logger.Warn("summarize worker: drop index after switch-off failed", "video_id", id, "err", err)
			}
		}
	}
	if err := w.d.Videos.ClearInDepthRetry(id); err != nil {
		return wrote || need, err
	}
	attrs := append(ident, "attempts", attempts, "written", wrote, "indexed", need,
		"words", len(strings.Fields(text)), "duration_ms", time.Since(started).Milliseconds())
	w.d.Logger.Info("summarize worker: in-depth retry done", append(attrs, totals.Snapshot().LogAttrs()...)...)
	return wrote || need, nil
}

// inDepthNeedsIndex reports whether the video's index lacks the in-depth
// sections it should carry. Only an index that exists is extended, under the
// model that wrote it: a video without one (an inbox read on a channel that
// does not keep reads) gets none here. A text with no indexable section never
// needs it, or the retry would never finish.
func (w *Worker) inDepthNeedsIndex(ctx context.Context, video *videos.Video, source, text string) (bool, error) {
	if video.EmbedModel != w.d.EmbedModel {
		return false, nil
	}
	if isInboxRead(video, source) && !w.channelKeepsReads(video.ID) {
		return false, nil
	}
	stats, err := w.d.Rag.ChunkStats(ctx, video.ID)
	if err != nil || len(stats) == 0 {
		return false, err
	}
	for _, k := range stats {
		if k.Kind == rag.KindInDepth {
			return false, nil
		}
	}
	for _, s := range inDepthSections(text, int(video.DurationSeconds)) {
		if strings.TrimSpace(s.Body) != "" {
			return true, nil
		}
	}
	return false, nil
}
