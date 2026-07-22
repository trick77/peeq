package sponsorblock

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/trick77/peeq/internal/sched"
	"github.com/trick77/peeq/internal/videos"
)

const (
	// pollInterval is how often the worker looks for videos to read segments
	// for. Short compared with the other background loops because this one
	// does not talk to YouTube: it is a public, unauthenticated API with no
	// account to protect, and a fresh import has thousands of videos to get
	// through.
	pollInterval = time.Minute
	// batchSize is how many videos one pass handles. Together with
	// betweenLookups and pollInterval this drains roughly 1200 videos an hour
	// — a large imported library in a few hours rather than a few weeks —
	// while never issuing more than one request at a time.
	batchSize = 20
	// betweenLookups spaces the requests inside a batch. Politeness toward a
	// free community API, not a rate limit peeq has hit.
	betweenLookups = time.Second
	// lookupTimeout bounds one video's lookup. The client's own HTTP timeout
	// is the same order; this also covers a body that trickles in.
	lookupTimeout = 20 * time.Second
)

// SegmentFetcher reads segments for one video. *Client implements it.
type SegmentFetcher interface {
	Segments(ctx context.Context, videoID string, durationSeconds float64) ([]Segment, error)
}

// VideoStore is the slice of videos.Store the worker needs.
type VideoStore interface {
	ClaimSponsorblockStale(limit int) ([]videos.SponsorblockCandidate, error)
	SetSponsorblockSegments(id, segmentsJSON string) error
}

// Deps are the worker's collaborators. Fetcher and Videos are required.
type Deps struct {
	Fetcher SegmentFetcher
	Videos  VideoStore
	// PollInterval defaults to pollInterval; BatchSize to batchSize;
	// BetweenLookups to betweenLookups (set it to a negative value in tests to
	// remove the spacing entirely).
	PollInterval   time.Duration
	BatchSize      int
	BetweenLookups time.Duration
	Logger         *slog.Logger
}

// Worker keeps every downloaded video's SponsorBlock segments current. It
// backfills videos that never had any — imports, and everything downloaded
// before the info.json parser was fixed — and re-reads old ones, since
// segments keep being submitted long after a video is published.
//
// Unlike peeq's other background loops it is NOT cookie-gated, NOT paced by
// the yt-dlp throttle, and NOT stopped by the YouTube kill-switch: it talks to
// sponsor.ajay.app, which is not YouTube and carries none of the risk those
// guards exist for.
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
	if d.BetweenLookups == 0 {
		d.BetweenLookups = betweenLookups
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
// so Run stops promptly rather than working through the rest of the claim.
func (w *Worker) pass(ctx context.Context) bool {
	candidates, err := w.d.Videos.ClaimSponsorblockStale(w.d.BatchSize)
	if err != nil {
		w.d.Logger.Error("sponsorblock: claim failed", "err", err)
		return true
	}
	for i, c := range candidates {
		if ctx.Err() != nil {
			return false
		}
		if i > 0 && w.d.BetweenLookups > 0 && !sched.Sleep(ctx, w.d.BetweenLookups) {
			return false
		}
		w.refresh(ctx, c)
	}
	return true
}

// refresh reads one video's segments and stores them, under a panic guard —
// this parses a remote HTTP response, and an unrecovered panic must not take
// the process down.
//
// A failed lookup deliberately leaves sponsorblock_refreshed_at alone, so the
// video stays claimable and is retried on a later pass. That cannot spin: a
// persistent failure means the API is unreachable, in which case every video
// fails equally and the whole loop is simply idle until it comes back.
func (w *Worker) refresh(ctx context.Context, c videos.SponsorblockCandidate) {
	defer func() {
		if r := recover(); r != nil {
			w.d.Logger.Error("sponsorblock: recovered from panic", "video_id", c.ID, "panic", r)
		}
	}()

	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	segs, err := w.d.Fetcher.Segments(lctx, c.ID, float64(c.DurationSeconds))
	if err != nil {
		w.d.Logger.Warn("sponsorblock lookup failed", "video_id", c.ID, "err", err)
		return
	}

	// An empty result is stored, not skipped: "this video has no segments" is
	// an answer worth recording, and the stamp it carries is what keeps the
	// claim query from returning the same video every minute forever.
	encoded, err := json.Marshal(nonNil(segs))
	if err != nil {
		w.d.Logger.Error("sponsorblock: encode segments failed", "video_id", c.ID, "err", err)
		return
	}
	if err := w.d.Videos.SetSponsorblockSegments(c.ID, string(encoded)); err != nil {
		w.d.Logger.Error("sponsorblock: store segments failed", "video_id", c.ID, "err", err)
		return
	}
	if len(segs) > 0 {
		w.d.Logger.Info("sponsorblock segments stored", "video_id", c.ID, "segments", len(segs))
	}
}

// nonNil turns a nil slice into an empty one, so the stored JSON is "[]"
// rather than "null" — the column's documented shape, and what the API layer
// and player parse.
func nonNil(segs []Segment) []Segment {
	if segs == nil {
		return []Segment{}
	}
	return segs
}
