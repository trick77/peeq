package ytdlp

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// maxUnpackedBytes caps what one release archive may unpack to. The real
// build is ~100MB; the cap only stops a broken or hostile archive from
// filling the data volume.
const maxUnpackedBytes = 1 << 30

// oldTreePrefix names a replaced install kept beside the new one.
const oldTreePrefix = ".yt-dlp-old-"

// downloadUnpackedFrom downloads the zipped self-contained build at url and
// installs it as the directory destDir, whose executable is destDir/exe. It
// returns the version the new executable reports.
//
// Everything happens beside destDir, on the same filesystem, and destDir is
// only touched once the new tree is complete and has answered --version: the
// archive goes to a temp file, unpacks into a temp directory, runs, and is
// then renamed into place. Any failure before that leaves the working install
// as it was and removes the temp files.
//
// The swap is two renames, so for an instant destDir does not exist; a call
// resolving the binary then falls back to the image's copy on PATH, which is
// the same build.
//
// The replaced tree is renamed aside, not deleted: a yt-dlp still running from
// it (a long download) loads modules from _internal/ as it goes, and deleting
// them under it would fail that run. It is removed by the next update.
func downloadUnpackedFrom(ctx context.Context, url, destDir, exe string) (string, error) {
	parent := filepath.Dir(destDir)

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

	archive, err := os.CreateTemp(parent, ".yt-dlp-download-*")
	if err != nil {
		return "", fmt.Errorf("ytdlp: create temp download file: %w", err)
	}
	archivePath := archive.Name()
	defer func() { _ = os.Remove(archivePath) }()
	written, err := io.Copy(archive, resp.Body)
	if cerr := archive.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", fmt.Errorf("ytdlp: write downloaded archive: %w", err)
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		return "", fmt.Errorf("ytdlp: download incomplete: wrote %d bytes, expected %d", written, resp.ContentLength)
	}

	staging, err := os.MkdirTemp(parent, ".yt-dlp-unpack-*")
	if err != nil {
		return "", fmt.Errorf("ytdlp: create unpack dir: %w", err)
	}
	// A no-op once staging has been renamed into place.
	defer func() { _ = os.RemoveAll(staging) }()

	if err := unzipInto(archivePath, staging); err != nil {
		return "", err
	}
	bin := filepath.Join(staging, exe)
	if info, err := os.Stat(bin); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0 {
		return "", fmt.Errorf("ytdlp: release archive has no executable %q", exe)
	}
	version, err := Version(ctx, bin)
	if err != nil {
		return "", err
	}

	removeOldTrees(parent)
	var old string
	if _, err := os.Stat(destDir); err == nil {
		old = filepath.Join(parent, fmt.Sprintf("%s%d", oldTreePrefix, time.Now().UnixNano()))
		if err := os.Rename(destDir, old); err != nil {
			return "", fmt.Errorf("ytdlp: move installed release aside: %w", err)
		}
	}
	if err := os.Rename(staging, destDir); err != nil {
		if old != "" {
			_ = os.Rename(old, destDir)
		}
		return "", fmt.Errorf("ytdlp: install downloaded release: %w", err)
	}
	return version, nil
}

// removeOldTrees deletes the installs earlier updates set aside. Best-effort:
// one that cannot be removed now is tried again by the next update.
func removeOldTrees(parent string) {
	matches, _ := filepath.Glob(filepath.Join(parent, oldTreePrefix+"*"))
	for _, m := range matches {
		_ = os.RemoveAll(m)
	}
}

// unzipInto unpacks the archive at src into the empty directory dst. Only
// plain files and directories are accepted, every entry must stay inside dst,
// and the total is capped at maxUnpackedBytes.
func unzipInto(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return fmt.Errorf("ytdlp: open release archive: %w", err)
	}
	defer func() { _ = zr.Close() }()

	remaining := int64(maxUnpackedBytes)
	for _, f := range zr.File {
		if !filepath.IsLocal(f.Name) {
			return fmt.Errorf("ytdlp: release archive entry %q escapes its directory", f.Name)
		}
		target := filepath.Join(dst, filepath.FromSlash(f.Name))
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("ytdlp: unpack release: %w", err)
			}
			continue
		case !mode.IsRegular():
			return fmt.Errorf("ytdlp: release archive entry %q is not a plain file", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("ytdlp: unpack release: %w", err)
		}
		n, err := unzipFile(f, target, remaining)
		if err != nil {
			return err
		}
		remaining -= n
	}
	return nil
}

var errArchiveTooLarge = errors.New("ytdlp: release archive unpacks past the size cap")

// unzipFile writes one archive entry to target, refusing to write more than
// limit bytes, and returns how many it wrote.
func unzipFile(f *zip.File, target string, limit int64) (int64, error) {
	perm := f.Mode().Perm()
	if perm == 0 {
		perm = 0o644
	}
	rc, err := f.Open()
	if err != nil {
		return 0, fmt.Errorf("ytdlp: unpack release: %w", err)
	}
	defer func() { _ = rc.Close() }()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return 0, fmt.Errorf("ytdlp: unpack release: %w", err)
	}
	n, err := io.Copy(out, io.LimitReader(rc, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("ytdlp: unpack release: %w", err)
	}
	if n > limit {
		return n, errArchiveTooLarge
	}
	return n, nil
}
