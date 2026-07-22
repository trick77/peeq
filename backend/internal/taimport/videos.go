package taimport

import (
	"context"
	"fmt"

	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
)

// Watch states peeq imports. "unwatched" is the never-started bulk; "continue"
// is started-but-not-finished and carries a resume position. Fully-watched
// videos are deliberately never fetched.
const (
	watchUnwatched = "unwatched"
	watchContinue  = "continue"
)

// DefaultWatchStates is the pair of TubeArchivist watch filters that make up the
// to-watch queue.
var DefaultWatchStates = []string{watchUnwatched, watchContinue}

// VideoLister reads a channel's videos in one watch state. *Client satisfies it.
type VideoLister interface {
	ChannelVideos(ctx context.Context, channelID, watch string) ([]Video, error)
}

// VideoWriter is the slice of peeq's stores the video import writes through.
// *Client never touches it; NewStoreWriter adapts the real stores.
type VideoWriter interface {
	Get(id string) (*videos.Video, error)
	Upsert(v videos.Video) error
	SetDownloaded(id string, res videos.DownloadedResult) error
	SetResumeRaw(id string, position float64) error
	EnqueueSummary(videoID string) (int64, error)
}

// storeWriter adapts peeq's separate videos and summary-jobs stores to
// VideoWriter, since the import needs both.
type storeWriter struct {
	videos    *videos.Store
	summaries *summaryjobs.Store
}

func (w storeWriter) Get(id string) (*videos.Video, error)    { return w.videos.Get(id) }
func (w storeWriter) Upsert(v videos.Video) error             { return w.videos.Upsert(v) }
func (w storeWriter) SetResumeRaw(id string, p float64) error { return w.videos.SetResumeRaw(id, p) }
func (w storeWriter) EnqueueSummary(id string) (int64, error) { return w.summaries.Enqueue(id) }
func (w storeWriter) SetDownloaded(id string, r videos.DownloadedResult) error {
	return w.videos.SetDownloaded(id, r)
}

// NewStoreWriter returns a VideoWriter backed by the real peeq stores.
func NewStoreWriter(v *videos.Store, s *summaryjobs.Store) VideoWriter {
	return storeWriter{videos: v, summaries: s}
}

// ImportOptions configures a video import run.
type ImportOptions struct {
	Paths       PathMapper
	WatchStates []string // defaults to DefaultWatchStates when empty
	Types       []string // vid_type allowlist; empty means all types

	// CheckSpace, if set, is called once after the target set is gathered with
	// the total media bytes about to be copied. Returning an error aborts the
	// run before any file is copied (fail closed). nil skips the check.
	CheckSpace func(neededBytes int64) error
}

// VideoResult summarises an import run.
type VideoResult struct {
	Planned           int   // videos that would be (dry-run) or were imported
	Imported          int   // rows actually written; 0 on a dry run
	SkippedDownloaded int   // already status='downloaded'
	SkippedType       int   // excluded by the --types filter
	MissingFile       int   // the .mp4 is not on the TA mount (trap 4)
	ResumeUnavailable int   // a "continue" video with no resume position (trap 5)
	BytesMedia        int64 // total .mp4 bytes to copy / copied
}

// target is one video that survived filtering and whose .mp4 exists.
type target struct {
	v     Video
	watch string
	bytes int64
}

