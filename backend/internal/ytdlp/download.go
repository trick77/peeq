package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DownloadReq describes a single video download.
type DownloadReq struct {
	// URL is the source video URL. It is re-canonicalized here (same as
	// Metadata does), so callers never need to canonicalize twice.
	URL string
	// VideoID is the yt-dlp video id. It drives the staging directory name
	// and the output template, and must match the id yt-dlp itself reports
	// in the resulting *.info.json.
	VideoID string
	// Format is a format preset id (see Presets) resolved via Resolve.
	// "custom" is accepted but currently always errors here, since
	// DownloadReq carries no separate custom-format field; custom formats
	// are a later extension point.
	Format string
	// LimitRate is a yt-dlp --limit-rate value (e.g. "5M"). Left empty,
	// --limit-rate is omitted entirely (no rate limiting).
	LimitRate string
}

// Segment is one SponsorBlock-marked chapter parsed out of the
// downloaded video's own *.info.json (never from a prior Metadata call).
type Segment struct {
	Category  string
	StartTime float64
	EndTime   float64
}

// Result is what a successful Download produces.
type Result struct {
	// MediaPath is the absolute path to the final merged media file,
	// already moved out of staging into MediaDir/<channelID>/<videoID>/.
	MediaPath string
	// ThumbnailPath is the absolute path to the downloaded thumbnail, or
	// "" if none was found (--write-thumbnail should always produce one,
	// but this is not treated as fatal).
	ThumbnailPath string
	FilesizeBytes int64
	// FormatUsed is the resolved -f selector string that was passed to
	// yt-dlp.
	FormatUsed           string
	SponsorblockSegments []Segment
}

// Progress is one parsed --newline progress update.
type Progress struct {
	Percent float64
	Speed   string
	ETA     string
}

// progressLineRe matches yt-dlp's --newline progress lines, e.g.:
//
//	[download]  10.0% of   50.00MiB at    2.00MiB/s ETA 01:23
//	[download] 100% of 50.00MiB in 00:05
//
// Speed and ETA are optional (the final "100%" line usually omits ETA and
// reports "in <duration>" instead of "at <speed> ETA <eta>").
var progressLineRe = regexp.MustCompile(`\[download\]\s+([0-9.]+)%(?:[^\r\n]*?\bat\s+(\S+))?(?:[^\r\n]*?\bETA\s+(\S+))?`)

// parseProgressLine parses a single line of yt-dlp --newline stdout output
// into a Progress. Lines that don't look like a download progress update
// (e.g. "[youtube] Extracting URL") return ok == false.
func parseProgressLine(line string) (Progress, bool) {
	m := progressLineRe.FindStringSubmatch(line)
	if m == nil {
		return Progress{}, false
	}
	pct, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return Progress{}, false
	}
	return Progress{Percent: pct, Speed: m[2], ETA: m[3]}, true
}

// downloadInfoJSON is the subset of yt-dlp's *.info.json vark needs after
// a download: the channel id (to place the final directory) and chapters
// (to extract SponsorBlock segments inserted by --sponsorblock-mark).
type downloadInfoJSON struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	Chapters  []struct {
		StartTime float64 `json:"start_time"`
		EndTime   float64 `json:"end_time"`
		Title     string  `json:"title"`
	} `json:"chapters"`
}

// sponsorblockChapterPrefix is how yt-dlp titles chapters it inserts via
// --sponsorblock-mark: "[SponsorBlock]: <category>".
const sponsorblockChapterPrefix = "[SponsorBlock]: "

func sponsorblockSegmentsFromInfo(info downloadInfoJSON) []Segment {
	var segs []Segment
	for _, c := range info.Chapters {
		if !strings.HasPrefix(c.Title, sponsorblockChapterPrefix) {
			continue
		}
		segs = append(segs, Segment{
			Category:  strings.TrimPrefix(c.Title, sponsorblockChapterPrefix),
			StartTime: c.StartTime,
			EndTime:   c.EndTime,
		})
	}
	return segs
}

