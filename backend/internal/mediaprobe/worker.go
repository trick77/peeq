package mediaprobe

import (
	"context"
	"log/slog"
	"time"

	"github.com/trick77/peeq/internal/sched"
	"github.com/trick77/peeq/internal/videos"
)

const (
	// pollInterval is how often the worker looks for unprobed videos. The
	// loop reads local files with a local binary — no network, no account —
	// so it can be brisk, and it goes permanently idle once the library is
	// probed.
	pollInterval = 30 * time.Second
	// batchSize is how many files one pass handles. ffprobe on a local file
	// is milliseconds, but the batch is still bounded so an interrupted
	// backfill of a large library spreads its disk reads out instead of
	// hammering the volume in one burst at boot.
	batchSize = 25
	// probeTimeout bounds one file. Generous: the usual case is instant, but
	// a file on slow or waking storage should be given a chance rather than
	// permanently marked unprobeable.
	probeTimeout = 30 * time.Second
)

// VideoStore is the slice of videos.Store the worker needs.
type VideoStore interface {
	UnprobedDownloaded(limit int) ([]videos.ProbeCandidate, error)
	SetProbed(id string, res videos.ProbeResult) error
}

// FileProber reads one file's media facts. *Prober implements it.
type FileProber interface {
	Probe(ctx context.Context, path string) (Info, error)
}

// Deps are the worker's collaborators. Prober and Videos are required.
type Deps struct {
	Prober FileProber
	Videos VideoStore
	// PollInterval defaults to pollInterval; BatchSize to batchSize.
	PollInterval time.Duration
	BatchSize    int
	Logger       *slog.Logger
}

// Worker fills in the media facts for videos downloaded before peeq probed
// anything, so an existing library shows a full stat strip without every
// video having to be downloaded again.
//
// New downloads are probed inline by the download worker; this loop exists
// purely for the backlog, and idles once it is drained.
type Worker struct {
	d Deps
}

// NewWorker builds a Worker, filling in defaults for the optional Deps.
func NewWorker(d Deps) *Worker {
	if d.PollInterval <= 0 {
		d.PollInterval = pollInterval
	}
	if d.BatchSize <= 0 {
		d.BatchSize = batchSize
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d}
}

// Run is the backfill loop; it blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if !w.pass(ctx) {
			return
		}
		if !sched.Sleep(ctx, w.d.PollInterval) {
			return
		}
	}
}

// pass handles one batch. It reports false when ctx was cancelled mid-batch,
// so Run stops promptly rather than working through the rest of the list.
func (w *Worker) pass(ctx context.Context) bool {
	candidates, err := w.d.Videos.UnprobedDownloaded(w.d.BatchSize)
	if err != nil {
		w.d.Logger.Error("mediaprobe: list unprobed failed", "err", err)
		return true
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return false
		}
		w.probe(ctx, c)
	}
	return true
}

// probe reads one file and records the attempt, under a panic guard: this
// parses the output of an external binary and must not take the process down.
//
// Both outcomes write. A failure stores a zero result, which still stamps
// probed_at — that is what stops a deleted or corrupt file being retried on
// every pass forever. It also means the loop always converges: each pass
// either advances or the store itself is broken.
func (w *Worker) probe(ctx context.Context, c videos.ProbeCandidate) {
	defer func() {
		if r := recover(); r != nil {
			w.d.Logger.Error("mediaprobe: recovered from panic", "video_id", c.ID, "panic", r)
		}
	}()

	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	info, err := w.d.Prober.Probe(pctx, c.MediaPath)
	if err != nil {
		w.d.Logger.Warn("mediaprobe: probe failed", "video_id", c.ID, "path", c.MediaPath, "err", err)
		info = Info{}
	}
	if err := w.d.Videos.SetProbed(c.ID, StoreResult(info)); err != nil {
		w.d.Logger.Error("mediaprobe: store failed", "video_id", c.ID, "err", err)
	}
}

// StoreResult maps an Info onto the store's write shape. It is the single
// translation point between the two, so the download worker and the backfill
// loop cannot drift apart on what gets persisted.
func StoreResult(i Info) videos.ProbeResult {
	return videos.ProbeResult{
		Container:   i.Container,
		VideoCodec:  i.VideoCodec,
		VideoHeight: i.VideoHeight,
		AudioCodec:  i.AudioCodec,
	}
}
