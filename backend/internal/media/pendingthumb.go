package media

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// A pending (inbox) video has no downloaded media yet, so unlike a finished
// video its thumbnail is not written by yt-dlp. FetchPendingThumbnail pulls the
// remote thumbnail server-side so the browser only ever loads it from peeq —
// never directly from YouTube's CDN — and a broken remote variant never renders
// a broken-image glyph on the card.
//
// Since migration 0023 the caching itself belongs to the caller, which stores
// the bytes in pending_thumbnails; this file is only the fetch, its candidate
// order and its retry taxonomy.

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

// PendingThumbDir is where a pending video's thumbnail was cached BEFORE
// migration 0023 moved it into the database. It survives for the import worker,
// which reads those files in and unlinks them.
func PendingThumbDir(videoID string) string {
	return ".pending/" + videoID
}

// PendingThumbExts are the extensions the pre-0023 cache was written under, in
// the order the import worker should look for them.
var PendingThumbExts = []string{".jpg", ".png", ".webp"}

// FetchPendingThumbnail downloads videoID's inbox poster and returns its mime
// and bytes for the caller to store.
//
// recordedURL is the variant the scan captured (usually the largest, which is
// exactly the one that 404s when maxresdefault was never generated). hqdefault
// is appended as a fallback because YouTube generates it for EVERY video, so it
// is the guaranteed floor that makes "an inbox video always has a thumbnail"
// actually hold.
func FetchPendingThumbnail(ctx context.Context, videoID, recordedURL string) (string, []byte, error) {
	if videoID == "" {
		return "", nil, fmt.Errorf("pending thumbnail: empty video id")
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
			mime, data, err := FetchImageBytes(ctx, url)
			if err == nil {
				return mime, data, nil
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
					return "", nil, ctx.Err()
				case <-time.After(time.Duration(attempt) * pendingThumbBackoff):
				}
			}
		}
	}

	// candidates always holds at least the hqdefault url (videoID is non-empty
	// past the guard above), so the loop ran and lastErr is set.
	return "", nil, fmt.Errorf("pending thumbnail %s: %w", videoID, lastErr)
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
