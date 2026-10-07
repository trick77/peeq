package ytdlp

import (
	"context"

	"github.com/trick77/peeq/internal/media"
)

// FetchImage downloads an image from YouTube's CDN (an inbox poster, channel
// art) and returns its mime type and bytes, as media.FetchImageBytes does.
//
// An image request is a YouTube request like any yt-dlp call, so it goes
// through the same gates and the same queue: refused when paused or without a
// valid cookie, and run only when it holds the turn, never alongside a yt-dlp
// process and always a gap after the previous call. The cookie is only
// checked, never sent: the CDN does not need it. An empty url is a no-op that
// costs no turn, like media.FetchImageBytes.
//
// This is the only caller of media.FetchImageBytes outside tests.
func (r *Runner) FetchImage(ctx context.Context, url string) (string, []byte, error) {
	if url == "" {
		return "", nil, nil
	}
	if _, err := r.gates(); err != nil {
		return "", nil, &RefusedError{Err: err}
	}
	release, err := r.acquire(ctx)
	if err != nil {
		return "", nil, err
	}
	ran := false
	defer func() { release(ran) }()
	// Handed the turn as the caller gave up: request nothing, owe no gap.
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if _, err := r.gates(); err != nil {
		return "", nil, &RefusedError{Err: err}
	}
	ran = true
	return media.FetchImageBytes(ctx, url)
}
