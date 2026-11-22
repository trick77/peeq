package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
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

// Subtitles fetches ONLY the captions for one video — no media, no thumbnail —
// and returns their path relative to MediaDir and the track's language.
//
// This is the whole point of the inbox summary: a caption track is a few KB of
// text and answers "is this worth downloading?" for a fraction of the cost of
// finding out the other way.
//
// It runs fetchCaptions, which Download's caption step runs too. If the two
// ever picked differently, the .vtt read before a download and the one read
// after it would differ, and the summary carried over from the inbox would
// describe a transcript the library no longer has.
//
// prefer is the language wanted when the video has it (PickCaptionTrack);
// anyManual lets a lone uploaded track in another language count, which the
// inbox allows on its last attempt only.
//
// A video whose captions do not exist yet is NOT an error: YouTube's automatic
// captions lag publication by minutes to hours, so the common case for a fresh
// upload is a list with no usable track. That returns ("", "", nil) and the
// caller retries later.
//
// Like every other Runner call this passes the cookie and pause gates, and it
// goes through the queue WITHOUT WithInteractive — nobody is waiting on it.
func (r *Runner) Subtitles(ctx context.Context, videoID, rawURL, prefer string, anyManual bool) (relPath, lang string, err error) {
	if videoID == "" {
		return "", "", fmt.Errorf("ytdlp: subtitles requires a non-empty video id")
	}

	watchURL, _, _, err := Canonicalize(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("ytdlp: canonicalize url: %w", err)
	}

	// Ask the gates before touching the filesystem: holdTurn and execCall ask
	// them again, but this call runs once a minute per inbox candidate, and a
	// paused or cookie-less peeq must not leave an empty summary directory
	// behind on every tick.
	if _, err := r.gates(); err != nil {
		return "", "", &RefusedError{Err: err}
	}

	dir := SummaryDir(r.cfg.MediaDir, videoID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", "", fmt.Errorf("ytdlp: create summary dir: %w", err)
	}

	ctx = withCallLabel(ctx, videoID)

	turn, err := r.holdTurn(ctx)
	if err != nil {
		return "", "", err
	}
	defer turn.done()

	track, err := r.fetchCaptions(ctx, turn, watchURL, dir, prefer, anyManual)
	if err != nil {
		// Leave the directory: an empty one costs an inode and the next
		// attempt reuses it. Removing it here would race a concurrent read of
		// a caption this same video fetched on an earlier attempt.
		return "", "", err
	}
	if track.Key == "" {
		return "", "", nil
	}
	rel, err := trackFile(r.cfg.MediaDir, dir, videoID, track.Key)
	if err != nil || rel == "" {
		return "", "", err
	}
	return rel, track.Speech, nil
}

// captionListName is the file the track list is handed to the fetch in. Not
// prefixed with the video id, so no <id>* glob (finalizeDownload) can
// ever take it for a caption or media file.
const captionListName = "captions.info.json"

