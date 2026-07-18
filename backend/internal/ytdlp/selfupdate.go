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
// current platform from GitHub and writes it to destPath with execute
// permission. It reports the new version by running the freshly
// downloaded binary with --version.
func downloadLatestRelease(ctx context.Context, destPath string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseBaseURL+binaryName(), nil)
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

	f, err := os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return "", fmt.Errorf("ytdlp: create destination file: %w", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return "", fmt.Errorf("ytdlp: write downloaded binary: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("ytdlp: close downloaded binary: %w", err)
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
