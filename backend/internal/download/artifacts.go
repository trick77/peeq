package download

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/mediaprobe"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// probeProbeTimeout bounds the inline probe of a just-finished download.
// Deliberately short: this call sits inside the single-concurrency download
// loop, before the job is settled, so the timeout is also the worst case for
// how long a wedged ffprobe can stop the queue claiming work. The file is
// local and already written, so a healthy probe takes milliseconds; anything
// slower than this is left to the backfill sweep, which blocks nothing.
const probeProbeTimeout = 5 * time.Second

// probeDownloaded reads the finished file's media facts and stores them.
// Nothing here is fatal.
//
// A failure writes NOTHING — unlike the backfill sweep, which stores a zero
// result to stamp probed_at and stop retrying. The two differ because their
// starting points differ: the sweep only ever sees rows with no values, so a
// zero write loses nothing, while this runs after re-downloads too, where the
// row may already hold good facts from an earlier probe. Overwriting those
// with blanks on a transient ffprobe failure would also stamp probed_at,
// which is exactly what stops the sweep from ever repairing the row.
//
// Writing nothing leaves probed_at NULL on a first download, so the sweep
// picks the video up and retries — and leaves the previous values intact on a
// re-download. Both are the recoverable outcome.
func (w *Worker) probeDownloaded(videoID, mediaPath string) {
	if w.deps.Prober == nil || mediaPath == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeProbeTimeout)
	defer cancel()

	info, err := w.deps.Prober.Probe(ctx, mediaPath)
	if err != nil {
		w.deps.Logger.Warn("download worker: probe failed; leaving it to the backfill sweep",
			"video_id", videoID, "err", err)
		return
	}
	if err := w.deps.Videos.SetProbed(videoID, mediaprobe.StoreResult(info)); err != nil {
		w.deps.Logger.Error("download worker: store probe failed", "video_id", videoID, "err", err)
	}
}

// storeTranscript reads the .vtt yt-dlp wrote into the row, then unlinks every
// .vtt beside the media file.
//
// All of them, not just the one yt-dlp's result names: it may write several
// language and auto-caption variants, and both globs that look for them take
// the FIRST match, so the rest were already unreferenced. Best-effort at every
// step — the download succeeded either way. Nothing retries a transcript that
// did not land now that the import worker is gone (0024), which is why every
// failure below returns BEFORE the unlink rather than after it.
func (w *Worker) storeTranscript(videoID, subtitleRelPath, mediaPath string) {
	if w.deps.Videos == nil {
		return
	}
	if subtitleRelPath != "" {
		// Every failure to get the text into the row RETURNS, the rejected path
		// included: the unlink below takes all of them, so falling through
		// would delete the only copy of a transcript nothing had read.
		safe, err := media.SafeMediaPath(w.deps.MediaDir, subtitleRelPath)
		if err != nil {
			w.deps.Logger.Warn("download worker: transcript path rejected", "video_id", videoID, "err", err)
			return
		}
		data, rerr := os.ReadFile(safe) //nolint:gosec // path comes from media.SafeMediaPath, which rejects traversal and symlink escape and returns the resolved path (see safepath_test.go)
		if rerr != nil {
			w.deps.Logger.Warn("download worker: read transcript failed", "video_id", videoID, "err", rerr)
			return
		}
		if serr := w.deps.Videos.SetTranscript(videoID, videos.TranscriptSourceDownload, string(data)); serr != nil {
			w.deps.Logger.Warn("download worker: store transcript failed", "video_id", videoID, "err", serr)
			return
		}
	}
	if mediaPath == "" {
		return
	}
	if safe, err := media.SafeMediaPath(w.deps.MediaDir, mediaPath); err == nil {
		media.RemoveSubtitleSidecars(safe)
	}
}

// storeThumbnail reads the poster yt-dlp wrote and stores its bytes on the
// video row. Best-effort at every step: no poster, an unreadable file, an
// oversized image or a failed insert are all logged and shrugged off — the
// download itself succeeded, and a card with no poster draws its gradient
// placeholder. Nothing retries it since the import worker went with 0024; a
// re-download is the only way back.
func (w *Worker) storeThumbnail(videoID, thumbPath string) {
	if thumbPath == "" || w.deps.Videos == nil {
		return
	}
	safe, err := media.SafeMediaPath(w.deps.MediaDir, thumbPath)
	if err != nil {
		w.deps.Logger.Warn("download worker: thumbnail path rejected", "video_id", videoID, "err", err)
		return
	}
	data, err := os.ReadFile(safe) //nolint:gosec // path comes from media.SafeMediaPath, which rejects traversal and symlink escape and returns the resolved path (see safepath_test.go)
	if err != nil {
		w.deps.Logger.Warn("download worker: read thumbnail failed", "video_id", videoID, "err", err)
		return
	}
	if err := w.deps.Videos.SetThumbnail(videoID, media.ThumbnailMime(safe), data); err != nil {
		w.deps.Logger.Warn("download worker: store thumbnail failed", "video_id", videoID, "err", err)
		return
	}
	// The file has served its purpose. Removing it only after a successful
	// store is what leaves the image where it is on a failure, rather than
	// unlinking the one copy that landed nowhere.
	_ = os.Remove(safe)
}

// humanSize renders a byte count as a compact KB/MB/GB string for a download's
// activity detail. A sub-megabyte file must not read as "0 MB" (that looks like
// an empty/failed download), so it falls through to KB. Zero bytes (yt-dlp did
// not report a size) yields "".
func humanSize(b int64) string {
	switch {
	case b <= 0:
		return ""
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%.0f KB", float64(b)/(1<<10))
	}
}

// segmentJSON is the stored shape of one SponsorBlock segment in the
// sponsorblock_segments TEXT column (ytdlp.Segment carries no json tags).
type segmentJSON struct {
	Category  string  `json:"category"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
}

// marshalSegments renders the download's SponsorBlock segments as the JSON
// array text stored in videos.sponsorblock_segments. It always returns a
// valid JSON array ("[]" when there are none).
func marshalSegments(segs []ytdlp.Segment) string {
	out := make([]segmentJSON, 0, len(segs))
	for _, s := range segs {
		out = append(out, segmentJSON{Category: s.Category, StartTime: s.StartTime, EndTime: s.EndTime})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// marshalStrings renders yt-dlp's tags/categories as the JSON array text
// stored in videos.yt_tags / videos.yt_categories, matching how
// marshalSegments stores segments.
//
// It returns "" — not "[]" — for an empty list, because SetDownloaded treats
// empty as "leave what is there". "[]" would be a value, and a re-download of
// a video whose extractor happened to omit tags would wipe the ones already
// stored.
func marshalStrings(vals []string) string {
	if len(vals) == 0 {
		return ""
	}
	b, err := json.Marshal(vals)
	if err != nil {
		return ""
	}
	return string(b)
}
