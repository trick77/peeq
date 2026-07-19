package summarize

import (
	"context"
	"errors"
	"log/slog"
	"os"
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

func (w *Worker) failJob(job *summaryjobs.Job, videoID, msg string) error {
	_ = w.d.Videos.SetSummaryStatus(videoID, "error", msg)
	return w.d.Jobs.Fail(job.ID, job.Attempts, msg)
}

// embedAndStore chunks the transcript, maps each chunk to the nearest earlier
// cue start-second, embeds, and replaces the video's chunks+vectors.
func (w *Worker) embedAndStore(ctx context.Context, videoID string, parsed subtitles.Parsed) error {
	chunks := rag.Chunk(parsed.Transcript, rag.DefaultChunkOptions())
	if len(chunks) == 0 {
		return errors.New("no chunks")
	}
	texts := make([]string, len(chunks))
	rows := make([]rag.ChunkRow, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
		rows[i] = rag.ChunkRow{Ordinal: c.Ordinal, Text: c.Text, TokenCount: c.TokenCount, StartSeconds: cueStartFor(c.Text, parsed.Cues)}
	}
	vecs, err := w.d.Embedder.Embed(ctx, texts)
	if err != nil {
		return err
	}
	return w.d.Rag.ReplaceVideoChunks(ctx, videoID, w.d.EmbedModel, w.d.EmbedDim, rows, vecs)
}

// cueStartFor finds the start-second of the first cue whose text opens this
// chunk (chunks are built from the joined cue texts, so the chunk's first
// words match some cue). Falls back to 0.
func cueStartFor(chunkText string, cues []subtitles.Cue) int {
	head := chunkText
	if len(head) > 24 {
		head = head[:24]
	}
	for _, c := range cues {
		if len(c.Text) >= len(head) && c.Text[:min(len(head), len(c.Text))] == head {
			return c.StartSeconds
		}
	}
	return 0
}
