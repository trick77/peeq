package summarize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/subtitles"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
)

// Embedder is the subset of rag.EmbedClient the worker needs.
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

// WorkerDeps are the worker's collaborators and tunables. The stores,
// Summarizer, and Embedder are required; the rest have safe defaults applied
// in NewWorker.
type WorkerDeps struct {
	Jobs         *summaryjobs.Store
	Videos       *videos.Store
	Rag          *rag.Store
	Summarizer   *Summarizer
	Embedder     Embedder
	MediaDir     string
	EmbedModel   string
	EmbedDim     int
	PollInterval time.Duration
	// VideoDelay is a pause between videos, giving a slow/rate-limited LLM
	// endpoint room to breathe. 0 disables it.
	VideoDelay time.Duration
	Logger     *slog.Logger

	// OnPhase, when set, is called at each summary state transition so an SSE
	// hub can push live progress to the Player. videoID is always set so the
	// client can filter to the open video.
	OnPhase func(videoID, status, phase string)
}

// Worker is the single-concurrency summarization+embedding loop: the twin of
// internal/download/worker.go for the summary_jobs queue.
type Worker struct {
	d WorkerDeps
	// classifyFailed holds video ids whose idle-sweep classify call errored, so
	// the sweep moves past them instead of retrying the same video forever.
	// Process-lifetime only: a restart gives every video another chance, which
	// is the right bound for a transient LLM outage.
	classifyFailed map[string]bool
}

// NewWorker builds a Worker, filling in defaults for the optional deps.
func NewWorker(d WorkerDeps) *Worker {
	if d.PollInterval <= 0 {
		d.PollInterval = 2 * time.Second
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d, classifyFailed: map[string]bool{}}
}

