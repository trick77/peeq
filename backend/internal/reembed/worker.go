// Package reembed rebuilds a video's search index when the content recipe
// changes — currently, to add the chapter chunks recipe 2 introduced.
//
// The defining property of this worker is what it does NOT need. Everything a
// rebuild requires is already stored: the prose summary and the chapters are
// columns on videos, and the transcript re-parses from the subtitle file on
// disk. Only the embeddings endpoint is called. No chat request, no token
// spend, and a retry costs almost nothing — which is what makes sweeping the
// whole library at boot reasonable.
//
// That property is enforced structurally rather than by comment: Deps has no
// Summarizer and no llm.Client field, so a chat call cannot be added here
// without changing the type.
//
// This package is temporary. Once every video reaches rag.ChunkRecipeRev it has
// no work left; issue #240 tracks removing it. videos.embed_rev is the part
// that stays.
package reembed

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/trick77/peeq/internal/embedjobs"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/sched"
	"github.com/trick77/peeq/internal/subtitles"
	"github.com/trick77/peeq/internal/videos"
)

// Embedder is the slice of rag.EmbedClient this worker uses. Declared at the
// consumer so tests can drive the worker without an HTTP endpoint.
type Embedder interface {
	EmbedBatched(ctx context.Context, inputs []string, gap time.Duration) ([][]float32, error)
}

// VideoStore is the slice of videos.Store this worker reads.
type VideoStore interface {
	Get(id string) (*videos.Video, error)
}

// ChunkStore is the slice of rag.Store this worker writes.
type ChunkStore interface {
	ReplaceVideoChunks(ctx context.Context, videoID string, meta rag.IndexMeta, rows []rag.ChunkRow, vectors [][]float32) error
	DeleteVideoChunks(ctx context.Context, videoID string) error
}

// Deps are the worker's collaborators. Note the absence of any chat client —
// see the package comment.
type Deps struct {
	Jobs     *embedjobs.Store
	Videos   VideoStore
	Rag      ChunkStore
	Embedder Embedder

	MediaDir   string
	EmbedModel string
	EmbedDim   int

	// PollInterval is how long to idle when the queue is empty.
	PollInterval time.Duration
	// VideoDelay is the pause between videos, so a library-wide backfill
	// trickles instead of running flat out.
	VideoDelay time.Duration
	// BatchDelay is the pause between the embedding requests of one video.
	BatchDelay time.Duration

	Logger *slog.Logger
}

// Worker drains the re-embed queue.
type Worker struct{ d Deps }

const (
	defaultPollInterval = 5 * time.Second
	defaultVideoDelay   = 2 * time.Second
	defaultBatchDelay   = 250 * time.Millisecond
)

func New(d Deps) *Worker {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.PollInterval <= 0 {
		d.PollInterval = defaultPollInterval
	}
	if d.VideoDelay <= 0 {
		d.VideoDelay = defaultVideoDelay
	}
	if d.BatchDelay <= 0 {
		d.BatchDelay = defaultBatchDelay
	}
	return &Worker{d: d}
}

// Run drains the queue until ctx is cancelled. Orphan reset and the stale sweep
// happen at boot in main, before this starts, so a restart mid-backfill resumes
// rather than double-queueing.
func (w *Worker) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		worked := w.processOne(ctx)
		delay := w.d.PollInterval
		if worked {
			delay = w.d.VideoDelay
		}
		if !sched.Sleep(ctx, delay) {
			return
		}
	}
}

