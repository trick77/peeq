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

// release says which yt-dlp GitHub release asset a platform runs and where it
// lives inside the install directory.
type release struct {
	// asset is the GitHub release asset name.
	asset string
	// install is the entry the asset becomes in the install directory: the
	// binary itself, or, for a .zip asset, the directory it unpacks into.
	install string
	// exe is the executable inside the unpacked directory; empty for a single
	// binary, which is install itself.
	exe string
}

// unpacked reports whether the asset is a zip of a self-contained directory.
func (r release) unpacked() bool { return r.exe != "" }

// releaseFor returns the release a platform runs.
//
// Linux takes the UNPACKED self-contained build wherever yt-dlp publishes one:
//   - never the plain "yt-dlp" zipapp: it runs on the system python, which has
//     no curl_cffi, so yt-dlp cannot impersonate a browser. It asks to for
//     every caption download, and without it YouTube answered some videos'
//     captions with HTTP 429 on every attempt. The bundle carries the
//     curl_cffi its own release was tested with, so a self-update can never
//     leave the two out of step;
//   - never the one-file "yt-dlp_linux": it unpacks ~100MB into TMPDIR on
//     every run, which the container's noexec /tmp tmpfs refuses, and a
//     killed run leaves its copy behind.
//
// Other platforms keep the asset they always ran.
func releaseFor(goos, goarch string) release {
	single := func(name string) release { return release{asset: name, install: name} }
	switch goos {
	case "windows":
		return single("yt-dlp.exe")
	case "darwin":
		return single("yt-dlp_macos")
	case "linux":
		suffix := map[string]string{"amd64": "", "arm64": "_aarch64", "arm": "_armv7l"}
		if s, ok := suffix[goarch]; ok {
			exe := "yt-dlp_linux" + s
			return release{asset: exe + ".zip", install: "yt-dlp_linux", exe: exe}
		}
	}
	return single("yt-dlp")
}

// InstalledBin is the path the self-update installs this platform's yt-dlp
// executable at inside dir. resolveYtdlpBin (cmd/peeq) runs it when present.
func InstalledBin(dir string) string {
	r := releaseFor(runtime.GOOS, runtime.GOARCH)
	return filepath.Join(dir, r.install, r.exe)
}

const latestReleaseBaseURL = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"

// downloadLatestRelease downloads the latest yt-dlp release for the current
// platform from GitHub and replaces destPath with it, reporting the new
// version by running the freshly installed executable with --version. A
// single binary goes through downloadReleaseFrom, an unpacked build through
// downloadUnpackedFrom; both leave destPath untouched on any failure.
func downloadLatestRelease(ctx context.Context, destPath string) (string, error) {
	r := releaseFor(runtime.GOOS, runtime.GOARCH)
	if r.unpacked() {
		return downloadUnpackedFrom(ctx, latestReleaseBaseURL+r.asset, destPath, r.exe)
	}
	return downloadReleaseFrom(ctx, latestReleaseBaseURL+r.asset, destPath)
}

// fetchToTemp downloads url into a new temp file in dir and returns its path;
// the caller removes it. On any error nothing is left behind. dir is the
// install directory, so a later rename out of it stays on one filesystem.
func fetchToTemp(ctx context.Context, url, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("ytdlp: build download request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ytdlp: download latest release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ytdlp: download latest release: unexpected status %s", resp.Status)
	}

	tmp, err := os.CreateTemp(dir, ".yt-dlp-download-*")
	if err != nil {
		return "", fmt.Errorf("ytdlp: create temp download file: %w", err)
	}
	written, err := io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && resp.ContentLength >= 0 && written != resp.ContentLength {
		// The body was truncated (e.g. the connection dropped mid-download)
		// even though io.Copy itself didn't error.
		err = fmt.Errorf("ytdlp: download incomplete: wrote %d bytes, expected %d", written, resp.ContentLength)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("ytdlp: write download: %w", err)
	}
	return tmp.Name(), nil
}

// downloadReleaseFrom downloads the yt-dlp binary at url and atomically
// installs it at destPath.
//
// The download is written to a temp file in the same directory as destPath
// (so the final rename is on the same filesystem and therefore atomic),
// verified to have downloaded in full, made executable, and only then renamed
// over destPath. If anything fails along the way the temp file is removed and
// destPath (any pre-existing binary) is left completely untouched, so a failed
// self-update never leaves a truncated or corrupt binary in place.
func downloadReleaseFrom(ctx context.Context, url, destPath string) (string, error) {
	tmpPath, err := fetchToTemp(ctx, url, filepath.Dir(destPath))
	if err != nil {
		return "", err
	}
	// A no-op once the rename below succeeds.
	defer func() { _ = os.Remove(tmpPath) }()

	if err := os.Chmod(tmpPath, 0o755); err != nil { //nolint:gosec // an executable must be executable
		return "", fmt.Errorf("ytdlp: chmod downloaded binary: %w", err)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return "", fmt.Errorf("ytdlp: install downloaded binary: %w", err)
	}
	return Version(ctx, destPath)
}

// UpdateLatest downloads the latest yt-dlp release into dir (at the platform's
// release.install) and returns its version. The actual fetch is delegated to
// the package-level downloader variable so tests can inject a fake that
// writes a placeholder file and reports a version without any network
// access.
//
// Updates are serialized: two Update clicks must not interleave their swaps.
// A caller waiting its turn gives up when its ctx ends.
//
// Once an unpacked build is installed, a plain dir/yt-dlp an older peeq
// installed is removed: it is a python zipapp nothing resolves any more.
func UpdateLatest(ctx context.Context, dir string) (string, error) {
	select {
	case updateSlot <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-updateSlot }()

	r := releaseFor(runtime.GOOS, runtime.GOARCH)
	version, err := downloader(ctx, filepath.Join(dir, r.install))
	if err != nil {
		return "", err
	}
	if r.unpacked() {
		_ = os.Remove(filepath.Join(dir, "yt-dlp"))
	}
	return version, nil
}

// updateSlot holds one token while an update runs.
var updateSlot = make(chan struct{}, 1)
