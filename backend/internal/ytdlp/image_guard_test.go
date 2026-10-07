package ytdlp

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFetchImageBytes_onlyBehindTheQueue: every request to YouTube, images
// included, takes a turn in the Runner's serial queue. media.FetchImageBytes
// is the bare wire fetch, so outside tests only FetchImage may call it;
// anything else would talk to YouTube's CDN past the queue, as the poster
// prefetch and channel art once did.
func TestFetchImageBytes_onlyBehindTheQueue(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path) //nolint:gosec // walking this module's own sources
		if err != nil {
			return err
		}
		if !strings.Contains(string(src), "media.FetchImageBytes(") {
			return nil
		}
		if filepath.Base(path) == "image.go" && filepath.Base(filepath.Dir(path)) == "ytdlp" {
			return nil
		}
		t.Errorf("%s calls media.FetchImageBytes directly; fetch through ytdlp.Runner.FetchImage", path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