// Download runs yt-dlp to fetch req.URL into a per-video staging
// directory, then atomically moves the finished result into its final
// MediaDir/<channelID>/<videoID>/ location. Like Metadata, it goes
// through the shared cookie gate and throttle (via execWithProgress); a
// download is refused with ErrNoCookie exactly like a metadata fetch
// would be, and it waits out the same 20s+ floor before invoking the
// binary.
//
// On a retryable failure (e.g. rate limiting), the staging directory
// (including any *.part file yt-dlp left behind) is preserved so a retry
// with --continue can resume it. On any other failure — terminal errors,
// blocked/cookie errors, or ctx cancellation — the staging directory is
// removed; there is nothing usable to resume.
func (r *Runner) Download(ctx context.Context, req DownloadReq, onProgress func(Progress)) (*Result, error) {
	cookieText, err := r.cookieGate()
	if err != nil {
		return nil, err
	}

	if req.VideoID == "" {
		return nil, fmt.Errorf("ytdlp: download requires a non-empty video id")
	}

	watchURL, _, _, err := Canonicalize(req.URL)
	if err != nil {
		return nil, fmt.Errorf("ytdlp: canonicalize url: %w", err)
	}

	formatSelector, err := Resolve(req.Format, "")
	if err != nil {
		return nil, err
	}

	stagingDir := filepath.Join(r.cfg.MediaDir, ".staging", req.VideoID)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("ytdlp: create staging dir: %w", err)
	}

	args := []string{"-f", formatSelector}
	if req.LimitRate != "" {
		args = append(args, "--limit-rate", req.LimitRate)
	}
	args = append(args,
		"-o", filepath.Join(stagingDir, "%(id)s.%(ext)s"),
		"--merge-output-format", "mp4",
		"--write-thumbnail",
		"--write-info-json",
		"--sponsorblock-mark", "all",
		"--no-playlist",
		"--newline",
		"--socket-timeout", "30",
		"--continue",
		watchURL,
	)

	onLine := func(line string) {
		if p, ok := parseProgressLine(line); ok && onProgress != nil {
			onProgress(p)
		}
	}

	if _, execErr := r.execWithProgress(ctx, cookieText, onLine, args...); execErr != nil {
		if !isRetryable(execErr) {
			_ = os.RemoveAll(stagingDir)
		}
		return nil, execErr
	}

	result, err := finalizeDownload(stagingDir, r.cfg.MediaDir, req.VideoID, formatSelector)
	if err != nil {
		_ = os.RemoveAll(stagingDir)
		return nil, err
	}
	return result, nil
}

// isRetryable reports whether err is a *RetryableError, i.e. a transient
// failure worth preserving staging for (so a later --continue can resume
// it) rather than cleaning up.
func isRetryable(err error) bool {
	var re *RetryableError
	return errors.As(err, &re)
}

// finalizeDownload reads the info.json yt-dlp wrote into stagingDir,
// atomically renames the whole staging directory into its final
// mediaDir/<channelID>/<videoID> location, and assembles the Result.
func finalizeDownload(stagingDir, mediaDir, videoID, formatUsed string) (*Result, error) {
	infoPath := filepath.Join(stagingDir, videoID+".info.json")
	infoBytes, err := os.ReadFile(infoPath)
	if err != nil {
		return nil, fmt.Errorf("ytdlp: read info json: %w", err)
	}

	var info downloadInfoJSON
	if err := json.Unmarshal(infoBytes, &info); err != nil {
		return nil, fmt.Errorf("ytdlp: parse info json: %w", err)
	}
	if info.ChannelID == "" {
		return nil, fmt.Errorf("ytdlp: info json for %q missing channel_id", videoID)
	}

	finalDir := filepath.Join(mediaDir, info.ChannelID, videoID)
	if err := os.MkdirAll(filepath.Dir(finalDir), 0o755); err != nil {
		return nil, fmt.Errorf("ytdlp: create channel dir: %w", err)
	}
	// Atomic placement: os.Rename of the whole staging/<id> directory
	// straight into its final <channelID>/<id> home. This is a single
	// filesystem rename (same volume, since both live under MediaDir), so
	// there is no window where a partially-written result is visible at
	// the final path.
	if err := os.Rename(stagingDir, finalDir); err != nil {
		return nil, fmt.Errorf("ytdlp: atomic rename to final dir: %w", err)
	}

	mediaPath := filepath.Join(finalDir, videoID+".mp4")
	fi, err := os.Stat(mediaPath)
	if err != nil {
		return nil, fmt.Errorf("ytdlp: expected merged media file missing: %w", err)
	}

	return &Result{
		MediaPath:            mediaPath,
		ThumbnailPath:        findThumbnail(finalDir, videoID),
		FilesizeBytes:        fi.Size(),
		FormatUsed:           formatUsed,
		SponsorblockSegments: sponsorblockSegmentsFromInfo(info),
	}, nil
}

// thumbnailExts are the extensions yt-dlp's --write-thumbnail may produce,
// depending on what format the source thumbnail was served in.
var thumbnailExts = []string{".jpg", ".jpeg", ".png", ".webp"}

// findThumbnail returns the path to the downloaded thumbnail file in dir,
// or "" if none of the known extensions is present.
func findThumbnail(dir, videoID string) string {
	for _, ext := range thumbnailExts {
		p := filepath.Join(dir, videoID+ext)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
