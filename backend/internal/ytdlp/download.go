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

	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/sponsorblock"
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
	// "custom" is accepted and uses CustomFormat as the verbatim -f
	// selector string.
	Format string
	// CustomFormat is the verbatim yt-dlp -f selector string used when
	// Format == "custom" (see Resolve). Ignored for any other Format
	// value.
	CustomFormat string
	// LimitRate is a yt-dlp --limit-rate value (e.g. "5M"). Left empty,
	// --limit-rate is omitted entirely (no rate limiting).
	LimitRate string
	// SubLang is the --sub-langs value passed to yt-dlp. Left empty, "en"
	// is used as the default.
	SubLang string
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
	// SubtitleRelPath is the MediaDir-relative path to the downloaded
	// subtitle .vtt file, or "" if none was found.
	SubtitleRelPath string
	// AudioLanguage is yt-dlp's reported "language" for the video (from its
	// own *.info.json), or "" if it didn't report one.
	AudioLanguage string
	// ChaptersJSON is yt-dlp's own (non-SponsorBlock) chapters, encoded as
	// `[{"ts":int,"title":string,"source":"yt-dlp"}]`, or "" if the video
	// had none.
	ChaptersJSON string
	// PublishedAt is the AIR date normalized to YYYY-MM-DD (same shape
	// Metadata produces), or "" if yt-dlp reported none of the four date
	// fields airDate looks at.
	PublishedAt string
	// Description is the video's own description, or "" if it had none.
	Description string
	// MediaType/LiveStatus/Tags/Categories: see the matching fields on Meta.
	MediaType  string
	LiveStatus string
	Tags       []string
	Categories []string
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

// downloadInfoJSON is the subset of yt-dlp's *.info.json peeq needs after
// a download: the channel id (to place the final directory), the video's own
// chapters, and the SponsorBlock segments fetched by --sponsorblock-mark.
type downloadInfoJSON struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	// Language is yt-dlp's reported audio/video language for the download.
	Language string `json:"language"`
	// Duration is the video length in seconds, used only to snap a SponsorBlock
	// segment that ends within a second of the end of the video. Absent for
	// some live streams, in which case that snap is skipped.
	Duration float64 `json:"duration"`
	// The four date fields airDate resolves the AIR date from. Read here so
	// that channel-driven downloads carry one too: they never go through
	// Runner.Metadata (no per-video -J call, to respect the throttle budget).
	// --write-info-json writes the complete info dict, so these cost nothing.
	UploadDate       string `json:"upload_date"`
	ReleaseDate      string `json:"release_date"`
	ReleaseTimestamp int64  `json:"release_timestamp"`
	Timestamp        int64  `json:"timestamp"`
	// Description is the video's own description — never fetched before this,
	// which is why videos.description sat empty since 0001 despite the column
	// and the API field both existing.
	Description string `json:"description"`
	// MediaType/LiveStatus/Tags/Categories: see the matching fields on Meta.
	MediaType  string   `json:"media_type"`
	LiveStatus string   `json:"live_status"`
	Tags       []string `json:"tags"`
	Categories []string `json:"categories"`
	Chapters   []struct {
		StartTime float64 `json:"start_time"`
		EndTime   float64 `json:"end_time"`
		Title     string  `json:"title"`
	} `json:"chapters"`
	// SponsorblockChapters is where --sponsorblock-mark's result actually
	// lands: its own top-level key, NOT merged into Chapters. See
	// sponsorblockSegmentsFromInfo.
	SponsorblockChapters []struct {
		StartTime float64 `json:"start_time"`
		EndTime   float64 `json:"end_time"`
		Category  string  `json:"category"`
	} `json:"sponsorblock_chapters"`
}

// sponsorblockChapterPrefix is how yt-dlp titles chapters it MERGES into the
// media file's embedded chapter list for --sponsorblock-mark:
// "[SponsorBlock]: <category>".
//
// It never appears in the *.info.json. yt-dlp's SponsorBlockPP runs at the
// after_filter stage and writes its result to the separate
// sponsorblock_chapters key; the prefixed titles are produced later by the
// ModifyChapters postprocessor, which runs after the info.json has already
// been written. Reading the prefix out of Chapters is exactly the bug that
// made peeq store an empty segment list for every video ever downloaded.
const sponsorblockChapterPrefix = "[SponsorBlock]: "

