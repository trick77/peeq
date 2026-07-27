package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// A pending (inbox) video has no downloaded media yet, so unlike a finished
// video its thumbnail is not written by yt-dlp. EnsurePendingThumbnail fetches
// the remote thumbnail server-side and caches it under mediaDir, so the browser
// only ever loads it from peeq — never directly from YouTube's CDN — and a
// broken remote variant never renders a broken-image glyph on the card.

// pendingThumbRetries is how many times a TRANSIENT fetch failure (a network
// error or a 5xx) is retried for one candidate URL before giving up on it. A
// permanent failure (a 4xx, or a 200 that isn't an image) is not retried — the
// next candidate is tried instead.
const pendingThumbRetries = 3

// pendingThumbBackoff is the base backoff between transient retries; attempt N
// waits N×this so a brief CDN blip clears before the next try.
const pendingThumbBackoff = 300 * time.Millisecond

// ytThumbHost is YouTube's image CDN origin, split out as a package var only so
// a test can point the hqdefault fallback at an httptest server instead of the
// real network.
var ytThumbHost = "https://i.ytimg.com"

// pendingThumbBase is the mediaDir-relative destination (WITHOUT extension, as
// FetchImage wants) for a pending video's cached thumbnail. Dot-prefixed so it
// sits outside the <channelID>/<videoID>/ downloaded-media tree, consistent
// with .channels/ and .staging/.
func pendingThumbBase(videoID string) string {
	return ".pending/" + videoID + "/thumbnail"
}

// PendingThumbDir is the directory holding one pending video's cached
// thumbnail, for callers that need to remove it (on ignore or download).
func PendingThumbDir(videoID string) string {
	return ".pending/" + videoID
}

// EnsurePendingThumbnail returns the mediaDir-relative path to videoID's cached
// thumbnail, fetching and storing it first if it isn't on disk yet. It self-
// heals: an entry cached before this existed (or whose file vanished) is
// re-fetched on demand.
//
// recordedURL is the variant the scan captured (usually the largest, which is
// exactly the one that 404s when maxresdefault was never generated). hqdefault
// is appended as a fallback because YouTube generates it for EVERY video, so it
// is the guaranteed floor that makes "an inbox video always has a thumbnail"
// actually hold.
func EnsurePendingThumbnail(ctx context.Context, mediaDir, videoID, recordedURL string) (string, error) {
	if mediaDir == "" {
		return "", fmt.Errorf("pending thumbnail: media dir not configured")
	}
	if videoID == "" {
		return "", fmt.Errorf("pending thumbnail: empty video id")
	}

	base := pendingThumbBase(videoID)

	// Cache hit: a previous fetch already stored it under one of the extensions
	// FetchImage writes.
	for _, ext := range []string{".jpg", ".png", ".webp"} {
		rel := base + ext
		if safe, err := SafeMediaPath(mediaDir, rel); err == nil {
			if _, statErr := os.Stat(safe); statErr == nil {
				return rel, nil
			}
		}
	}

	// Candidate order: the recorded variant first (it's the largest when it
	// exists), then hqdefault as the guaranteed fallback. Deduped so a recorded
	// URL that already IS hqdefault isn't fetched twice.
	candidates := make([]string, 0, 2)
	if recordedURL != "" {
		candidates = append(candidates, recordedURL)
	}
	hq := ytThumbHost + "/vi/" + videoID + "/hqdefault.jpg"
	if recordedURL != hq {
		candidates = append(candidates, hq)
	}

	var lastErr error
	for _, url := range candidates {
		for attempt := 1; attempt <= pendingThumbRetries; attempt++ {
			rel, err := FetchImage(ctx, url, mediaDir, base)
			if err == nil {
				return rel, nil
			}
			lastErr = err
			// A 4xx / non-image response won't heal by asking again — move on to
			// the next candidate rather than burning retries on it.
			if isPermanentFetchError(err) {
				break
			}
			if attempt < pendingThumbRetries {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(time.Duration(attempt) * pendingThumbBackoff):
				}
			}
		}
	}

	if lastErr == nil {
		lastErr = errors.New("no candidate url")
	}
	return "", fmt.Errorf("pending thumbnail %s: %w", videoID, lastErr)
}

// isPermanentFetchError reports whether a FetchImage error cannot be fixed by
// retrying the SAME url: a 4xx status (the variant doesn't exist) or a 200 that
// wasn't an image. Everything else (network errors, 5xx, timeouts) is treated
// as transient and worth a retry.
func isPermanentFetchError(err error) bool {
	var se *FetchStatusError
	if errors.As(err, &se) {
		return se.StatusCode >= 400 && se.StatusCode < 500
	}
	return errors.Is(err, ErrUnsupportedContentType)
}