// processOne claims and rebuilds a single video. It reports whether it did any
// work, so Run can idle longer on an empty queue. It is the test seam.
func (w *Worker) processOne(ctx context.Context) bool {
	job, err := w.d.Jobs.ClaimNext()
	if err != nil {
		w.d.Logger.Error("reembed: claim failed", "err", err)
		return false
	}
	if job == nil {
		return false
	}

	if err := w.rebuild(ctx, job.VideoID); err != nil {
		// A cancelled context is a shutdown, not a failure of this video —
		// requeue it without consuming the error budget's meaning.
		if ctx.Err() != nil {
			_, _ = w.d.Jobs.Fail(job.ID, "interrupted by shutdown")
			return true
		}
		terminal, ferr := w.d.Jobs.Fail(job.ID, err.Error())
		if ferr != nil {
			w.d.Logger.Error("reembed: record failure failed", "video_id", job.VideoID, "err", ferr)
		}
		level := slog.LevelWarn
		if terminal {
			level = slog.LevelError
		}
		w.d.Logger.Log(ctx, level, "reembed failed",
			"video_id", job.VideoID, "terminal", terminal, "err", err)
		return true
	}

	if err := w.d.Jobs.Finish(job.ID, embedjobs.StateDone, ""); err != nil {
		w.d.Logger.Error("reembed: finish failed", "video_id", job.VideoID, "err", err)
	}
	return true
}

// errSkip marks a video that cannot be rebuilt and never will be, so the job is
// completed rather than retried three times to no purpose.
var errSkip = errors.New("nothing to rebuild")

// rebuild re-derives a video's chunks from stored data and replaces its index.
func (w *Worker) rebuild(ctx context.Context, videoID string) error {
	video, err := w.d.Videos.Get(videoID)
	if err != nil {
		return err
	}
	// video == nil is defensive only: embed_jobs.video_id cascades on delete, so
	// a job cannot outlive the video it names.
	if video == nil || video.Status == videos.StatusTombstoned || video.SubtitlePath == "" {
		w.d.Logger.Info("reembed: skipped", "video_id", videoID, "reason", "no subtitles to rebuild from")
		return nil
	}

	parsed, err := w.parseSubtitles(video.SubtitlePath)
	if err != nil {
		if errors.Is(err, errSkip) {
			// The subtitle file is gone or unreadable. The stored chunks now
			// describe a transcript nothing can reproduce, so drop them rather
			// than leave a stale index that search would still return.
			if derr := w.d.Rag.DeleteVideoChunks(ctx, videoID); derr != nil {
				return derr
			}
			w.d.Logger.Info("reembed: dropped stale index", "video_id", videoID, "reason", err.Error())
			return nil
		}
		return err
	}

	rows := rag.BuildVideoChunks(parsed, video.Summary, rag.DecodeChapters(video.Chapters))
	if len(rows) == 0 {
		if derr := w.d.Rag.DeleteVideoChunks(ctx, videoID); derr != nil {
			return derr
		}
		w.d.Logger.Info("reembed: dropped empty index", "video_id", videoID)
		return nil
	}

	texts := make([]string, len(rows))
	for i, r := range rows {
		texts[i] = r.Text
	}
	vecs, err := w.d.Embedder.EmbedBatched(ctx, texts, w.d.BatchDelay)
	if err != nil {
		return err
	}

	meta := rag.IndexMeta{Model: w.d.EmbedModel, Dim: w.d.EmbedDim, Rev: rag.ChunkRecipeRev}
	if err := w.d.Rag.ReplaceVideoChunks(ctx, videoID, meta, rows, vecs); err != nil {
		return err
	}
	w.d.Logger.Info("reembed: reindexed",
		"video_id", videoID, "chunks", len(rows), "rev", rag.ChunkRecipeRev,
		"chapters", countKind(rows, rag.KindChapter))
	return nil
}

// parseSubtitles loads and parses the video's VTT, always through
// media.SafeMediaPath so a stored path cannot escape the media directory.
func (w *Worker) parseSubtitles(subtitlePath string) (subtitles.Parsed, error) {
	safe, err := media.SafeMediaPath(w.d.MediaDir, subtitlePath)
	if err != nil {
		return subtitles.Parsed{}, err
	}
	f, err := os.Open(safe)
	if err != nil {
		return subtitles.Parsed{}, errSkip
	}
	defer f.Close()
	parsed, err := subtitles.ParseVTT(f)
	if err != nil {
		return subtitles.Parsed{}, err
	}
	if parsed.Transcript == "" {
		return subtitles.Parsed{}, errSkip
	}
	return parsed, nil
}

func countKind(rows []rag.ChunkRow, kind string) int {
	n := 0
	for _, r := range rows {
		if r.Kind == kind {
			n++
		}
	}
	return n
}