// sponsorblockSegmentsFromInfo extracts the segments --sponsorblock-mark
// fetched for this download. It runs them through the SAME
// sponsorblock.Normalize the backfill client uses — category filtering,
// boundary snapping and ordering alike — so a video shows the same bands, at
// the same boundaries, whether it was downloaded or filled in later. Doing
// only half of it here would leave freshly-downloaded videos with unsnapped
// boundaries for the full refresh interval.
func sponsorblockSegmentsFromInfo(info downloadInfoJSON) []Segment {
	raw := make([]sponsorblock.Segment, 0, len(info.SponsorblockChapters))
	for _, c := range info.SponsorblockChapters {
		raw = append(raw, sponsorblock.Segment{
			Category:  c.Category,
			StartTime: c.StartTime,
			EndTime:   c.EndTime,
		})
	}
	normalized := sponsorblock.Normalize(raw, info.Duration)
	if len(normalized) == 0 {
		return nil
	}
	segs := make([]Segment, 0, len(normalized))
	for _, s := range normalized {
		segs = append(segs, Segment{
			Category:  s.Category,
			StartTime: s.StartTime,
			EndTime:   s.EndTime,
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
	if err := r.pauseGate(); err != nil {
		return nil, err
	}

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

	formatSelector, err := Resolve(req.Format, req.CustomFormat)
	if err != nil {
		return nil, err
	}

	stagingDir := filepath.Join(r.cfg.MediaDir, ".staging", req.VideoID)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("ytdlp: create staging dir: %w", err)
	}

	subLang := req.SubLang
	if subLang == "" {
		subLang = "en"
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
		"--write-subs",
		"--write-auto-subs",
		"--sub-langs", subLang,
		"--convert-subs", "vtt",
		"--no-playlist",
		"--newline",
		"--socket-timeout", "30",
		"--continue",
		watchURL,
	)

	// Name this call for the logger, so a warning on stderr can be tied to the
	// video it belongs to. The pacer deliberately lets an interactive call run
	// alongside a background one, so two yt-dlp processes really can be writing
	// at the same time.
	ctx = withCallLabel(ctx, req.VideoID)

	onLine := func(line string) {
		if p, ok := parseProgressLine(line); ok {
			if onProgress != nil {
				onProgress(p)
			}
			return
		}
		// Everything else yt-dlp says about this download. These lines were
		// dropped here without anything ever reading them, so the only account
		// peeq could give of a download was its percentage — and a download
		// spends a visible part of its life outside that percentage: extracting
		// the URL before the first byte, and merging streams after the last one.
		//
		// Trimmed because yt-dlp pads some of them, and skipped when empty so a
		// blank line between phases doesn't become a log entry.
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			r.cfg.Logger.Debug("yt-dlp", "video_id", req.VideoID, "line", trimmed)
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
	// finalDir is unique to this video id, so if it already exists it can
	// only be a prior copy of THIS SAME video (e.g. a re-download). Remove
	// it first: os.Rename onto a pre-existing non-empty directory fails
	// with ENOTEMPTY, which would otherwise leave the fresh download
	// stranded in staging and then deleted by the caller's cleanup path,
	// silently losing the new file. Overwriting is always correct here.
	if err := os.RemoveAll(finalDir); err != nil {
		return nil, fmt.Errorf("ytdlp: remove stale final dir: %w", err)
	}
	// Atomic placement: os.Rename of the whole staging/<id> directory
	// straight into its final <channelID>/<id> home. This is a single
	// filesystem rename (same volume, since both live under MediaDir), so
	// there is no window where a partially-written result is visible at
	// the final path.
	if err := os.Rename(stagingDir, finalDir); err != nil {
		return nil, fmt.Errorf("ytdlp: atomic rename to final dir: %w", err)
	}
	// The info.json has told us everything it is ever asked: it is read once,
	// here, and nothing else in peeq opens it. Keeping it meant a file that
	// survived tombstone, hard delete and channel cascade alike, referenced by
	// nothing. --write-info-json stays because finalize needs the channel id.
	_ = os.Remove(filepath.Join(finalDir, videoID+".info.json"))

	mediaPath := filepath.Join(finalDir, videoID+".mp4")
	fi, err := os.Stat(mediaPath)
	if err != nil {
		return nil, fmt.Errorf("ytdlp: expected merged media file missing: %w", err)
	}

	var subtitleRelPath string
	if matches, err := filepath.Glob(filepath.Join(finalDir, videoID+"*.vtt")); err == nil && len(matches) > 0 {
		if rel, err := filepath.Rel(mediaDir, matches[0]); err == nil {
			subtitleRelPath = rel
		}
	}

	// non-SponsorBlock chapters become the provisional yt-dlp TOC. The prefix
	// check is defensive: as documented on sponsorblockChapterPrefix, yt-dlp
	// does not merge SponsorBlock titles into the info.json's chapters, so it
	// never fires today. It stays because a chapter list is written into the
	// media file with those titles, and a future yt-dlp that also wrote them
	// to the sidecar would otherwise silently pollute the table of contents.
	type chapterOut struct {
		TS     int    `json:"ts"`
		Title  string `json:"title"`
		Source string `json:"source"`
	}
	var chs []chapterOut
	for _, c := range info.Chapters {
		if strings.HasPrefix(c.Title, sponsorblockChapterPrefix) {
			continue
		}
		chs = append(chs, chapterOut{TS: int(c.StartTime), Title: c.Title, Source: "yt-dlp"})
	}
	var chaptersJSON string
	if len(chs) > 0 {
		if b, err := json.Marshal(chs); err == nil {
			chaptersJSON = string(b)
		}
	}

	return &Result{
		MediaPath:            mediaPath,
		ThumbnailPath:        findThumbnail(finalDir, videoID),
		FilesizeBytes:        fi.Size(),
		FormatUsed:           formatUsed,
		SponsorblockSegments: sponsorblockSegmentsFromInfo(info),
		SubtitleRelPath:      subtitleRelPath,
		AudioLanguage:        info.Language,
		ChaptersJSON:         chaptersJSON,
		PublishedAt: airDate(rawDates{
			ReleaseTimestamp: info.ReleaseTimestamp,
			Timestamp:        info.Timestamp,
			ReleaseDate:      info.ReleaseDate,
			UploadDate:       info.UploadDate,
		}),
		Description: info.Description,
		MediaType:   info.MediaType,
		LiveStatus:  info.LiveStatus,
		Tags:        info.Tags,
		Categories:  info.Categories,
	}, nil
}

// findThumbnail returns the path to the downloaded thumbnail file in dir,
// or "" if none of the known extensions is present. The extension list lives in
// media.ThumbnailExts: the thumbnail import worker goes looking for the very
// same files, and a second copy of the list would be a second thing to keep in
// lockstep with what yt-dlp actually writes.
func findThumbnail(dir, videoID string) string {
	for _, ext := range media.ThumbnailExts {
		p := filepath.Join(dir, videoID+ext)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
