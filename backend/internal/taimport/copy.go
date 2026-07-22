package taimport

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// statSize returns a regular file's size and whether it exists. A missing file,
// a directory, or any stat error all report ok=false — used both for the
// dry-run "would copy" sizing and for the trap-4 check that a video's .mp4 is
// actually on disk before its row is marked downloaded.
func statSize(path string) (size int64, ok bool) {
	if path == "" {
		return 0, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	return info.Size(), true
}

// copyFile copies src to dst, creating dst's parent directory, and verifies the
// copied byte count matches the source. It is idempotent: if dst already exists
// with the same size the copy is skipped, so a re-run does not rewrite files. A
// missing src returns an error wrapping fs.ErrNotExist so callers can tell "file
// gone" (skip the video) from a real failure (abort the run).
func copyFile(src, dst string) (int64, error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("taimport: source %s: %w", src, fs.ErrNotExist)
		}
		return 0, fmt.Errorf("taimport: stat source %s: %w", src, err)
	}
	if !srcInfo.Mode().IsRegular() {
		return 0, fmt.Errorf("taimport: source %s is not a regular file", src)
	}
	if dstInfo, err := os.Stat(dst); err == nil && dstInfo.Size() == srcInfo.Size() {
		return srcInfo.Size(), nil // already copied
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, fmt.Errorf("taimport: mkdir for %s: %w", dst, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("taimport: open source %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return 0, fmt.Errorf("taimport: create %s: %w", dst, err)
	}
	n, err := io.Copy(out, in)
	if err != nil {
		_ = out.Close()
		return 0, fmt.Errorf("taimport: copy to %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		return 0, fmt.Errorf("taimport: close %s: %w", dst, err)
	}
	if n != srcInfo.Size() {
		return 0, fmt.Errorf("taimport: copied %d bytes of %s, expected %d", n, src, srcInfo.Size())
	}
	return n, nil
}
