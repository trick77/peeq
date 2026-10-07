package ytdlp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
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
	// exe is the executable, relative to the install directory.
	exe string
}

// unpacked reports whether the asset is a zip of a self-contained directory.
func (r release) unpacked() bool { return strings.HasSuffix(r.asset, ".zip") }

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
	single := func(name string) release { return release{asset: name, install: name, exe: name} }
	switch goos {
	case "windows":
		return single("yt-dlp.exe")
	case "darwin":
		return single("yt-dlp_macos")
	case "linux":
		suffix := map[string]string{"amd64": "", "arm64": "_aarch64", "arm": "_armv7l"}
		if s, ok := suffix[goarch]; ok {
			exe := "yt-dlp_linux" + s
			return release{asset: exe + ".zip", install: "yt-dlp_linux", exe: "yt-dlp_linux/" + exe}
		}
	}
	return single("yt-dlp")
}

// InstalledBin is the path the self-update installs this platform's yt-dlp
// executable at inside dir. resolveYtdlpBin (cmd/peeq) runs it when present.
func InstalledBin(dir string) string {
	return filepath.Join(dir, filepath.FromSlash(releaseFor(runtime.GOOS, runtime.GOARCH).exe))
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
		return downloadUnpackedFrom(ctx, latestReleaseBaseURL+r.asset, destPath, path.Base(r.exe))
	}
	return downloadReleaseFrom(ctx, latestReleaseBaseURL+r.asset, destPath)
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
	defer func() { _ = resp.Body.Close() }()
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
	defer func() { _ = os.Remove(tmpPath) }()

	written, err := io.Copy(tmp, resp.Body)
	if err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("ytdlp: write downloaded binary: %w", err)
	}
	// A Content-Length mismatch means the body was truncated (e.g. the
	// connection dropped mid-download) even though io.Copy itself didn't
	// error. Catch that before it ever reaches destPath.
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		_ = tmp.Close()
		return "", fmt.Errorf("ytdlp: download incomplete: wrote %d bytes, expected %d", written, resp.ContentLength)
	}

	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
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

// UpdateLatest downloads the latest yt-dlp release into dir (at the platform's
// release.install) and returns its version. The actual fetch is delegated to
// the package-level downloader variable so tests can inject a fake that
// writes a placeholder file and reports a version without any network
// access.
func UpdateLatest(ctx context.Context, dir string) (string, error) {
	destPath := filepath.Join(dir, releaseFor(runtime.GOOS, runtime.GOARCH).install)
	version, err := downloader(ctx, destPath)
	if err != nil {
		return "", err
	}
	return version, nil
}