// Run is the worker loop; it blocks until ctx is cancelled. It first resets
// any orphaned running jobs left by a previous process and backfills jobs for
// downloaded videos that never got one, then repeatedly claims and processes
// the next job.
func (w *Worker) Run(ctx context.Context) {
	if err := w.d.Jobs.ResetOrphans(); err != nil {
		w.d.Logger.Error("summarize worker: reset orphans", "err", err)
	}
	// A video can be downloaded without a summary job when a process dies in
	// the window between the two writes (see taimport.importOne). Nothing else
	// ever revisits such a video, so sweep for them once at boot.
	if n, err := w.d.Jobs.EnqueueMissing(); err != nil {
		w.d.Logger.Error("summarize worker: backfill missing jobs", "err", err)
	} else if n > 0 {
		w.d.Logger.Info("summarize worker: backfilled summary jobs", "count", n)
	}
	for {
		if ctx.Err() != nil {
			return
		}
		did, err := w.processOne(ctx)
		if err != nil {
			w.d.Logger.Error("summarize worker: process", "err", err)
		}
		// Idle: poll again shortly. Busy: pause VideoDelay so the LLM endpoint
		// gets room to breathe between videos.
		gap := w.d.PollInterval
		if did {
			gap = w.d.VideoDelay
		}
		if gap > 0 {
			t := time.NewTimer(gap)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}
}

// processOne claims and processes a single job. When the queue is empty it
// spends the turn on the classification backlog instead, and returns false
// only when there is nothing left to do at all. Panics are recovered so one bad
// job never kills the loop. This is the test seam: tests call it directly to
// drive one job deterministically, without goroutine/ticker timing.
func (w *Worker) processOne(ctx context.Context) (did bool, err error) {
	job, err := w.d.Jobs.ClaimNext()
	if err != nil {
		return false, err
	}
	if job == nil {
		return w.classifyOne(ctx)
	}
	defer func() {
		if r := recover(); r != nil {
			w.d.Logger.Error("summarize worker: recovered", "job_id", job.ID, "panic", r)
			_ = w.d.Jobs.Fail(job.ID, job.Attempts, "panic")
			_ = w.d.Videos.SetSummaryStatus(job.VideoID, "error", "internal error")
		}
	}()

	video, err := w.d.Videos.Get(job.VideoID)
	if err != nil || video == nil {
		_ = w.d.Jobs.Finish(job.ID, "failed", "video missing")
		return true, err
	}

	// No subtitles => clean terminal no_transcript state, not an error.
	if video.SubtitlePath == "" {
		_ = w.d.Videos.SetSummaryStatus(video.ID, "no_transcript", "")
		w.emit(video.ID, "no_transcript", "")
		_ = w.d.Jobs.Finish(job.ID, "done", "")
		return true, nil
	}

	// Announce "running" only for a fresh job; a resumed one (retrying just the
	// key-points step) already has its summary marked done and must not regress.
	if video.SummaryStatus != "done" {
		_ = w.d.Videos.SetSummaryStatus(video.ID, "running", "")
		w.emit(video.ID, "running", "summarizing")
	}

	safe, err := media.SafeMediaPath(w.d.MediaDir, video.SubtitlePath)
	if err != nil {
		return true, w.failJob(job, video.ID, "unsafe subtitle path")
	}
	f, err := os.Open(safe)
	if err != nil {
		_ = w.d.Videos.SetSummaryStatus(video.ID, "no_transcript", "")
		w.emit(video.ID, "no_transcript", "")
		_ = w.d.Jobs.Finish(job.ID, "done", "")
		return true, nil
	}
	parsed, perr := subtitles.ParseVTT(f)
	f.Close()
	if perr != nil {
		return true, w.failJob(job, video.ID, "parse vtt: "+perr.Error())
	}
	if parsed.Transcript == "" {
		_ = w.d.Videos.SetSummaryStatus(video.ID, "no_transcript", "")
		w.emit(video.ID, "no_transcript", "")
		_ = w.d.Jobs.Finish(job.ID, "done", "")
		return true, nil
	}

	// The pipeline is resumable: each artifact is saved the moment it is
	// produced, and a retry skips whatever a prior attempt already stored. So a
	// failure in the fragile key-points step (step 3) never discards the summary
	// or embeddings, and only that step re-runs.

	// Step 1 — prose summary. Persist on its own; skip if already saved.
	summary := video.Summary
	if summary == "" {
		s, serr := w.d.Summarizer.SummarizeText(ctx, parsed.Transcript)
		if serr != nil {
			return true, w.failJob(job, video.ID, serr.Error())
		}
		if err := w.d.Videos.SetSummaryText(video.ID, s); err != nil {
			return true, w.failJob(job, video.ID, err.Error())
		}
		summary = s
	}

	// Step 2 — category (best-effort). It needs only the title and the summary,
	// so it runs here rather than at the end: behind the fragile key-points call
	// it never ran at all for videos whose endpoint timed out there, which is
	// how a large backlog ended up summarized but 'uncategorized'. A classify
	// failure must NOT fail the job — the summary above is already saved, and
	// the idle sweep in classifyOne picks the video up later.
	if video.Category == "" || video.Category == videos.UncategorizedCategory {
		if raw, cerr := w.d.Summarizer.Classify(ctx, video.Title, summary, videos.ClassifiableCategories()); cerr != nil {
			w.d.Logger.Warn("summarize worker: classify failed", "video_id", video.ID, "err", cerr)
		} else if serr := w.d.Videos.SetCategory(video.ID, videos.NormalizeCategory(raw)); serr != nil {
			w.d.Logger.Error("summarize worker: set category failed", "video_id", video.ID, "err", serr)
		}
	}

	// Step 3 — embeddings. Skip if already embedded (embed_model is set).
	if video.EmbedModel == "" {
		w.emit(video.ID, "running", "embedding")
		if err := w.embedAndStore(ctx, video.ID, parsed, summary); err != nil {
			return true, w.failJob(job, video.ID, err.Error())
		}
	}

	// Summary + search are usable now — mark done so the UI shows them even if
	// the key-points step below is still pending on a slow endpoint.
	if video.SummaryStatus != "done" {
		_ = w.d.Videos.SetSummaryStatus(video.ID, "done", "")
		w.emit(video.ID, "done", "")
	}

	// Step 4 — key points (and chapters when yt-dlp didn't supply them). The
	// fragile call, run last so a failure retries only this and costs nothing.
	ytChapters := decodeChapters(video.Chapters)
	chapters, keyPoints, err := w.d.Summarizer.KeyPoints(ctx, summary, parsed.Cues, ytChapters)
	if err != nil {
		return true, w.requeueJob(job, video.ID, err.Error())
	}
	if err := w.d.Videos.SetKeyPoints(video.ID, encodeChapters(chapters), encodeKeyPoints(keyPoints)); err != nil {
		return true, w.requeueJob(job, video.ID, err.Error())
	}
	w.emit(video.ID, "done", "")

	_ = w.d.Jobs.Finish(job.ID, "done", "")
	return true, nil
}

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

	raw, cerr := w.d.Summarizer.Classify(ctx, video.Title, video.Summary, videos.ClassifiableCategories())
	if cerr != nil {
		// Park it for this process so the sweep advances to the next video
		// rather than retrying this one on every turn.
		w.classifyFailed[video.ID] = true
		w.d.Logger.Warn("summarize worker: backlog classify failed", "video_id", video.ID, "err", cerr)
		return true, nil
	}
	category := videos.NormalizeCategory(raw)
	if serr := w.d.Videos.SetCategory(video.ID, category); serr != nil {
		w.classifyFailed[video.ID] = true
		w.d.Logger.Error("summarize worker: backlog set category failed", "video_id", video.ID, "err", serr)
		return true, nil
	}
	if category == videos.UncategorizedCategory {
		// The call succeeded but the reply was unusable. Park it too: retrying
		// the same prompt every turn would just burn requests.
		w.classifyFailed[video.ID] = true
	}
	w.d.Logger.Info("summarize worker: classified backlog video", "video_id", video.ID, "category", category)
	return true, nil
}

