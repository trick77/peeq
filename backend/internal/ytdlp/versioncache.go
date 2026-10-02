package ytdlp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// VersionCache answers Version without starting the binary every time.
//
// `yt-dlp --version` boots a Python interpreter, which is most of a second on
// a small host, and the SPA asks on every page load. The answer can only
// change when the file does, so it is remembered against the file's path,
// size and modification time: an update through the Settings page, a manual
// replacement and an image upgrade all change one of those and are seen on the
// next read, with nobody having to invalidate anything.
//
// The zero value is ready to use.
type VersionCache struct {
	mu      sync.Mutex
	key     string
	version string
}

// Version returns bin's version, running it only when the file behind bin is
// not the one last read. Failures are returned and never remembered.
func (c *VersionCache) Version(ctx context.Context, bin string) (string, error) {
	key := binaryKey(bin)
	c.mu.Lock()
	defer c.mu.Unlock()
	if key != "" && key == c.key {
		return c.version, nil
	}
	version, err := Version(ctx, bin)
	if err != nil {
		return "", err
	}
	c.key, c.version = key, version
	return version, nil
}

// binaryKey identifies the file bin resolves to, or "" when it cannot be
// found — which disables the cache for that call rather than guessing.
func binaryKey(bin string) string {
	path, err := exec.LookPath(bin)
	if err != nil {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s|%d|%d", path, fi.Size(), fi.ModTime().UnixNano())
}