// fetchCaptions writes the one best caption track (PickCaptionTrack) into dir,
// under a turn the caller holds, and returns which track that was. The zero
// track means the list had none; nothing is fetched then.
//
// Two calls, the same YouTube requests one blind --sub-langs call made: the
// list (-J) is the player request, and the fetch (--load-info-json) reads the
// timedtext URL out of that list instead of extracting again. Both run under
// the one turn: queued apart, other work could land between a player request
// and its caption download, and the list's signed URLs could age while the
// fetch waits. No gap between them either: --sleep-subtitles already waits
// longer than any gap, between exactly those two requests.
//
// Never asking for a translation is the point (see captionInfo): the blind
// "en" call asked German videos for YouTube's English translation, which
// answered 429 on the same videos every time.
func (r *Runner) fetchCaptions(ctx context.Context, turn *heldTurn, watchURL, dir, prefer string, anyManual bool) (CaptionTrack, error) {
	out, err := r.execCall(ctx, turn, nil,
		"-J",
		"--skip-download",
		// Without these yt-dlp lists fewer tracks than it fetches with them;
		// the flags are the ones the fetch below is checked against in prod.
		"--write-subs",
		"--write-auto-subs",
		"--no-playlist",
		"--socket-timeout", "30",
		watchURL,
	)
	if err != nil {
		return CaptionTrack{}, err
	}
	info, err := parseCaptionInfo(out)
	if err != nil {
		return CaptionTrack{}, err
	}
	track, held := pickCaptionTrack(info, prefer, anyManual)
	if held {
		// Otherwise indistinguishable from a video with no captions, which
		// would hide why this one takes the whole ladder to get a transcript.
		r.cfg.Logger.Info("caption track held for the last attempt", "video_id", callLabel(ctx))
	}
	if track.Key == "" {
		return CaptionTrack{}, nil
	}

	listPath := filepath.Join(dir, captionListName)
	if err := os.WriteFile(listPath, out, 0o600); err != nil {
		return CaptionTrack{}, fmt.Errorf("ytdlp: write caption list: %w", err)
	}
	defer func() { _ = os.Remove(listPath) }()

	if _, err := r.execCall(ctx, turn, nil, r.captionFetchArgs(dir, listPath, track)...); err != nil {
		return CaptionTrack{}, err
	}
	return track, nil
}

// subtitleSleepFloor and subtitleSleepJitter set yt-dlp's wait between the
// player request and the caption download: floor + rand[0, jitter).
//
// YouTube answers a caption download that follows the player request within
// seconds with HTTP 429, on the same videos every attempt, at a call rate far
// below any limit and with Chrome impersonation in place. yt-dlp's maintainers
// give a wait of at least 60s as the workaround (yt-dlp#13831). The queue's gap
// cannot provide it: it separates two runs, and both requests are one run.
//
// The run holds the YouTube turn through the wait. That stays under
// maxCallRuntime, and under the download watchdog, which sees the gap before
// the caption step plus this.
const (
	subtitleSleepFloor  = 120 * time.Second
	subtitleSleepJitter = 60 * time.Second
)

// captionFetchArgs downloads exactly track from the list at listPath into dir.
// Only the list the track is in is asked for, and --sub-langs is anchored (it
// is a regex to yt-dlp): "en" must not also match "en-orig" or "en-GB", and an
// uploaded "de" must not come back as YouTube's ASR "de".
//
// The output template must start with the id: trackFile looks for
// <id>.<key>.vtt and finalizeDownload for <id>*.vtt.
func (r *Runner) captionFetchArgs(dir, listPath string, track CaptionTrack) []string {
	sleep := subtitleSleepFloor + time.Duration(r.cfg.RandFloat64()*float64(subtitleSleepJitter))
	which := "--write-auto-subs"
	if track.Manual {
		which = "--write-subs"
	}
	return []string{
		"--load-info-json", listPath,
		"--skip-download",
		which,
		"--sub-langs", "^" + regexp.QuoteMeta(track.Key) + "$",
		"--convert-subs", "vtt",
		"--socket-timeout", "30",
		"--sleep-subtitles", strconv.Itoa(int(sleep.Seconds())),
		"-o", filepath.Join(dir, "%(id)s.%(ext)s"),
	}
}

// trackFile returns the .vtt yt-dlp wrote into dir for track key
// (<id>.<key>.vtt) relative to mediaDir, or ("", nil) when there is none.
//
// The exact name, not a <id>*.vtt glob: the summary dir outlives an attempt,
// and a file an earlier rung left under another key would otherwise be
// returned with this track's language.
func trackFile(mediaDir, dir, videoID, key string) (string, error) {
	full := filepath.Join(dir, videoID+"."+key+".vtt")
	if _, err := os.Stat(full); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("ytdlp: stat subtitles: %w", err)
	}
	rel, err := filepath.Rel(mediaDir, full)
	if err != nil {
		return "", fmt.Errorf("ytdlp: relativize subtitle path: %w", err)
	}
	return rel, nil
}
