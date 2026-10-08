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

// thumbRetryAfter is how long a poster the CDN said it does not have (a 4xx,
// a non-image body) is left alone, and thumbTransientRetry how long one is
// after a failure that may clear (a 5xx, a network error, a timeout). The
// inbox asks for every uncached poster on every load, and each attempt costs
// up to two turns in the YouTube queue; neither kind may cost them on every
// visit.
const (
	thumbRetryAfter     = 6 * time.Hour
	thumbTransientRetry = 30 * time.Minute
)

// thumbJob is one pending video whose poster the drainer should cache.
type thumbJob struct {
	videoID string
	url     string
}

// QueueThumbnail asks for videoID's inbox poster to be fetched and cached in
// the background. The inbox's poster endpoint calls it for a poster it does
// not have, instead of fetching on the request: every poster request is a
// turn in the YouTube queue, and a page of uncached cards would otherwise hold
// a request open per card for as long as the queue takes.
//
// It reports whether the poster is on its way: queued now or already
// waiting. Not queued, and false: one backing off after a failure
// (thumbRetryAfter or thumbTransientRetry), any poster while YouTube calls are
// refused (paused, or no valid cookie — it would only be refused again), and
// any poster while the queue is full. The page asks for posters top to
// bottom, so letting a request evict the oldest job would push out the newest
// uploads at the top of the inbox; a later page load asks again.
func (s *Scheduler) QueueThumbnail(videoID, url string) bool {
	if s.d.Images == nil {
		return false
	}
	claimed, waiting := s.markWaiting(videoID)
	if !claimed {
		return waiting
	}
	// Checked after the in-memory claim: the gate reads the database, and
	// most asks are for a poster already waiting or backing off.
	if s.d.CookieStatus != nil && !s.gate.Open(context.Background()) {
		s.thumbDone(videoID, 0)
		return false
	}
	select {
	case s.thumbs <- thumbJob{videoID: videoID, url: url}:
		return true
	default:
		s.thumbDone(videoID, 0)
		return false
	}
}

// thumbPruneAt is how many back-offs may pile up before markWaiting sweeps
// the expired ones out.
const thumbPruneAt = 256

// markWaiting claims videoID for the queue. claimed is false when it is
// already waiting (waiting true) or backing off after a failure. Only the
// asked-for id's back-off is checked; the whole map is swept for expired ones
// only once it has grown, so a page of posters costs no full scans.
func (s *Scheduler) markWaiting(videoID string) (claimed, waiting bool) {
	s.thumbMu.Lock()
	defer s.thumbMu.Unlock()
	now := s.d.Now()
	if s.thumbWaiting[videoID] {
		return false, true
	}
	if at, ok := s.thumbRetryAt[videoID]; ok {
		if now.Before(at) {
			return false, false
		}
		delete(s.thumbRetryAt, videoID)
	}
	if len(s.thumbRetryAt) > thumbPruneAt {
		for id, at := range s.thumbRetryAt {
			if !now.Before(at) {
				delete(s.thumbRetryAt, id)
			}
		}
	}
	s.thumbWaiting[videoID] = true
	return true, false
}

// queueThumbnail hands a newly-pending video's poster to the drainer without
// blocking the scan. It replaced a detached `go` per video: unbounded, on a
// background context, and outside any WaitGroup, so a pass with many pending
// uploads burst parallel requests at i.ytimg.com and a shutdown could leave a
// prefetch writing to a database that main had already closed.
//
// A scan's discoveries are the newest uploads, so when the queue is full the
// OLDEST waiting job makes room for this one.
func (s *Scheduler) queueThumbnail(videoID, url string) {
	if claimed, _ := s.markWaiting(videoID); !claimed {
		return
	}
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
		s.thumbDone(dropped.videoID, 0)
		s.d.Logger.Info("scan: thumbnail prefetch queue full; dropping the oldest", "video_id", dropped.videoID)
	default:
	}
	select {
	case s.thumbs <- job:
	default:
		s.thumbDone(videoID, 0)
		s.d.Logger.Info("scan: thumbnail prefetch queue full; dropping it", "video_id", videoID)
	}
}

// thumbDone takes videoID off the waiting set and, for a backoff > 0, keeps
// it from being queued again until that has passed.
func (s *Scheduler) thumbDone(videoID string, backoff time.Duration) {
	s.thumbMu.Lock()
	defer s.thumbMu.Unlock()
	delete(s.thumbWaiting, videoID)
	if backoff > 0 {
		s.thumbRetryAt[videoID] = s.d.Now().Add(backoff)
	} else {
		delete(s.thumbRetryAt, videoID)
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
// on its ledger row. Best-effort: a failure is logged and the poster backs off
// (thumbRetryAfter, or thumbTransientRetry for one that may clear). A fetch cut short by shutdown, or refused because
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
	var backoff time.Duration
	defer func() { s.thumbDone(j.videoID, backoff) }()

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
		// The CDN saying this url has no image (4xx, non-image body) holds for
		// hours; a 5xx, a network blip or a timeout may clear much sooner.
		backoff = thumbTransientRetry
		if media.IsCDNRefusal(err) {
			backoff = thumbRetryAfter
		}
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