// ImportVideos copies the unwatched/continue queue of the given channels from
// TubeArchivist into peeq. It gathers the target set first (so the free-space
// preflight can fail closed before any copy), then, unless dryRun, copies each
// video's media/thumbnail/subtitle and writes the rows.
//
// Already-downloaded videos are skipped so a re-run never re-copies or
// re-enqueues a summary (summaryjobs.Enqueue has no uniqueness). A video whose
// .mp4 is missing is counted and skipped rather than marked downloaded.
func ImportVideos(ctx context.Context, lister VideoLister, w VideoWriter, channelIDs []string, opts ImportOptions, dryRun bool) (VideoResult, error) {
	var res VideoResult

	watchStates := opts.WatchStates
	if len(watchStates) == 0 {
		watchStates = DefaultWatchStates
	}

	// Phase 1 — gather the target set.
	var targets []target
	seen := make(map[string]bool)
	for _, chID := range channelIDs {
		for _, watch := range watchStates {
			vids, err := lister.ChannelVideos(ctx, chID, watch)
			if err != nil {
				return res, err
			}
			for _, v := range vids {
				if v.ID == "" || seen[v.ID] {
					continue
				}
				seen[v.ID] = true

				if !typeAllowed(v.VidType, opts.Types) {
					res.SkippedType++
					continue
				}
				existing, err := w.Get(v.ID)
				if err != nil {
					return res, fmt.Errorf("taimport: look up %s: %w", v.ID, err)
				}
				if existing != nil && existing.Status == "downloaded" {
					res.SkippedDownloaded++
					continue
				}
				size, ok := statSize(opts.Paths.srcMedia(v.ChannelID, v.ID))
				if !ok {
					res.MissingFile++ // trap 4: metadata without a file on disk
					continue
				}
				res.BytesMedia += size
				targets = append(targets, target{v: v, watch: watch, bytes: size})
			}
		}
	}
	res.Planned = len(targets)

	if dryRun {
		return res, nil
	}

	// Phase 2 — free-space preflight (fails closed).
	if opts.CheckSpace != nil {
		if err := opts.CheckSpace(res.BytesMedia); err != nil {
			return res, err
		}
	}

	// Phase 3 — copy files and write rows.
	for _, t := range targets {
		if err := importOne(t, opts.Paths, w, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

// importOne copies one video's files and writes its rows. Media is already
// known to exist (checked during gather); thumbnail and subtitle are
// best-effort. Order matters: Upsert (metadata) then SetDownloaded (media
// columns) then the resume position, then the summary job.
func importOne(t target, paths PathMapper, w VideoWriter, res *VideoResult) error {
	v := t.v
	ch, id := v.ChannelID, v.ID

	mediaBytes, err := copyFile(paths.srcMedia(ch, id), paths.dstMedia(ch, id))
	if err != nil {
		return fmt.Errorf("taimport: copy media %s: %w", id, err)
	}

	// Thumbnail lives on TA's cache volume; a missing one is not fatal.
	thumbStored := ""
	if _, err := copyFile(paths.srcThumbnail(id), paths.dstThumbnail(ch, id)); err == nil {
		thumbStored = paths.dstThumbnail(ch, id)
	}

	// Subtitle: copy the preferred language, if any. subtitle_path is stored
	// RELATIVE to MediaDir — the one column that differs from media/thumbnail.
	subRel, audioLang := "", ""
	if lang := preferredSubLang(v.SubtitleLangs); lang != "" {
		if _, err := copyFile(paths.srcSubtitle(ch, id, lang), paths.dstSubtitle(ch, id, lang)); err == nil {
			subRel = paths.storedSubtitleRel(ch, id, lang)
			audioLang = lang
		}
	}

	if err := w.Upsert(videos.Video{
		ID:              id,
		URL:             "https://www.youtube.com/watch?v=" + id,
		Title:           v.Title,
		ChannelID:       ch,
		ChannelName:     v.ChannelName,
		DurationSeconds: int64(v.DurationSeconds),
		PublishedAt:     v.Published,
		Description:     v.Description,
		Availability:    "available",
	}); err != nil {
		return fmt.Errorf("taimport: upsert %s: %w", id, err)
	}
	if err := w.SetDownloaded(id, videos.DownloadedResult{
		MediaPath:       paths.dstMedia(ch, id),
		ThumbnailPath:   thumbStored,
		FilesizeBytes:   mediaBytes,
		SubtitleRelPath: subRel,
		AudioLanguage:   audioLang,
	}); err != nil {
		return fmt.Errorf("taimport: set downloaded %s: %w", id, err)
	}

	// Resume position, only for the continue set, written without the >=90%
	// auto-watch so a nearly-finished video stays in the queue.
	if t.watch == watchContinue {
		if v.Position > 0 {
			if err := w.SetResumeRaw(id, v.Position); err != nil {
				return fmt.Errorf("taimport: set resume %s: %w", id, err)
			}
		} else {
			res.ResumeUnavailable++ // trap 5: report prominently
		}
	}

	if _, err := w.EnqueueSummary(id); err != nil {
		return fmt.Errorf("taimport: enqueue summary %s: %w", id, err)
	}
	res.Imported++
	return nil
}

// preferredSubLang picks which caption track to import for the single
// subtitle_path peeq stores: English if present, else the first listed.
func preferredSubLang(langs []string) string {
	for _, l := range langs {
		if l == "en" {
			return "en"
		}
	}
	if len(langs) > 0 {
		return langs[0]
	}
	return ""
}

// typeAllowed reports whether a video's vid_type passes the --types filter. An
// empty allowlist means all types.
func typeAllowed(vidType string, allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	for _, a := range allow {
		if a == vidType {
			return true
		}
	}
	return false
}
