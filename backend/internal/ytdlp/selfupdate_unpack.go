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
	"strings"
	"time"
)

// maxUnpackedBytes caps what one release archive may unpack to. The real
// build is ~100MB; the cap only stops a broken or hostile archive from
// filling the data volume.
const maxUnpackedBytes = 1 << 30

// treePrefix names one unpacked release beside the install link.
const treePrefix = ".yt-dlp-tree-"

// downloadUnpackedFrom downloads the zipped self-contained build at url and
// installs it so that destDir/exe runs it. It returns the version the new
// executable reports.
//
// Every release unpacks into a tree of its own beside destDir, and destDir is
// a symlink to the current one. The new tree is complete and has answered
// --version before the link moves, and the link moves by renaming a new link
// over it, so a caller resolving the binary sees the old release or the new
// one, never neither. Any failure before that removes the new tree and leaves
// the install as it was.
//
// A tree is never changed after it is installed, and that is what keeps a
// running yt-dlp safe: PyInstaller resolves the real path of its executable at
// start and loads modules from that tree for as long as it runs, so a long
// download carries on from the tree it started in. The tree the link just left
// is therefore kept; older ones are removed, since no run outlives two updates
// in practice.
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

	tree, err := os.MkdirTemp(parent, treePrefix+"*")
	if err != nil {
		return "", fmt.Errorf("ytdlp: create unpack dir: %w", err)
	}
	installed := false
	defer func() {
		if !installed {
			_ = os.RemoveAll(tree)
		}
	}()
	// MkdirTemp makes the tree 0700: only peeq's own user ever runs it.
	if err := unzipInto(archivePath, tree); err != nil {
		return "", err
	}
	bin := filepath.Join(tree, exe)
	if info, err := os.Stat(bin); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0 {
		return "", fmt.Errorf("ytdlp: release archive has no executable %q", exe)
	}
	version, err := Version(ctx, bin)
	if err != nil {
		return "", err
	}

	previous, err := swapLink(destDir, tree)
	if err != nil {
		return "", err
	}
	installed = true
	removeTreesExcept(parent, tree, previous)
	return version, nil
}

// swapLink points the symlink at link to tree and returns the tree it pointed
// to before ("" if none). A plain directory found at link is moved into a tree
// of its own first, so it is kept like any previous release.
func swapLink(link, tree string) (string, error) {
	parent := filepath.Dir(link)
	var previous string
	switch info, err := os.Lstat(link); {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		if target, err := os.Readlink(link); err == nil {
			previous = filepath.Join(parent, filepath.Base(target))
		}
	case err == nil:
		previous = filepath.Join(parent, fmt.Sprintf("%s%d", treePrefix, time.Now().UnixNano()))
		if err := os.Rename(link, previous); err != nil {
			return "", fmt.Errorf("ytdlp: move installed release aside: %w", err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("ytdlp: inspect installed release: %w", err)
	}

	next := filepath.Join(parent, fmt.Sprintf(".yt-dlp-link-%d", time.Now().UnixNano()))
	// Relative, so the link keeps working wherever the volume is mounted.
	if err := os.Symlink(filepath.Base(tree), next); err != nil {
		return "", fmt.Errorf("ytdlp: link downloaded release: %w", err)
	}
	if err := os.Rename(next, link); err != nil {
		_ = os.Remove(next)
		return "", fmt.Errorf("ytdlp: install downloaded release: %w", err)
	}
	return previous, nil
}

// removeTreesExcept deletes every unpacked release in parent other than keep.
// Best-effort: one that cannot be removed now is tried again by the next
// update.
func removeTreesExcept(parent string, keep ...string) {
	matches, _ := filepath.Glob(filepath.Join(parent, treePrefix+"*"))
	for _, m := range matches {
		kept := false
		for _, k := range keep {
			if k != "" && filepath.Clean(k) == filepath.Clean(m) {
				kept = true
			}
		}
		if !kept && strings.HasPrefix(filepath.Base(m), treePrefix) {
			_ = os.RemoveAll(m)
		}
	}
}

// unzipInto unpacks the archive at src into the empty directory dst. Only
// plain files and directories are accepted, every entry must stay inside dst,
// and the total is capped at maxUnpackedBytes.
//
// Symlinks are refused rather than followed: yt-dlp's release archives have
// none (2026.08.19: 175 entries, all plain), and one appearing would fail the
// update loudly instead of installing something unchecked.
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
			if err := os.MkdirAll(target, 0o750); err != nil {
				return fmt.Errorf("ytdlp: unpack release: %w", err)
			}
			continue
		case !mode.IsRegular():
			return fmt.Errorf("ytdlp: release archive entry %q is not a plain file", f.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
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
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm) //nolint:gosec // target is the fresh unpack dir joined with an entry name unzipInto has checked with filepath.IsLocal
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
