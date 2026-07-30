// Package captionfetch reads an inbox video before anyone decides whether to
// download it.
//
// A subscription scan puts a video on the ledger as 'pending' and the Inbox
// shows it as a poster, a title, a channel and a runtime. That is not enough to
// judge a 42-minute video, and the only way to learn more used to be to
// download it. This worker closes that gap: it fetches the caption track on its
// own — a few KB, no media — creates the videos row that the rest of peeq
// hangs analysis off, and hands the video to the summarize worker.
//
// The video does NOT leave the Inbox. It gains a summary and stays exactly
// where it was, because the summary exists to inform a decision that has not
// been made yet.
package captionfetch

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/sched"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// pollInterval is how often the worker looks for a video to read.
//
// One video per tick, not a batch: every fetch is a yt-dlp call against
// YouTube, and this loop shares the pacer with the scans and metadata
// refreshes that peeq's subscriptions actually depend on. A minute is fast
// enough that a newly discovered video is usually summarized before the user
// next opens the Inbox, and slow enough that a channel dumping its archive
// cannot crowd out a scan.
const pollInterval = time.Minute

// Backoff is the retry ladder, indexed by the number of attempts already made.
//
// YouTube's automatic captions do not exist when a video is published. The ASR
// pass runs minutes to hours later — longer for long videos, which are exactly
// the ones worth summarizing — so a fetch at discovery usually comes back
// empty, and treating that as "this video has no captions" would make the
// feature useless for the fast-scanning channels it most helps.
//
// Five attempts spread over roughly 31 hours. The last rung is a day out
// because the remaining case at that point is an upload whose captions are
// genuinely slow rather than merely late, and there is no hurry: the user has
// had the card in their Inbox for a day already.
var Backoff = []time.Duration{
	15 * time.Minute,
	time.Hour,
	6 * time.Hour,
	24 * time.Hour,
	// The 5th attempt has no successor — a miss there settles the row.
}

// SubtitleFetcher fetches only the captions for one video, returning their
// MediaDir-relative path, or "" when YouTube has none yet. *ytdlp.Runner
// implements it.
type SubtitleFetcher interface {
	Subtitles(ctx context.Context, videoID, rawURL, subLang string) (string, error)
}

// Ledger is the slice of channelvideos.Store the worker needs.
type Ledger interface {
	NextCaptionCandidate() (*channelvideos.CaptionCandidate, error)
	RecordCaptionAttempt(videoID string, delaySeconds int) error
	ReturnCaptionAttempt(videoID string) error
	MarkCaptionSettled(videoID string) error
}

// VideoStore is the slice of videos.Store the worker needs.
type VideoStore interface {
	Upsert(v videos.Video) error
	SetStatus(id, status, errMsg string) error
	SetSubtitle(id, relPath, audioLang string) error
	SetSummaryStatus(id, status, errMsg string) error
}

// SummaryQueue enqueues the analysis job once a transcript exists.
type SummaryQueue interface {
	Enqueue(videoID string) (int64, error)
}

// Deps are the worker's collaborators. Fetcher, Ledger, Videos and Summaries
// are required.
type Deps struct {
	Fetcher   SubtitleFetcher
	Ledger    Ledger
	Videos    VideoStore
	Summaries SummaryQueue
	// DefaultSubLang is the caption language to request, mirroring the download
	// worker's. It must agree with what a download would ask for: if the two
	// diverge, the transcript summarized from the Inbox and the one stored
	// after downloading are different files, and the summary carried over
	// describes text the library does not have.
	DefaultSubLang string
	// PollInterval defaults to pollInterval.
	PollInterval time.Duration
	Logger       *slog.Logger
}

// Worker fetches captions for pending inbox videos and queues them for
// summarization.
type Worker struct {
	d Deps
}

// NewWorker builds a Worker, filling in defaults for the optional Deps.
func NewWorker(d Deps) *Worker {
	if d.PollInterval <= 0 {
		d.PollInterval = pollInterval
	}
	if d.DefaultSubLang == "" {
		d.DefaultSubLang = "en"
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d}
}

// Run is the fetch loop; it blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		w.pass(ctx)
		if !sched.Sleep(ctx, w.d.PollInterval) {
			return
		}
	}
}

