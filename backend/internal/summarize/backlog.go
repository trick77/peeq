package summarize

import (
	"context"
	"time"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/videos"
)

// classifyOne repairs one video from the classification backlog: a downloaded
// video that has a summary but is still 'uncategorized'. It runs only when the
// summary queue is empty, so real work always wins, and reports did=true only
// when it actually made an LLM call — returning true on an empty backlog would
// spin the Run loop, which skips its poll interval whenever a turn did work.
//
// The backlog exists because classification used to sit behind the key-points
// call and was skipped whenever that failed; it also absorbs any future
// best-effort classify failure. Errors are logged, never returned as job
// failures — there is no job here to fail.
func (w *Worker) classifyOne(ctx context.Context) (bool, error) {
	skip := make([]string, 0, len(w.classifyFailed))
	for id := range w.classifyFailed {
		skip = append(skip, id)
	}
	video, err := w.d.Videos.NextUnclassified(skip)
	if err != nil || video == nil {
		return false, err
	}

	// Same identity/token plumbing as a full analysis, so a backlog sweep is as
	// readable as a normal run — including the "still waiting" heartbeat.
	totals := &llm.Totals{}
	cctx := llm.WithTotals(llm.WithCall(ctx, llm.CallInfo{
		VideoID: video.ID, Title: video.Title, Channel: video.ChannelName,
		Step: "classify-backlog",
	}), totals)
	ident := []any{"video_id", video.ID, "title", video.Title, "channel", video.ChannelName}
	started := time.Now()

	raw, cerr := w.d.Summarizer.Classify(cctx, video.Title, video.Summary, videos.ClassifiableCategories())
	elapsed := time.Since(started).Milliseconds()
	if cerr != nil {
		// Park it for this process so the sweep advances to the next video
		// rather than retrying this one on every turn.
		w.classifyFailed[video.ID] = true
		w.d.Logger.Warn("summarize worker: backlog classify failed", append(ident, "duration_ms", elapsed, "err", cerr)...)
		return true, nil
	}
	category := videos.NormalizeCategory(raw)
	// Guarded, because the classify call above is slow enough for the user to
	// have picked a category on the Player in the meantime. A no-op write is
	// not a failure and needs no parking: the video no longer matches
	// NextUnclassified, so the sweep will not offer it again.
	applied, serr := w.d.Videos.SetCategoryIfUnset(video.ID, category)
	if serr != nil {
		w.classifyFailed[video.ID] = true
		w.d.Logger.Error("summarize worker: backlog set category failed", append(ident, "duration_ms", elapsed, "err", serr)...)
		return true, nil
	}
	if !applied {
		w.d.Logger.Info("summarize worker: backlog video was categorized meanwhile; keeping it", "video_id", video.ID)
		return true, nil
	}
	if category == videos.UncategorizedCategory {
		// The call succeeded but the reply was unusable. Park it too: retrying
		// the same prompt every turn would just burn requests.
		w.classifyFailed[video.ID] = true
	}
	attrs := append(ident, "category", category, "duration_ms", elapsed)
	w.d.Logger.Info("summarize worker: classified backlog video", append(attrs, totals.Snapshot().LogAttrs()...)...)
	return true, nil
}
