package media

import (
	"context"
	"errors"
	"fmt"
)

// A pending (inbox) video has no downloaded media yet, so unlike a finished
// video its thumbnail is not written by yt-dlp. FetchPendingThumbnail pulls the
// remote thumbnail server-side so the browser only ever loads it from peeq —
// never directly from YouTube's CDN — and a broken remote variant never renders
// a broken-image glyph on the card.
//
// Since migration 0023 the caching itself belongs to the caller, which stores
// the bytes in pending_thumbnails; this file is only the candidate order.

// ImageFetcher downloads one image and returns its mime type and bytes.
// Production passes ytdlp.Runner.FetchImage, which makes the request a turn in
// the same serial queue as every yt-dlp call; FetchImageBytes is the bare wire
// fetch underneath it.
type ImageFetcher func(ctx context.Context, url string) (string, []byte, error)

// ytThumbHost is YouTube's image CDN origin, split out as a package var only so
// a test can point the hqdefault fallback at an httptest server instead of the
// real network.
var ytThumbHost = "https://i.ytimg.com"

// FetchPendingThumbnail downloads videoID's inbox poster through fetch and
// returns its mime and bytes for the caller to store.
//
// recordedURL is the variant the scan captured (usually the largest, which is
// exactly the one that 404s when maxresdefault was never generated). hqdefault
// is appended as a fallback because YouTube generates it for EVERY video, so it
// is the guaranteed floor that makes "an inbox video always has a thumbnail"
// actually hold.
//
// Each candidate is asked ONCE. Every request is a turn in the YouTube queue
// (20s+ apart), so a retry is never cheap; a transient failure moves on to the
// fallback, and a poster that still fails is asked for again when the inbox
// next shows its card, after a back-off (scan.QueueThumbnail).
func FetchPendingThumbnail(ctx context.Context, fetch ImageFetcher, videoID, recordedURL string) (string, []byte, error) {
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
		mime, data, err := fetch(ctx, url)
		if err == nil {
			return mime, data, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		// A refusal (paused, no cookie) was never sent and would meet the
		// fallback the same way. Anything else — a status, a non-image or
		// oversize body, a timeout — is about this url, and the smaller
		// fallback may well work.
		if isRefusal(err) {
			break
		}
	}

	// candidates always holds at least the hqdefault url (videoID is non-empty
	// past the guard above), so the loop ran and lastErr is set.
	return "", nil, fmt.Errorf("pending thumbnail %s: %w", videoID, lastErr)
}

// IsCDNRefusal reports whether err is the CDN saying this url has no image —
// a 4xx, or a body that is not an image. Unlike a 5xx or a network failure it
// will not change on the next ask, so it is the only failure worth
// remembering.
func IsCDNRefusal(err error) bool {
	var se *FetchStatusError
	if errors.As(err, &se) {
		return se.StatusCode >= 400 && se.StatusCode < 500
	}
	return errors.Is(err, ErrUnsupportedContentType)
}

// isRefusal reports whether err says the request was never sent (YouTube
// calls paused, no valid cookie): ytdlp.RefusedError, seen through the
// interface it implements because media cannot import ytdlp.
func isRefusal(err error) bool {
	var r interface{ Refused() bool }
	return errors.As(err, &r) && r.Refused()
}
