// Package captionrefetch fetches the captions of a library video that has
// none, on the user's request.
//
// A downloaded video ends up without a transcript when the caption call failed
// or found nothing at download time. Reprocess needs a transcript, and
// re-download is refused for a downloaded video, so this is the one way to get
// the text without fetching the media again: one caption call through the
// Runner's shared YouTube queue, then the analysis re-runs as Reprocess would,
// except that a hand-picked category stays.
//
// Nothing here is persisted. A restart mid-fetch loses the request and the
// user clicks again; the in-flight set only stops a double click from queueing
// the same call twice.
package captionrefetch

import (
	"context"
	"log/slog"
	"sync"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/videos"
)

// Fetcher fetches only the captions for one video and returns their
// MediaDir-relative path and spoken language, or "" when YouTube has none.
// *ytdlp.Runner implements it.
type Fetcher interface {
	Subtitles(ctx context.Context, videoID, rawURL, prefer string, anyManual bool) (relPath, lang string, err error)
}

// VideoStore is the slice of videos.Store the fetch needs.
type VideoStore interface {
	Get(id string) (*videos.Video, error)
	StoreFetchedCaptions(id, vtt, audioLang string) (int64, error)
}

// Deps are the service's collaborators. Fetcher and Videos are required.
type Deps struct {
	Fetcher Fetcher
	Videos  VideoStore
	// MediaDir is where the fetched caption lands before it is read into the
	// row.
	MediaDir string
	// DefaultSubLang is the preferred caption language, the download worker's:
	// the transcript this stores stands in for the one a download would have.
	DefaultSubLang string
	// Activity records each outcome; the user has no other way to learn that
	// YouTube had no captions. Optional.
	Activity activity.Recorder
	// Background is the process lifetime. A fetch outlives the request that
	// started it and stops waiting for its turn at shutdown. nil means never.
	Background context.Context
	// OnDone fires when a fetch settles, after its in-flight claim is
	// released. Test-only.
	OnDone func(videoID string)
	Logger *slog.Logger
}

// Service runs caption fetches in the background, one per video at a time.
type Service struct {
	d Deps

	mu       sync.Mutex
	inFlight map[string]bool
	wg       sync.WaitGroup
}

// New builds a Service, filling in defaults for the optional Deps.
func New(d Deps) *Service {
	if d.DefaultSubLang == "" {
		d.DefaultSubLang = "en"
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Background == nil {
		d.Background = context.Background()
	}
	return &Service{d: d, inFlight: map[string]bool{}}
}

// Start claims videoID and fetches its captions in the background. It reports
// false, starting nothing, when a fetch for videoID is already in flight. The
// claim is taken here, not in the goroutine, so a double click's second
// request is refused deterministically.
func (s *Service) Start(videoID, rawURL string) bool {
	s.mu.Lock()
	if s.inFlight[videoID] {
		s.mu.Unlock()
		return false
	}
	s.inFlight[videoID] = true
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			// yt-dlp output is external input; contain a panic as every other
			// background goroutine in peeq does.
			if r := recover(); r != nil {
				s.d.Logger.Error("caption refetch: recovered from panic", "video_id", videoID, "panic", r)
			}
			s.mu.Lock()
			delete(s.inFlight, videoID)
			s.mu.Unlock()
			if s.d.OnDone != nil {
				s.d.OnDone(videoID)
			}
		}()
		s.fetch(s.d.Background, videoID, rawURL)
	}()
	return true
}

// Wait blocks until every started fetch has settled. main calls it after
// cancelling Background, so shutdown does not exit under a write.
func (s *Service) Wait() { s.wg.Wait() }