// pass handles at most one video.
func (w *Worker) pass(ctx context.Context) {
	c, err := w.d.Ledger.NextCaptionCandidate()
	if err != nil {
		w.d.Logger.Error("captionfetch: claim failed", "err", err)
		return
	}
	if c == nil {
		return
	}

	// Burn the rung BEFORE doing anything that can fail, hang or be killed.
	// The alternative — recording the attempt on the way out — turns any crash
	// during a fetch into an unbounded retry loop against YouTube.
	last := c.Attempts+1 >= channelvideos.CaptionMaxAttempts
	delay := time.Duration(0)
	if c.Attempts < len(Backoff) {
		delay = Backoff[c.Attempts]
	}
	if err := w.d.Ledger.RecordCaptionAttempt(c.VideoID, int(delay.Seconds())); err != nil {
		w.d.Logger.Error("captionfetch: record attempt failed", "video_id", c.VideoID, "err", err)
		return
	}

	// The videos row is created before the fetch, not after, so a video whose
	// captions never arrive still has somewhere to record that fact — the
	// no_transcript status the card reads to stop showing a spinner.
	if err := w.ensureRow(c); err != nil {
		w.d.Logger.Error("captionfetch: create video row failed", "video_id", c.VideoID, "err", err)
		return
	}

	relPath, err := w.d.Fetcher.Subtitles(ctx, c.VideoID, c.URL, w.d.DefaultSubLang)
	if err != nil {
		// Cookie, pause and kill-switch refusals are the system working as
		// designed, not this video failing. Give the rung back so the video is
		// not quietly spent while peeq was gated.
		if refused(err) {
			if rerr := w.d.Ledger.ReturnCaptionAttempt(c.VideoID); rerr != nil {
				w.d.Logger.Error("captionfetch: return attempt failed", "video_id", c.VideoID, "err", rerr)
			}
			w.d.Logger.Debug("captionfetch: gated", "video_id", c.VideoID, "err", err)
			return
		}
		w.d.Logger.Warn("captionfetch: fetch failed", "video_id", c.VideoID, "attempt", c.Attempts+1, "err", err)
		if last {
			w.settleWithout(c)
		}
		return
	}

	if relPath == "" {
		w.d.Logger.Debug("captionfetch: no captions yet", "video_id", c.VideoID, "attempt", c.Attempts+1)
		if last {
			w.settleWithout(c)
		}
		return
	}

	if err := w.d.Videos.SetSubtitle(c.VideoID, relPath, w.d.DefaultSubLang); err != nil {
		w.d.Logger.Error("captionfetch: save subtitle failed", "video_id", c.VideoID, "err", err)
		return
	}
	if err := w.d.Ledger.MarkCaptionSettled(c.VideoID); err != nil {
		w.d.Logger.Error("captionfetch: settle failed", "video_id", c.VideoID, "err", err)
		// Not fatal: the summary job below is what matters, and a row that
		// gets one extra fetch is cheaper than one that never gets summarized.
	}
	if _, err := w.d.Summaries.Enqueue(c.VideoID); err != nil {
		w.d.Logger.Error("captionfetch: enqueue summary failed", "video_id", c.VideoID, "err", err)
		return
	}
	w.d.Logger.Info("captionfetch: queued for summary", "video_id", c.VideoID, "title", c.Title)
}

// ensureRow creates or refreshes the videos row for a candidate at StatusNew —
// "recorded, nothing requested yet", which is precisely what an inbox video
// with a summary is.
//
// videos.Store.Upsert writes only metadata columns; it never touches summary,
// subtitle_path, summary_status or status. That is what lets this run over a
// row that already has analysis without destroying it, and it is the same
// property the download path depends on.
func (w *Worker) ensureRow(c *channelvideos.CaptionCandidate) error {
	if err := w.d.Videos.Upsert(videos.Video{
		ID:              c.VideoID,
		URL:             c.URL,
		Title:           ytdlp.NormalizeTitle(c.Title),
		ChannelID:       c.ChannelID,
		DurationSeconds: int64(c.DurationSeconds),
		PublishedAt:     c.PublishedAt,
	}); err != nil {
		return err
	}
	return w.d.Videos.SetStatus(c.VideoID, videos.StatusNew, "")
}

// settleWithout closes the ladder for a video whose captions never arrived.
//
// no_transcript rather than error: the UI copy for that status already covers
// both "this video has no captions" and "the captions were music", and neither
// is a failure the user can act on. It also stops the card showing a spinner
// forever.
func (w *Worker) settleWithout(c *channelvideos.CaptionCandidate) {
	if err := w.d.Videos.SetSummaryStatus(c.VideoID, videos.SummaryNoTranscript, ""); err != nil {
		w.d.Logger.Error("captionfetch: set no_transcript failed", "video_id", c.VideoID, "err", err)
	}
	if err := w.d.Ledger.MarkCaptionSettled(c.VideoID); err != nil {
		w.d.Logger.Error("captionfetch: settle failed", "video_id", c.VideoID, "err", err)
	}
	w.d.Logger.Info("captionfetch: no captions after every attempt", "video_id", c.VideoID)
}

// refused reports whether err belongs to a failure family that applies to the
// whole run rather than to this video — which is exactly how ytdlp/errors.go
// describes all four of its sentinels.
//
// All four, not just the two gates. ErrCookieExpired and ErrBlocked come out of
// stderr classification rather than a gate, so it is tempting to read them as
// this call failing. They are not: during bot detection or an expired cookie
// EVERY call fails the same way, so counting them would burn a rung per tick on
// every video in the inbox and settle the lot as no_transcript within five
// ticks — permanently, since nothing re-reads a settled row. That is the exact
// outcome ReturnCaptionAttempt exists to prevent, and it is the half that
// matters most: a cookie expires far more often than the kill-switch is
// thrown. download.Worker.classify groups these two with the pause cases for
// the same reason.
func refused(err error) bool {
	return errors.Is(err, ytdlp.ErrNoCookie) ||
		errors.Is(err, ytdlp.ErrPaused) ||
		errors.Is(err, ytdlp.ErrCookieExpired) ||
		errors.Is(err, ytdlp.ErrBlocked)
}
