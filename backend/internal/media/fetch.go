package media

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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

// FetchImage downloads url into mediaDir under relBase (a path relative to
// mediaDir, WITHOUT an extension — the extension comes from the response's
// content type) and returns the stored path relative to mediaDir.
//
// An empty url is a no-op returning ("", nil): a channel with no banner is
// normal, not an error.
func FetchImage(ctx context.Context, url, mediaDir, relBase string) (string, error) {
	if url == "" {
		return "", nil
	}
	if mediaDir == "" {
		return "", fmt.Errorf("fetch image: media dir not configured")
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch image: status %d", resp.StatusCode)
	}

	ctype := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	ext, ok := imageExts[strings.ToLower(ctype)]
	if !ok {
		return "", fmt.Errorf("fetch image: unsupported content type %q", ctype)
	}

	// Read one byte past the cap so an exactly-at-limit body still succeeds
	// while an oversize one is detected rather than silently truncated.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	if len(body) > maxImageBytes {
		return "", fmt.Errorf("fetch image: body exceeds %d bytes", maxImageBytes)
	}

	rel := relBase + ext
	dest := filepath.Join(mediaDir, rel)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	// Write to a temp file and rename, so a reader never observes a partial
	// image and a failed write cannot leave a corrupt one behind.
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("fetch image: %w", err)
	}
	return rel, nil
}
