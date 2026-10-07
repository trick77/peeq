package ytdlp

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// maxUnpackedBytes caps what one release archive may unpack to. The real
// build is ~100MB; the cap only stops a broken or hostile archive from
// filling the data volume.
const maxUnpackedBytes = 1 << 30

// treePrefix names one unpacked release beside the install link.
const treePrefix = ".yt-dlp-tree-"

// treeKeep is how long a replaced tree is kept. A download has no total time
// cap (only an inactivity watchdog), so how many updates it can outlive is
// unbounded; how long it runs is not. A var so tests can expire trees.
var treeKeep = 24 * time.Hour

// downloadUnpackedFrom downloads the zipped self-contained build at url and
// installs it so that destDir/exe runs it. It returns the version the new
// executable reports.
//
// Every release unpacks into a tree of its own beside destDir, and destDir is
// a symlink to the current one. The new tree is complete and has answered
// --version before the link moves, and the link moves by renaming a new link
// over it, so a caller resolving the binary sees the old release or the new
// one, never neither. Any failure leaves the install as it was and removes
// the new tree.
//
// A tree is never changed after it is installed, and that is what keeps a
// running yt-dlp safe: PyInstaller resolves the real path of its executable at
// start and loads modules from that tree for as long as it runs, so a long
// download carries on from the tree it started in. A replaced tree is
// therefore only removed once it is older than treeKeep; the current and the
// previous one are always kept.
func downloadUnpackedFrom(ctx context.Context, url, destDir, exe string) (string, error) {
	parent := filepath.Dir(destDir)

	archivePath, err := fetchToTemp(ctx, url, parent)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(archivePath) }()

	// MkdirTemp makes the tree 0700: only peeq's own user ever runs it.
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
	removeStaleTrees(parent, time.Now(), tree, previous)
	return version, nil
}

// swapLink points the symlink at link to tree and returns the tree it pointed
// to before ("" if none). A plain directory found at link is first moved into
// a tree of its own, so it is kept like any previous release, and moved back
// if the link cannot be made.
func swapLink(link, tree string) (string, error) {
	parent := filepath.Dir(link)
	var previous string
	movedPlain := false
	switch info, err := os.Lstat(link); {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		// Unreadable means the tree in use is unknown, and removing trees
		// afterwards could take the one a run is executing from.
		target, err := os.Readlink(link)
		if err != nil {
			return "", fmt.Errorf("ytdlp: read installed release link: %w", err)
		}
		previous = filepath.Join(parent, filepath.Base(target))
	case err == nil:
		previous = filepath.Join(parent, fmt.Sprintf("%s%d", treePrefix, time.Now().UnixNano()))
		if err := os.Rename(link, previous); err != nil {
			return "", fmt.Errorf("ytdlp: move installed release aside: %w", err)
		}
		movedPlain = true
	case !errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("ytdlp: inspect installed release: %w", err)
	}
	restore := func() {
		if movedPlain {
			_ = os.Rename(previous, link)
		}
	}

	next := filepath.Join(parent, fmt.Sprintf(".yt-dlp-link-%d", time.Now().UnixNano()))
	// Relative, so the link keeps working wherever the volume is mounted.
	if err := os.Symlink(filepath.Base(tree), next); err != nil {
		restore()
		return "", fmt.Errorf("ytdlp: link downloaded release: %w", err)
	}
	if err := os.Rename(next, link); err != nil {
		_ = os.Remove(next)
		restore()
		return "", fmt.Errorf("ytdlp: install downloaded release: %w", err)
	}
	return previous, nil
}

// removeStaleTrees deletes the unpacked releases in parent that are older
// than treeKeep, other than keep. Best-effort: one that cannot be removed now
// is tried again by the next update.
func removeStaleTrees(parent string, now time.Time, keep ...string) {
	matches, _ := filepath.Glob(filepath.Join(parent, treePrefix+"*"))
	for _, m := range matches {
		if isAny(m, keep) {
			continue
		}
		if info, err := os.Stat(m); err == nil && now.Sub(info.ModTime()) >= treeKeep {
			_ = os.RemoveAll(m)
		}
	}
}

func isAny(p string, set []string) bool {
	for _, s := range set {
		if s != "" && filepath.Clean(s) == filepath.Clean(p) {
			return true
		}
	}
	return false
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
