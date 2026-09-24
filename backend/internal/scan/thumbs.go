package scan

import (
	"context"
	"sync"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/media"
)

// prefetchQueueSize bounds how many newly-pending thumbnails can wait for the
// drainers. A scan pass lists at most a few dozen entries per channel, so the
// queue is rarely more than a few deep. When it is full the OLDEST waiting
// job is dropped for the new one: the newest uploads are the ones at the top
// of the inbox, and a dropped poster is fetched on demand by the serve
// endpoint — the same self-healing path a failed prefetch already takes.
const prefetchQueueSize = 64

// prefetchDrainers is how many posters are fetched at once. More than one, so
// a single slow CDN response (each job may spend up to
// pendingThumbPrefetchTimeout across its retries) does not hold every later
// poster behind it; few, so a pass with many uploads never bursts at
// i.ytimg.com the way one detached goroutine per video used to.
const prefetchDrainers = 2

// thumbJob is one pending video whose poster a drainer should cache.
type thumbJob struct {
	videoID string
	url     string
}

// queueThumbnail hands a newly-pending video's poster to the drainers without
// blocking the scan. It replaced a detached `go` per video: unbounded, on a
// background context, and outside any WaitGroup, so a pass with many pending
// uploads burst parallel requests at i.ytimg.com and a shutdown could leave a
// prefetch writing to a database that main had already closed.
func (s *Scheduler) queueThumbnail(videoID, url string) {
	job := thumbJob{videoID: videoID, url: url}
	select {
	case s.thumbs <- job:
		return
	default:
	}
	// Full: make room by dropping the oldest, then queue this one. Info, not
	// Debug — sustained drops mean the CDN is slower than the scans, which an
	// operator at the default level should be able to see.
	select {
	case dropped := <-s.thumbs:
		s.d.Logger.Info("scan: thumbnail prefetch queue full; leaving the oldest to the serve endpoint", "video_id", dropped.videoID)
	default:
	}
	select {
	case s.thumbs <- job:
	default:
		s.d.Logger.Info("scan: thumbnail prefetch queue full; leaving it to the serve endpoint", "video_id", videoID)
	}
}

// drainThumbnails fetches queued posters until ctx is cancelled. Run starts
// prefetchDrainers of these and waits for them, so none can outlive the
// process's database handle.
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
// on its ledger row. Best-effort: a failure is logged and left for the serve
// endpoint to retry on demand. A fetch cut short by shutdown is not logged as
// a failure and writes nothing.
//
// The row is re-read first. A job can sit in the queue for a while, and the
// world moves meanwhile: the user may have ignored or queued the video (which
// deletes its poster cache on purpose — see the serve endpoint's refusal to
// recreate it), or the serve endpoint may already have fetched the poster.
// Either way there is nothing to do, and doing it anyway would leak a blob the
// primary reclaim path has already run for.
func (s *Scheduler) prefetchPendingThumbnail(ctx context.Context, j thumbJob) {
	entry, err := s.d.Ledger.Get(j.videoID)
	if err != nil || entry == nil || entry.State != channelvideos.StatePending {
		return
	}
	if have, err := s.d.Ledger.GetThumbnail(j.videoID); err == nil && have != nil {
		return
	}
	fctx, cancel := context.WithTimeout(ctx, pendingThumbPrefetchTimeout)
	defer cancel()
	mime, data, err := media.FetchPendingThumbnail(fctx, j.videoID, j.url)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		s.d.Logger.Warn("scan: prefetch pending thumbnail failed", "video_id", j.videoID, "err", err)
		return
	}
	if err := s.d.Ledger.SetThumbnail(j.videoID, mime, data); err != nil {
		s.d.Logger.Warn("scan: store pending thumbnail failed", "video_id", j.videoID, "err", err)
	}
}

// runThumbnailDrainers starts prefetchDrainers drainers and returns a func that
// waits for all of them to stop.
func (s *Scheduler) runThumbnailDrainers(ctx context.Context) (wait func()) {
	var wg sync.WaitGroup
	for range prefetchDrainers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.drainThumbnails(ctx)
		}()
	}
	return wg.Wait
}
