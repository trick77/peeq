package captionrefetch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/trick77/peeq/internal/logx"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// fetch runs one caption call for videoID and, on a track, stores it and
// queues the analysis (videos.Store.StoreFetchedCaptions). Every other outcome
// leaves the video as it was (summary_status stays no_transcript).
func (s *Service) fetch(ctx context.Context, videoID, rawURL string) {
	// anyManual: a one-shot fetch like a download, so any uploaded track counts.
	relPath, lang, err := s.d.Fetcher.Subtitles(ctx, videoID, rawURL, s.d.DefaultSubLang, true)
	if err != nil {
		var retryable *ytdlp.RetryableError
		switch {
		case ctx.Err() != nil:
			// Shutdown, not this video failing. The user clicks again.
			s.d.Logger.Debug("caption refetch: stopped by shutdown", "video_id", videoID, "err", err)
		case refused(err):
			s.d.Logger.Info("caption refetch: refused", "video_id", videoID, "err", err)
			s.recordRefused(videoID, err)
		case errors.As(err, &retryable):
			s.d.Logger.Info("caption refetch: rate limited", "video_id", videoID, "err", err)
			s.recordRateLimited(videoID)
		default:
			s.d.Logger.Warn("caption refetch: fetch failed", "video_id", videoID, "err", err)
			s.recordFailed(videoID, err)
		}
		return
	}
	if relPath == "" {
		// The empty .summaries/<id> the call made stays: the Inbox caption
		// worker shares it (ytdlp.Runner.Subtitles).
		s.d.Logger.Info("caption refetch: no captions", "video_id", videoID)
		s.recordNoCaptions(videoID)
		return
	}
	// Whatever happens below, the file this call wrote goes: the text either
	// lands in the row or is not wanted.
	defer s.removeCaption(relPath)

	vtt, err := s.readCaption(relPath)
	if err != nil {
		s.d.Logger.Error("caption refetch: read caption failed", "video_id", videoID, "err", err)
		s.recordFailed(videoID, err)
		return
	}
	switch _, err := s.d.Videos.StoreFetchedCaptions(videoID, vtt, lang); {
	case errors.Is(err, videos.ErrCaptionsSuperseded), errors.Is(err, videos.ErrNotFound):
		// The call waited in the YouTube queue: a download or an analysis was
		// queued meanwhile, or the video was deleted. Nothing was written.
		s.d.Logger.Debug("caption refetch: video changed during the fetch; dropping the caption", "video_id", videoID, "err", err)
		return
	case err != nil:
		s.d.Logger.Error("caption refetch: save captions failed", "video_id", videoID, "err", err)
		s.recordFailed(videoID, err)
		return
	}
	s.d.Logger.Info("caption refetch: queued for summary", "video_id", videoID, "lang", lang)
	s.recordFetched(videoID, lang)
}

// readCaption reads the .vtt the fetch wrote.
func (s *Service) readCaption(relPath string) (string, error) {
	safe, err := media.SafeMediaPath(s.d.MediaDir, relPath)
	if err != nil {
		return "", fmt.Errorf("resolve caption path: %w", err)
	}
	data, err := os.ReadFile(safe) //nolint:gosec // path comes from media.SafeMediaPath, which rejects traversal and symlink escape and returns the resolved path (see safepath_test.go)
	if err != nil {
		return "", fmt.Errorf("read caption: %w", err)
	}
	return string(data), nil
}

// removeCaption removes the file this fetch wrote, then its directory only if
// that is now empty. Never RemoveAll: .summaries/<id> is the Inbox caption
// worker's too, and ytdlp.Runner.Subtitles creates it before waiting for its
// turn, so another fetch may be about to write there. os.Remove fails on a
// non-empty or missing directory, which is exactly when it must stay put.
func (s *Service) removeCaption(relPath string) {
	safe, err := media.SafeMediaPath(s.d.MediaDir, relPath)
	if err != nil {
		return
	}
	if err := os.Remove(safe); err != nil {
		return
	}
	_ = os.Remove(filepath.Dir(safe))
}

// refused reports a failure that applies to every YouTube call right now
// (pause, no cookie, expired cookie, bot block, this server refused), not to
// this video: the captionfetch worker and ytdlp's download draw the same line.
func refused(err error) bool {
	return ytdlp.IsRefused(err) ||
		errors.Is(err, ytdlp.ErrNoCookie) ||
		errors.Is(err, ytdlp.ErrPaused) ||
		errors.Is(err, ytdlp.ErrCookieExpired) ||
		errors.Is(err, ytdlp.ErrBlocked) ||
		errors.Is(err, ytdlp.ErrHostRefused)
}

// redacted is err's text fit to leave the logs.
func redacted(err error) string { return logx.RedactErr(err).Error() }
