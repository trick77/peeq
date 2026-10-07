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
		// Any mention in code, not just a call: handing the function on as a
		// value (an ImageFetcher) bypasses the queue just the same. Comment
		// lines may name it.
		inCode := false
		for _, line := range strings.Split(string(src), "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "//") && strings.Contains(line, "FetchImageBytes") {
				inCode = true
				break
			}
		}
		if !inCode {
			return nil
		}
		dir, base := filepath.Base(filepath.Dir(path)), filepath.Base(path)
		if (dir == "ytdlp" && base == "image.go") || (dir == "media" && base == "fetch.go") {
			return nil
		}
		t.Errorf("%s uses FetchImageBytes; fetch through ytdlp.Runner.FetchImage", path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
