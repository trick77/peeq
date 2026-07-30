package ytdlp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// SummaryDirName is the directory under MediaDir that holds captions fetched
// for a video peeq has not downloaded — the inbox summaries.
//
// It is deliberately dot-prefixed and deliberately NOT the final
// <channelID>/<videoID>/ home a download uses. Two reasons, in order of how
// much they would hurt:
//
//   - finalizeDownload RemoveAll's the final directory before renaming staging
//     onto it, so a caption sitting there would be destroyed by the very
//     download it was meant to inform. Harmless in itself (the download
//     re-fetches captions) but it would make subtitle_path point at a file
//     that briefly does not exist.
//   - The final path needs the channel id from the download's own info.json.
//     A caption fetch has no info.json, and the ledger's channel id is not
//     always the one yt-dlp reports.
//
// The dot prefix keeps it out of the <channelID>/ glob that enumerates real
// media. It does NOT keep it out of a filepath.WalkDir — retention's sweeper
// works from database rows rather than a filesystem walk, which is why this is
// safe today; anything that starts walking MediaDir has to skip it explicitly.
const SummaryDirName = ".summaries"

// SummaryDir is where captions for videoID live before it is downloaded.
func SummaryDir(mediaDir, videoID string) string {
	return filepath.Join(mediaDir, SummaryDirName, videoID)
}

// Subtitles fetches ONLY the captions for one video — no media, no thumbnail,
// no info.json — and returns their path relative to MediaDir.
//
// This is the whole point of the inbox summary: a caption track is a few KB of
// text and answers "is this worth downloading?" for a fraction of the cost of
// finding out the other way.
//
// The flags below are a strict subset of Download's, and must stay that way.
// If the two ever disagree on --sub-langs or --convert-subs, the .vtt read
// before a download and the one read after it would differ, and the summary
// carried over from the inbox would describe a transcript the library no
// longer has.
//
// A video whose captions do not exist yet is NOT an error: YouTube's automatic
// captions lag publication by minutes to hours, so the common case for a fresh
// upload is a clean exit with no file written. That returns ("", nil) and the
// caller retries later.
//
// Like every other Runner call this passes the cookie and pause gates, and it
// goes through the pacer WITHOUT WithInteractive — nobody is waiting on it.
func (r *Runner) Subtitles(ctx context.Context, videoID, rawURL, subLang string) (string, error) {
	if err := r.pauseGate(); err != nil {
		return "", err
	}

	cookieText, err := r.cookieGate()
	if err != nil {
		return "", err
	}

	if videoID == "" {
		return "", fmt.Errorf("ytdlp: subtitles requires a non-empty video id")
	}

	watchURL, _, _, err := Canonicalize(rawURL)
	if err != nil {
		return "", fmt.Errorf("ytdlp: canonicalize url: %w", err)
	}

	if subLang == "" {
		subLang = "en"
	}

	dir := SummaryDir(r.cfg.MediaDir, videoID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("ytdlp: create summary dir: %w", err)
	}

	ctx = withCallLabel(ctx, videoID)

	if _, execErr := r.exec(ctx, cookieText,
		"--skip-download",
		"--write-subs",
		"--write-auto-subs",
		"--sub-langs", subLang,
		"--convert-subs", "vtt",
		"--no-playlist",
		"--socket-timeout", "30",
		"-o", filepath.Join(dir, "%(id)s.%(ext)s"),
		watchURL,
	); execErr != nil {
		// Leave the directory: an empty one costs an inode and the next
		// attempt reuses it. Removing it here would race a concurrent read of
		// a caption this same video fetched on an earlier attempt.
		return "", execErr
	}

	return foundSubtitle(r.cfg.MediaDir, dir, videoID)
}

// foundSubtitle locates the .vtt yt-dlp wrote into dir and returns it relative
// to mediaDir, or ("", nil) when there is none.
//
// The glob mirrors finalizeDownload's: yt-dlp names the file
// <id>.<lang>.vtt, and with several languages requested there can be more
// than one. Taking the first match is the same arbitrary-but-consistent
// choice the download path makes, and it has to stay the same choice — see
// Subtitles for why the two paths must not drift.
func foundSubtitle(mediaDir, dir, videoID string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, videoID+"*.vtt"))
	if err != nil {
		return "", fmt.Errorf("ytdlp: glob subtitles: %w", err)
	}
	if len(matches) == 0 {
		return "", nil
	}
	rel, err := filepath.Rel(mediaDir, matches[0])
	if err != nil {
		return "", fmt.Errorf("ytdlp: relativize subtitle path: %w", err)
	}
	return rel, nil
}
