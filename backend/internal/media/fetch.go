package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// FetchStatusError is returned when the remote server answers with a non-200
// status. It carries the code so a caller can tell a permanent 4xx (the
// variant doesn't exist — don't retry it) from a transient 5xx (worth a
// retry). Its message keeps the "status <code>" wording earlier code and
// tests rely on.
type FetchStatusError struct{ StatusCode int }

func (e *FetchStatusError) Error() string {
	return fmt.Sprintf("fetch image: status %d", e.StatusCode)
}

// ErrUnsupportedContentType is returned when the response is a 200 whose body
// is not one of the image types we store (e.g. an HTML error page). It is a
// permanent failure for that URL — retrying the same URL cannot fix it.
var ErrUnsupportedContentType = errors.New("fetch image: unsupported content type")

// maxImageBytes caps a fetched channel image. Avatars and banners are well
// under this; the cap exists so a hostile or broken server cannot fill the
// disk through this path.
const maxImageBytes = 8 << 20 // 8 MiB

// imageExts maps the content types YouTube serves channel art as to the
// extension we store it under. A response whose type is not in this map is
// rejected — an HTML error page served with a 200 must never be written to
// disk as if it were an avatar.
var imageExts = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
}

// FetchImageBytes downloads url and returns the image bytes plus the content
// type to serve them as. Nothing is written to disk: since migration 0023 every
// image peeq caches — channel avatars and banners, inbox posters — lives in the
// database, so the bytes go straight to a row.
//
// An empty url is a no-op returning ("", nil, nil): a channel with no banner is
// normal, not an error.
//
// The response is vetted before it is accepted: a non-200 becomes a
// *FetchStatusError so the caller can tell a permanent 404 from a transient
// blip, a content type outside imageExts is refused (an HTML error page served
// with a 200 must never be stored as if it were an avatar), and the body is
// read one byte past the cap so an oversize response is detected rather than
// silently truncated.
func FetchImageBytes(ctx context.Context, url string) (string, []byte, error) {
	if url == "" {
		return "", nil, nil
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", nil, fmt.Errorf("fetch image: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("fetch image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, &FetchStatusError{StatusCode: resp.StatusCode}
	}

	ctype := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if _, ok := imageExts[strings.ToLower(ctype)]; !ok {
		return "", nil, fmt.Errorf("%w %q", ErrUnsupportedContentType, ctype)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("fetch image: %w", err)
	}
	if len(body) > maxImageBytes {
		return "", nil, fmt.Errorf("fetch image: body exceeds %d bytes", maxImageBytes)
	}
	return strings.ToLower(ctype), body, nil
}
