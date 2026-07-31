// Package media resolves and removes on-disk media files safely under
// config.MediaDir. It is the single place a database-stored path (which is
// otherwise untrusted — it could in principle contain traversal or a
// symlink escape) is turned into a filesystem path that is safe to open or
// remove. Both the videos HTTP API (manual delete/stream) and the
// retention sweeper (automatic tombstone) share this logic so the two
// deletion paths can never diverge.
package media

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SafeMediaPath resolves a stored media_path (or thumbnail_path) safely
// under mediaDir. It rejects paths (relative or absolute) that traverse or
// otherwise resolve outside mediaDir, and rejects symlink escapes by
// evaluating symlinks on both mediaDir and the candidate path (or its
// nearest existing ancestor, for a not-yet-existing file such as a rename
// target) before confirming containment.
//
// The returned path is the EvalSymlinks-resolved path, not the unresolved
// candidate: callers open or remove exactly what was validated here,
// closing the TOCTOU window where a path component could otherwise be
// swapped for a symlink between this check and the filesystem operation.
func SafeMediaPath(mediaDir, storedPath string) (string, error) {
	if mediaDir == "" {
		return "", errors.New("media dir not configured")
	}
	if storedPath == "" {
		return "", errors.New("empty media path")
	}

	absMediaDir, err := filepath.Abs(mediaDir)
	if err != nil {
		return "", fmt.Errorf("resolve media dir: %w", err)
	}

	var candidate string
	if filepath.IsAbs(storedPath) {
		candidate = filepath.Clean(storedPath)
	} else {
		candidate = filepath.Join(absMediaDir, storedPath)
	}
	if err := requireWithin(absMediaDir, candidate); err != nil {
		return "", err
	}

	resolvedMediaDir, err := filepath.EvalSymlinks(absMediaDir)
	if err != nil {
		return "", fmt.Errorf("resolve media dir symlinks: %w", err)
	}
	resolvedCandidate, err := resolveExistingOrAncestor(candidate)
	if err != nil {
		return "", err
	}
	if err := requireWithin(resolvedMediaDir, resolvedCandidate); err != nil {
		return "", err
	}

	return resolvedCandidate, nil
}

// requireWithin errors unless candidate is root or a descendant of root
// (lexically — no filesystem access).
func requireWithin(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes media dir %q", candidate, root)
	}
	return nil
}

// resolveExistingOrAncestor evaluates symlinks on path, walking up to the
// nearest existing ancestor if path itself doesn't exist yet (e.g. a file
// about to be created), then rejoins the non-existing suffix.
func resolveExistingOrAncestor(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", fmt.Errorf("resolve symlinks: %w", err)
	}
	resolvedParent, err := resolveExistingOrAncestor(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(path)), nil
}

// RemoveSubtitleSidecars unlinks every .vtt sitting beside mediaPath — all of
// them, not just the one a row happens to name.
//
// yt-dlp writes captions as <videoID>*.vtt next to <videoID>.<ext>, and may
// write several language and auto-caption variants at once; the code that
// records subtitle_path globs and takes the first match, so the rest were
// already referenced by nothing. Since migration 0023 the transcript text lives
// in the database, so the files are an import source and nothing more.
//
// mediaPath must already be a SafeMediaPath result. Best-effort throughout.
func RemoveSubtitleSidecars(mediaPath string) {
	dir := filepath.Dir(mediaPath)
	base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, base) && strings.HasSuffix(name, ".vtt") {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// RemoveVideoFiles removes everything a video owns on disk: its media file, any
// subtitle sidecars, and its whole <channelID>/<videoID>/ directory once nothing
// is left in it.
//
// This is the hard-delete flavour, for when the database row goes too (a channel
// cascade). Everything else the video owned — poster, transcript, summary,
// chunks — lives in the database since 0022 and 0023 and goes with the row on
// the FK cascade, so the caller does not have to name any of it. What is left on
// disk is the media file plus whatever a pre-migration library still has beside
// it, and taking the directory collects the lot.
//
// A tombstone must call RemoveTombstonedVideoFiles instead — see there.
// Best-effort: an unresolvable or already-gone path is silently skipped.
func RemoveVideoFiles(mediaDir, mediaPath string) {
	if mediaPath == "" {
		return
	}
	safe, err := SafeMediaPath(mediaDir, mediaPath)
	if err != nil {
		return
	}
	_ = os.Remove(safe)
	RemoveSubtitleSidecars(safe)
	// os.Remove, not RemoveAll: it refuses a non-empty directory, which is the
	// safety property wanted here — an unexpected file survives and can be
	// found, rather than being swept silently.
	dir := filepath.Dir(safe)
	if entries, rerr := os.ReadDir(dir); rerr == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}

// RemoveTombstonedVideoFiles reclaims what a tombstone is for and nothing
// else: the media file, which is the whole point — megabytes against the
// kilobytes everything around it costs.
//
// Everything the card still shows after a delete — the poster, the transcript
// the transcript panel reads and the chunks search answers from, the summary —
// lives in the database (migrations 0022 and 0023) and is untouched by this.
// That is what a delete means in peeq: the file goes, the memory of the video
// stays, and it stays searchable and re-analysable with nothing on disk at all.
//
// This used to be a careful exercise in sparing the right siblings: the poster,
// the .vtt subtitle_path named, AND any .vtt sidecar beside the media file,
// because yt-dlp writes them as <videoID>*.vtt and a plain sidecar sweep would
// have taken the only source a transcript could be rebuilt from. None of that
// is needed now — there is nothing beside the media file worth keeping.
//
// Both tombstone paths — the manual DELETE endpoint and the retention sweeper —
// go through here, so the two can never diverge.
func RemoveTombstonedVideoFiles(mediaDir, mediaPath string) {
	if mediaPath != "" {
		if safe, err := SafeMediaPath(mediaDir, mediaPath); err == nil {
			_ = os.Remove(safe)
		}
	}
}