// emit calls OnPhase when set, so an SSE hub can push live summarize
// progress to the Player. It is a no-op when OnPhase is nil.
func (w *Worker) emit(videoID, status, phase string) {
	if w.d.OnPhase != nil {
		w.d.OnPhase(videoID, status, phase)
	}
}

// failJob records the failure on both the video and the job, and always
// returns a non-nil error so the caller (processOne) surfaces the failure
// to the Run loop, which logs it. Jobs.Fail's own return is often nil on
// the common path, so it must never be returned as-is.
func (w *Worker) failJob(job *summaryjobs.Job, videoID, msg string) error {
	if err := w.d.Videos.SetSummaryStatus(videoID, "error", msg); err != nil {
		w.d.Logger.Error("summarize worker: set error status", "video_id", videoID, "err", err)
	}
	w.emit(videoID, "error", "")
	if err := w.d.Jobs.Fail(job.ID, job.Attempts, msg); err != nil {
		return fmt.Errorf("summarize job %d failed (%s); also fail-record error: %w", job.ID, msg, err)
	}
	return fmt.Errorf("summarize job %d failed: %s", job.ID, msg)
}

// requeueJob records a retryable failure and requeues the job WITHOUT touching
// summary_status. It is used for the key-points step, which runs after the
// summary is already marked done: a failure there must retry only that step and
// must NOT regress a usable summary to "error". If retries run out the job is
// marked failed but the video keeps its summary and search.
func (w *Worker) requeueJob(job *summaryjobs.Job, videoID, msg string) error {
	w.d.Logger.Warn("summarize worker: key-points step failed, will retry", "video_id", videoID, "err", msg)
	if err := w.d.Jobs.Fail(job.ID, job.Attempts, msg); err != nil {
		return fmt.Errorf("summarize job %d key-points failed (%s); also fail-record error: %w", job.ID, msg, err)
	}
	return fmt.Errorf("summarize job %d key-points failed: %s", job.ID, msg)
}

// embedAndStore chunks the transcript, maps each chunk to its start-second via
// word-offset lookup against the cue index, embeds, and replaces the video's
// chunks+vectors.
func (w *Worker) embedAndStore(ctx context.Context, videoID string, parsed subtitles.Parsed, summaryText string) error {
	chunks := rag.Chunk(parsed.Transcript, rag.DefaultChunkOptions())
	if len(chunks) == 0 {
		return errors.New("no chunks")
	}
	cueWordStarts := cueWordStartIndex(parsed.Cues)
	texts := make([]string, 0, len(chunks)+1)
	rows := make([]rag.ChunkRow, 0, len(chunks)+1)
	for _, c := range chunks {
		texts = append(texts, c.Text)
		rows = append(rows, rag.ChunkRow{
			Ordinal:      c.Ordinal,
			Text:         c.Text,
			Kind:         "transcript",
			TokenCount:   c.TokenCount,
			StartSeconds: cueStartForWordOffset(c.WordOffset, parsed.Cues, cueWordStarts),
		})
	}
	// Index the summary as one extra chunk so keyword+semantic search also
	// matches against it (spec §7). It describes the whole video, so it has no
	// timestamp (start_seconds = 0); the search UI badges it and opens at 0.
	if s := strings.TrimSpace(summaryText); s != "" {
		texts = append(texts, s)
		rows = append(rows, rag.ChunkRow{
			Ordinal:      len(chunks),
			Text:         s,
			Kind:         "summary",
			StartSeconds: 0,
		})
	}
	vecs, err := w.d.Embedder.Embed(ctx, texts)
	if err != nil {
		return err
	}
	return w.d.Rag.ReplaceVideoChunks(ctx, videoID, w.d.EmbedModel, w.d.EmbedDim, rows, vecs)
}

// cueWordStartIndex returns, for each cue, the cumulative word count of all
// preceding cues' text — i.e. the word-offset (into subtitles.Parsed.Transcript,
// which is strings.Join(cueTexts, " ")) at which that cue's text begins. This
// lets a chunk's WordOffset be mapped back to the cue it actually starts in,
// which is exact and monotonic, unlike prefix-matching the chunk's (possibly
// overlap-shifted) leading text against cue text.
func cueWordStartIndex(cues []subtitles.Cue) []int {
	starts := make([]int, len(cues))
	total := 0
	for i, c := range cues {
		starts[i] = total
		total += len(strings.Fields(c.Text))
	}
	return starts
}

// cueStartForWordOffset returns the StartSeconds of the last cue whose
// word-start is <= wordOffset, i.e. the cue that word belongs to. Falls back
// to 0 when cues is empty.
func cueStartForWordOffset(wordOffset int, cues []subtitles.Cue, cueWordStarts []int) int {
	best := 0
	for i, ws := range cueWordStarts {
		if ws <= wordOffset {
			best = cues[i].StartSeconds
		} else {
			break
		}
	}
	return best
}
