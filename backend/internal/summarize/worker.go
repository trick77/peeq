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
	Logger       *slog.Logger
}

// Worker is the single-concurrency summarization+embedding loop: the twin of
// internal/download/worker.go for the summary_jobs queue.
type Worker struct{ d WorkerDeps }

// NewWorker builds a Worker, filling in defaults for the optional deps.
func NewWorker(d WorkerDeps) *Worker {
	if d.PollInterval <= 0 {
		d.PollInterval = 2 * time.Second
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d}
}

// Run is the worker loop; it blocks until ctx is cancelled. It first resets
// any orphaned running jobs left by a previous process, then repeatedly
// claims and processes the next job.
func (w *Worker) Run(ctx context.Context) {
	if err := w.d.Jobs.ResetOrphans(); err != nil {
		w.d.Logger.Error("summarize worker: reset orphans", "err", err)
	}
	for {
		if ctx.Err() != nil {
			return
		}
		did, err := w.processOne(ctx)
		if err != nil {
			w.d.Logger.Error("summarize worker: process", "err", err)
		}
		if !did {
			t := time.NewTimer(w.d.PollInterval)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}
}

// processOne claims and processes a single job. Returns false when the queue
// is empty. Panics are recovered so one bad job never kills the loop. This is
// the test seam: tests call it directly to drive one job deterministically,
// without goroutine/ticker timing.
func (w *Worker) processOne(ctx context.Context) (did bool, err error) {
	job, err := w.d.Jobs.ClaimNext()
	if err != nil || job == nil {
		return false, err
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
		_ = w.d.Jobs.Finish(job.ID, "done", "")
		return true, nil
	}

	_ = w.d.Videos.SetSummaryStatus(video.ID, "running", "")

	safe, err := media.SafeMediaPath(w.d.MediaDir, video.SubtitlePath)
	if err != nil {
		return true, w.failJob(job, video.ID, "unsafe subtitle path")
	}
	f, err := os.Open(safe)
	if err != nil {
		_ = w.d.Videos.SetSummaryStatus(video.ID, "no_transcript", "")
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
		_ = w.d.Jobs.Finish(job.ID, "done", "")
		return true, nil
	}

	// yt-dlp chapters already stored on the video (source=yt-dlp) are preferred.
	ytChapters := decodeChapters(video.Chapters)
	art, err := w.d.Summarizer.Run(ctx, parsed.Transcript, parsed.Cues, ytChapters)
	if err != nil {
		return true, w.failJob(job, video.ID, err.Error())
	}

	if err := w.embedAndStore(ctx, video.ID, parsed); err != nil {
		return true, w.failJob(job, video.ID, err.Error())
	}

	chJSON := encodeChapters(art.Chapters)
	kpJSON := encodeKeyPoints(art.KeyPoints)
	if err := w.d.Videos.SetSummary(video.ID, art.Summary, chJSON, kpJSON); err != nil {
		return true, w.failJob(job, video.ID, err.Error())
	}
	_ = w.d.Jobs.Finish(job.ID, "done", "")
	return true, nil
}

// failJob records the failure on both the video and the job, and always
// returns a non-nil error so the caller (processOne) surfaces the failure
// to the Run loop, which logs it. Jobs.Fail's own return is often nil on
// the common path, so it must never be returned as-is.
func (w *Worker) failJob(job *summaryjobs.Job, videoID, msg string) error {
	if err := w.d.Videos.SetSummaryStatus(videoID, "error", msg); err != nil {
		w.d.Logger.Error("summarize worker: set error status", "video_id", videoID, "err", err)
	}
	if err := w.d.Jobs.Fail(job.ID, job.Attempts, msg); err != nil {
		return fmt.Errorf("summarize job %d failed (%s); also fail-record error: %w", job.ID, msg, err)
	}
	return fmt.Errorf("summarize job %d failed: %s", job.ID, msg)
}

// embedAndStore chunks the transcript, maps each chunk to its start-second via
// word-offset lookup against the cue index, embeds, and replaces the video's
// chunks+vectors.
func (w *Worker) embedAndStore(ctx context.Context, videoID string, parsed subtitles.Parsed) error {
	chunks := rag.Chunk(parsed.Transcript, rag.DefaultChunkOptions())
	if len(chunks) == 0 {
		return errors.New("no chunks")
	}
	cueWordStarts := cueWordStartIndex(parsed.Cues)
	texts := make([]string, len(chunks))
	rows := make([]rag.ChunkRow, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
		rows[i] = rag.ChunkRow{
			Ordinal:      c.Ordinal,
			Text:         c.Text,
			TokenCount:   c.TokenCount,
			StartSeconds: cueStartForWordOffset(c.WordOffset, parsed.Cues, cueWordStarts),
		}
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
