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

// RemoveMediaAndSidecars unlinks the media file itself plus any sibling
// subtitle (.vtt) files in the same directory. Best-effort: a missing file
// is not an error (it may already be gone), and any single removal failure
// doesn't stop the others. mediaPath must already be a SafeMediaPath result.
func RemoveMediaAndSidecars(mediaPath string) {
	_ = os.Remove(mediaPath)

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

// RemoveVideoFiles removes a video's media file (plus sidecars), thumbnail,
// and subtitle from disk under mediaDir, resolving each path safely first.
// This is the hard-delete flavour, for when the database row goes too (a
// channel cascade): nothing is left behind that could reference the files.
// A tombstone must call RemoveTombstonedVideoFiles instead — see there.
// Best-effort: an unresolvable (already-gone, or not a local path) media,
// thumbnail, or subtitle path is silently skipped rather than treated as
// an error — there is nothing on disk to remove in that case.
func RemoveVideoFiles(mediaDir, mediaPath, thumbnailPath, subtitlePath string) {
	// Written out rather than delegating to RemoveTombstonedVideoFiles: that
	// flavour deliberately spares the .vtt sidecars, and a hard delete that
	// inherited the sparing would leave orphaned subtitles on disk with no row
	// left to reference them.
	if mediaPath != "" {
		if safe, err := SafeMediaPath(mediaDir, mediaPath); err == nil {
			RemoveMediaAndSidecars(safe)
		}
	}
	if subtitlePath != "" {
		if safe, err := SafeMediaPath(mediaDir, subtitlePath); err == nil {
			_ = os.Remove(safe)
		}
	}
	if thumbnailPath != "" {
		// A queued-but-not-yet-downloaded video may have a remote thumbnail
		// URL here instead of a local path; SafeMediaPath rejecting that (or
		// the file simply not existing under mediaDir) is harmless — there
		// is nothing on local disk to remove in that case.
		if safe, err := SafeMediaPath(mediaDir, thumbnailPath); err == nil {
			_ = os.Remove(safe)
		}
	}
}

// RemoveTombstonedVideoFiles reclaims what a tombstone is for and nothing
// else: the media file, which is the whole point — megabytes against the
// kilobytes everything around it costs. It deliberately KEEPS:
//
//   - the thumbnail, so the remembered card keeps its poster. Removing it used
//     to leave thumbnail_path pointing at a file that no longer existed, which
//     rendered as a broken image on every tombstoned card.
//   - the subtitle .vtt — both the one subtitle_path names and any .vtt
//     sidecar sitting next to the media file (yt-dlp writes them as
//     <videoID>*.vtt beside <videoID>.<ext>, so RemoveMediaAndSidecars would
//     sweep exactly those). The .vtt is the ONLY source a transcript can be
//     rebuilt from: transcript_chunks / fts_chunks / vec_chunks serve today's
//     searches, but re-chunking or re-embedding (a Reprocess, an embedding
//     model change) reads the file back. Deleting it made a tombstone silently
//     permanent for search, and cost the transcript view too.
//
// The media file therefore goes via a plain unlink, NOT
// RemoveMediaAndSidecars — see the sidecar note above. A hard delete (the
// database row going too, e.g. a channel cascade) wants the opposite and calls
// RemoveVideoFiles.
//
// Both tombstone paths — the manual DELETE endpoint and the retention
// sweeper — go through here, so the two can never diverge.
// The subtitle path is not a parameter: there is nothing here to decide about
// it. A caller with one in hand is meant to leave it alone.
func RemoveTombstonedVideoFiles(mediaDir, mediaPath string) {
	if mediaPath != "" {
		if safe, err := SafeMediaPath(mediaDir, mediaPath); err == nil {
			_ = os.Remove(safe)
		}
	}
}
