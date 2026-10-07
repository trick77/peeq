package scan

import (
	"context"
	"errors"
	"time"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/ytdlp"
)

// prefetchQueueSize bounds how many newly-pending thumbnails can wait for the
// drainer. A scan pass lists at most a few dozen entries per channel, so the
// queue is rarely more than a few deep. When it is full the OLDEST waiting
// job is dropped for the new one: the newest uploads are the ones at the top
// of the inbox, and a dropped poster is queued again when the inbox asks for
// it (QueueThumbnail).
const prefetchQueueSize = 64

// thumbRetryAfter is how long a poster whose fetch failed is left alone. The
// inbox asks for every uncached poster on every load, and each attempt costs
// up to two turns in the YouTube queue; a poster YouTube does not serve must
// not cost them on every visit.
const thumbRetryAfter = 6 * time.Hour

// thumbJob is one pending video whose poster the drainer should cache.
type thumbJob struct {
	videoID string
	url     string
}

// QueueThumbnail asks for videoID's inbox poster to be fetched and cached in
// the background. The inbox's poster endpoint calls it for a poster it does
// not have, instead of fetching on the request: every poster request is a
// turn in the YouTube queue, and a page of uncached cards would otherwise hold
// a request open per card for as long as the queue takes. A poster already
// waiting, or one that failed within thumbRetryAfter, is not queued again.
func (s *Scheduler) QueueThumbnail(videoID, url string) {
	if s.d.Images == nil {
		return
	}
	s.queueThumbnail(videoID, url)
}

// queueThumbnail hands a newly-pending video's poster to the drainer without
// blocking the scan. It replaced a detached `go` per video: unbounded, on a
// background context, and outside any WaitGroup, so a pass with many pending
// uploads burst parallel requests at i.ytimg.com and a shutdown could leave a
// prefetch writing to a database that main had already closed.
func (s *Scheduler) queueThumbnail(videoID, url string) {
	s.thumbMu.Lock()
	failedAt, failed := s.thumbFailed[videoID]
	if s.thumbWaiting[videoID] || (failed && s.d.Now().Sub(failedAt) < thumbRetryAfter) {
		s.thumbMu.Unlock()
		return
	}
	s.thumbWaiting[videoID] = true
	s.thumbMu.Unlock()

	job := thumbJob{videoID: videoID, url: url}
	select {
	case s.thumbs <- job:
		return
	default:
	}
	// Full: make room by dropping the oldest, then queue this one. Info, not
	// Debug — sustained drops mean posters arrive faster than the YouTube queue
	// lets them be fetched, which an operator at the default level should see.
	select {
	case dropped := <-s.thumbs:
		s.thumbDone(dropped.videoID, false)
		s.d.Logger.Info("scan: thumbnail prefetch queue full; dropping the oldest", "video_id", dropped.videoID)
	default:
	}
	select {
	case s.thumbs <- job:
	default:
		s.thumbDone(videoID, false)
		s.d.Logger.Info("scan: thumbnail prefetch queue full; dropping it", "video_id", videoID)
	}
}

// thumbDone takes videoID off the waiting set and, when failed, remembers when.
func (s *Scheduler) thumbDone(videoID string, failed bool) {
	s.thumbMu.Lock()
	defer s.thumbMu.Unlock()
	delete(s.thumbWaiting, videoID)
	if failed {
		s.thumbFailed[videoID] = s.d.Now()
	} else {
		delete(s.thumbFailed, videoID)
	}
}

// drainThumbnails fetches queued posters until ctx is cancelled. Run starts
// one and waits for it, so it cannot outlive the process's database handle.
//
// One, not several: each fetch is a turn in the serial YouTube queue, so a
// second drainer would only hold a second place in that queue.
func (s *Scheduler) drainThumbnails(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.thumbs:
			s.prefetchPendingThumbnail(ctx, j)
		}
	}
}

// prefetchPendingThumbnail fetches one pending video's thumbnail and caches it
// on its ledger row. Best-effort: a failure is logged and the poster is left
// alone for thumbRetryAfter. A fetch cut short by shutdown, or refused because
// YouTube calls are paused or the cookie is not valid, is not a failure of
// this poster: it is logged at debug and may be asked for again at once.
//
// The row is re-read first. A job can sit in the queue for a while, and the
// world moves meanwhile: the user may have ignored or queued the video (which
// deletes its poster cache on purpose — see the serve endpoint's refusal to
// recreate it), or an earlier job may already have fetched the poster.
// Either way there is nothing to do, and doing it anyway would leak a blob the
// primary reclaim path has already run for.
//
// No timeout of its own: the wait for a turn can last as long as the download
// in front of it, and each request is bounded by FetchImageBytes' own.
func (s *Scheduler) prefetchPendingThumbnail(ctx context.Context, j thumbJob) {
	failed := false
	defer func() { s.thumbDone(j.videoID, failed) }()

	entry, err := s.d.Ledger.Get(j.videoID)
	if err != nil || entry == nil || entry.State != channelvideos.StatePending {
		return
	}
	if have, err := s.d.Ledger.HasThumbnail(j.videoID); err == nil && have {
		return
	}
	mime, data, err := media.FetchPendingThumbnail(ctx, s.d.Images, j.videoID, j.url)
	if ctx.Err() != nil {
		return
	}
	var refused *ytdlp.RefusedError
	if errors.As(err, &refused) {
		s.d.Logger.Debug("scan: prefetch pending thumbnail refused", "video_id", j.videoID, "err", err)
		return
	}
	if err != nil {
		failed = true
		s.d.Logger.Warn("scan: prefetch pending thumbnail failed", "video_id", j.videoID, "err", err)
		return
	}
	if err := s.d.Ledger.SetThumbnail(j.videoID, mime, data); err != nil {
		s.d.Logger.Warn("scan: store pending thumbnail failed", "video_id", j.videoID, "err", err)
	}
}

// runThumbnailDrainers starts the drainer and returns a func that waits for
// it to stop.
func (s *Scheduler) runThumbnailDrainers(ctx context.Context) (wait func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.drainThumbnails(ctx)
	}()
	return func() { <-done }
}
