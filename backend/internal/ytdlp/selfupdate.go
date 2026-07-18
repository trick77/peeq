package ytdlp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Version runs `<bin> --version` and returns the trimmed version string
// yt-dlp prints (e.g. "2024.07.01"). It does not go through the cookie
// gate: version reporting never touches YouTube.
func Version(ctx context.Context, bin string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, "--version")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ytdlp: version: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// releaseDownloader fetches the latest yt-dlp release binary for the
// current platform and writes it to destPath, returning the version it
// downloaded.
type releaseDownloader func(ctx context.Context, destPath string) (version string, err error)

// downloader is the seam UpdateLatest calls through. Tests reassign this
// package variable to a fake so self-update tests never touch the
// network; production code leaves it at downloadLatestRelease.
var downloader releaseDownloader = downloadLatestRelease

// binaryName returns the yt-dlp release asset name for the current
// platform, matching yt-dlp's own GitHub release naming.
func binaryName() string {
	switch runtime.GOOS {
	case "windows":
		return "yt-dlp.exe"
	case "darwin":
		return "yt-dlp_macos"
	default:
		return "yt-dlp"
	}
}

const latestReleaseBaseURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"

// downloadLatestRelease downloads the latest yt-dlp release binary for the
// current platform from GitHub and atomically replaces destPath with it.
// It reports the new version by running the freshly downloaded binary
// with --version.
//
// The download is written to a temp file in the same directory as
// destPath (so the final rename is on the same filesystem and therefore
// atomic), verified to have downloaded in full, made executable, and only
// then renamed over destPath. If anything fails along the way — a
// non-200 status, a short/interrupted body, a rename failure — the temp
// file is removed and destPath (any pre-existing binary) is left
// completely untouched. This avoids ever leaving a truncated or corrupt
// binary in place after a failed self-update.
func downloadLatestRelease(ctx context.Context, destPath string) (string, error) {
	return downloadReleaseFrom(ctx, latestReleaseBaseURL+binaryName(), destPath)
}

// downloadReleaseFrom downloads the yt-dlp binary at url and atomically
// installs it at destPath, as described on downloadLatestRelease. Factored
// out from downloadLatestRelease so tests can point it at an
// httptest.Server instead of the real GitHub releases URL, without ever
// touching the network.
func downloadReleaseFrom(ctx context.Context, url, destPath string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("ytdlp: build download request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ytdlp: download latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ytdlp: download latest release: unexpected status %s", resp.Status)
	}

	destDir := filepath.Dir(destPath)
	tmp, err := os.CreateTemp(destDir, ".yt-dlp-download-*")
	if err != nil {
		return "", fmt.Errorf("ytdlp: create temp download file: %w", err)
	}
	tmpPath := tmp.Name()
	// Always clean up the temp file on any early return; once the rename
	// below succeeds this is a no-op (the file no longer exists at tmpPath).
	defer os.Remove(tmpPath)

	written, err := io.Copy(tmp, resp.Body)
	if err != nil {
		tmp.Close()
		return "", fmt.Errorf("ytdlp: write downloaded binary: %w", err)
	}
	// A Content-Length mismatch means the body was truncated (e.g. the
	// connection dropped mid-download) even though io.Copy itself didn't
	// error. Catch that before it ever reaches destPath.
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		tmp.Close()
		return "", fmt.Errorf("ytdlp: download incomplete: wrote %d bytes, expected %d", written, resp.ContentLength)
	}

	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", fmt.Errorf("ytdlp: chmod downloaded binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("ytdlp: close downloaded binary: %w", err)
	}

	// os.Rename is atomic within the same filesystem: destPath either has
	// the old binary or the fully-downloaded new one, never a partial
	// write, regardless of when a crash or failure occurs.
	if err := os.Rename(tmpPath, destPath); err != nil {
		return "", fmt.Errorf("ytdlp: install downloaded binary: %w", err)
	}

	return Version(ctx, destPath)
}

// UpdateLatest downloads the latest yt-dlp release binary into dir (as
// binaryName()) and returns its version. The actual fetch is delegated to
// the package-level downloader variable so tests can inject a fake that
// writes a placeholder file and reports a version without any network
// access.
func UpdateLatest(ctx context.Context, dir string) (string, error) {
	destPath := filepath.Join(dir, binaryName())
	version, err := downloader(ctx, destPath)
	if err != nil {
		return "", err
	}
	return version, nil
}
