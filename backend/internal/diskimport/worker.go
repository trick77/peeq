// Package diskimport carries the assets that used to live in the media tree —
// video posters, transcripts, channel artwork, inbox posters — into the
// database (migrations 0022 and 0023), so an existing library keeps everything
// it has without re-downloading anything.
//
// It is also the repair path for the bug that started the move: videos.Upsert
// blanked thumbnail_path on any metadata write, so a card could lose its poster
// while the image file sat untouched in the media tree. Such a row has no usable
// pointer, so the video passes also look where a download would have written the
// file.
//
// Everything here is local: os.Stat and os.ReadFile under the media dir. No
// network, no YouTube, no cookies. An asset whose file is genuinely gone (the
// pre-#239 tombstone unlinked posters and transcripts alike) cannot be recovered
// here.
//
// A stored asset's file is UNLINKED, which is what makes the media tree
// converge on video files and nothing else. The ordering matters: the unlink
// only ever follows a successful store, so a failure leaves the file where it
// was for the next pass to retry.
//
// This whole package is temporary. Once it has drained in production it, its
// wiring in cmd/peeq, and the path columns it reads are deleted — the same
// retirement 0019_drop_embed_jobs gave the re-embed backfill.
package diskimport

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/sched"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

const (
	// pollInterval is how often the worker looks for anything left to carry in.
	// Local file reads only, so it can be brisk, and it goes idle once the
	// library is over.
	pollInterval = 30 * time.Second
	// batchSize bounds one pass per asset kind so a large library spreads its
	// disk reads out instead of reading everything it owns in one burst at boot.
	batchSize = 25
)

// VideoStore is the slice of videos.Store the worker needs.
type VideoStore interface {
	ThumbnaillessVideos(limit int) ([]videos.ThumbnailImportCandidate, error)
	SetThumbnail(id, mime string, data []byte) error
	SetThumbnailPath(id, path string) error
	TranscriptlessVideos(limit int) ([]videos.TranscriptImportCandidate, error)
	SetTranscript(id, source, vtt string) error
}

// ChannelStore is the slice of channels.Store the worker needs.
type ChannelStore interface {
	ImagelessChannels(limit int) ([]channels.ImageImportCandidate, error)
	SetImage(channelID, kind, mime string, data []byte) error
}

// LedgerStore is the slice of channelvideos.Store the worker needs: the inbox
// posters cached under .pending/ before they had a row to live on.
type LedgerStore interface {
	ThumbnaillessPendingIDs(limit int) ([]string, error)
	SetThumbnail(videoID, mime string, data []byte) error
}

// Deps are the worker's collaborators. MediaDir is required; each store may be
// nil, which skips that asset kind (tests that only care about one).
type Deps struct {
	Videos   VideoStore
	Channels ChannelStore
	Ledger   LedgerStore
	MediaDir string
	// PollInterval defaults to pollInterval; BatchSize to batchSize.
	PollInterval time.Duration
	BatchSize    int
	Logger       *slog.Logger
}

// Worker imports on-disk assets into the database, then idles.
type Worker struct {
	d Deps
	// missing is the set of asset keys this process already looked for and did
	// not find on disk. Without it every pass would re-stat the same hopeless
	// rows forever: the candidate queries select "has nothing stored", and an
	// asset whose file is gone never stops matching. Deliberately in-process
	// rather than a database column — the cost of forgetting is one stat per
	// boot, and a file that reappears (a re-download, a restored backup) then
	// gets picked up instead of being permanently written off.
	missing map[string]struct{}
	// swept records that the one-off tidy of the media tree has run in this
	// process. It is one-shot rather than every-pass because after it the tree
	// is already clean, and re-walking a large library every 30 seconds forever
	// would be pure waste.
	swept bool
	// blocked records that some asset was FOUND on disk but could not be carried
	// in — an unreadable file, an oversized image, a write the store refused.
	// Those are exactly the files the sweep must not touch: the copy on disk is
	// still the only one. A file that simply is not there does not set this;
	// there is nothing to lose in that case, and a library with one permanently
	// missing poster must still get its tree tidied.
	//
	// One-way for the life of the process. The next boot retries the import and,
	// if it succeeds, sweeps then.
	blocked bool
}

// stagingDirName is the in-flight download directory the sweep must never
// touch. Restated here rather than imported so the sweep's exclusion cannot be
// silently widened by a change elsewhere.
const stagingDirName = ".staging"

// NewWorker builds a Worker, filling in defaults for the optional Deps.
func NewWorker(d Deps) *Worker {
	if d.PollInterval <= 0 {
		d.PollInterval = pollInterval
	}
	if d.BatchSize <= 0 {
		d.BatchSize = batchSize
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d, missing: make(map[string]struct{})}
}

// Run is the import loop; it blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if !w.pass(ctx) {
			return
		}
		if !sched.Sleep(ctx, w.d.PollInterval) {
			return
		}
	}
}

// pass runs one batch of each asset kind, then — only once every pass has come
// up empty — sweeps whatever is left in the media tree that is not a video.
// It reports false when ctx was cancelled mid-pass, so Run stops promptly
// rather than working through the rest of the list.
func (w *Worker) pass(ctx context.Context) bool {
	drained := true
	for _, step := range []struct {
		kind string
		run  func(context.Context) (imported, unattempted int, ok bool)
	}{
		{"transcripts", w.importTranscripts},
		{"thumbnails", w.importThumbnails},
		{"channel images", w.importChannelImages},
		{"inbox posters", w.importPendingThumbnails},
	} {
		n, left, ok := step.run(ctx)
		if n > 0 {
			w.d.Logger.Info("diskimport: imported", "kind", step.kind, "count", n)
		}
		// Drained means every candidate has been TRIED, not merely that this
		// pass carried nothing in: a batch full of hopeless rows imports zero
		// while real files still queue up behind it.
		if n > 0 || left > 0 {
			drained = false
		}
		if !ok {
			return false
		}
	}
	// The sweep runs ONLY once nothing is left to carry in AND nothing that was
	// found failed to land. Ordering is the whole safety argument: while an
	// asset is still on disk and not yet in the database, that file is the only
	// copy, and deleting it would be the data loss this migration exists to
	// prevent.
	if drained && !w.blocked && !w.swept {
		w.swept = true
		w.sweep(ctx)
	}
	return true
}

// sweepable are the extensions the media tree may hold that are NOT the video:
// the poster, the captions, yt-dlp's metadata dump, and the description file
// older versions wrote. Everything peeq now reads for these lives in the
// database, so a file with one of these extensions is by definition a leftover.
//
// An allow-list, not a deny-list of video extensions: a file this does not
// recognise is logged and kept. Sweeping the media tree is not the place to
// discover what an unfamiliar file was for.
var sweepable = map[string]struct{}{
	".vtt":         {},
	".jpg":         {},
	".jpeg":        {},
	".png":         {},
	".webp":        {},
	".info.json":   {},
	".description": {},
}

// sweepExt is filepath.Ext with one special case: ".info.json" is matched
// whole, so the sweep takes yt-dlp's metadata dump without claiming every .json
// a future version of peeq might keep beside a video.
func sweepExt(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".info.json") {
		return ".info.json"
	}
	return strings.ToLower(filepath.Ext(path))
}

// legacyDirs are the media-tree directories whose entire contents moved into
// the database. Once the import has drained they hold nothing peeq will ever
// read again.
//
// .staging is deliberately absent: it holds downloads in flight, and one is
// kept on a retryable failure precisely so --continue can resume it.
var legacyDirs = []string{".channels", ".pending", ytdlp.SummaryDirName}

// sweep removes what does not belong in the media tree any more, so the end
// state is video files and nothing else.
//
// Earlier versions of peeq leaked in several directions at once — a channel's
// artwork was never deleted, an info.json survived every delete path, a
// re-download's stale-extension poster was orphaned beside its replacement, and
// a caption directory outlived the row that pointed at it. None of those had a
// row to find them by, so this walks the tree instead.
//
// Deliberately conservative: only known asset extensions are removed, only
// outside .staging, and everything removed is counted in the log line. A file
// it does not recognise is reported and left alone.
func (w *Worker) sweep(ctx context.Context) {
	if w.d.MediaDir == "" {
		return
	}
	root, err := media.SafeMediaPath(w.d.MediaDir, ".")
	if err != nil {
		w.d.Logger.Error("diskimport: sweep could not resolve media dir", "err", err)
		return
	}

	// The wholesale directories go first: nothing inside them is ever read
	// again, so walking their contents file by file would only be slower.
	for _, name := range legacyDirs {
		if safe, serr := media.SafeMediaPath(w.d.MediaDir, name); serr == nil {
			if _, statErr := os.Stat(safe); statErr == nil {
				if rerr := os.RemoveAll(safe); rerr != nil {
					w.d.Logger.Warn("diskimport: sweep could not remove legacy dir", "dir", name, "err", rerr)
				} else {
					w.d.Logger.Info("diskimport: swept legacy directory", "dir", name)
				}
			}
		}
	}

	removed, kept := 0, 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // an unreadable entry is not worth failing the sweep over
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			// A download in flight is not litter.
			if d.Name() == stagingDirName && filepath.Dir(path) == root {
				return filepath.SkipDir
			}
			return nil
		}
		if _, ok := sweepable[sweepExt(path)]; !ok {
			kept++
			return nil
		}
		if rerr := os.Remove(path); rerr != nil {
			w.d.Logger.Warn("diskimport: sweep could not remove file", "path", path, "err", rerr)
			return nil
		}
		removed++
		return nil
	})
	if err != nil && ctx.Err() == nil {
		w.d.Logger.Warn("diskimport: sweep walk failed", "err", err)
	}

	// Second walk for the directories the removals just emptied. Bottom-up so a
	// <channelID>/ goes once its last <videoID>/ has.
	w.pruneEmptyDirs(root)

	w.d.Logger.Info("diskimport: sweep complete", "removed", removed, "kept", kept)
}

// pruneEmptyDirs removes every empty directory under root, deepest first, so a
// video directory emptied by the sweep takes its channel directory with it when
// that was the channel's last video.
func (w *Worker) pruneEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == root {
			return nil
		}
		if d.Name() == stagingDirName && filepath.Dir(path) == root {
			return filepath.SkipDir
		}
		dirs = append(dirs, path)
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		removeIfEmpty(dirs[i])
	}
}

// budget is how many candidates to read for one pass: the batch plus everything
// already written off, because those still match the candidate queries. Without
// the allowance a library whose first 25 candidates are all hopeless would make
// no progress at all.
func (w *Worker) budget() int { return w.d.BatchSize + len(w.missing) }

// importTranscripts carries .vtt files into video_transcripts.
func (w *Worker) importTranscripts(ctx context.Context) (int, int, bool) {
	if w.d.Videos == nil {
		return 0, 0, true
	}
	budget := w.budget()
	candidates, err := w.d.Videos.TranscriptlessVideos(budget)
	if err != nil {
		w.d.Logger.Error("diskimport: list transcriptless videos failed", "err", err)
		// Unknown rather than drained: a query that failed says nothing about
		// what is left, and the sweep must not read it as "nothing".
		return 0, 1, true
	}
	done := 0
	for i, c := range candidates {
		if ctx.Err() != nil {
			return done, len(candidates) - i, false
		}
		if w.skip("vtt:" + c.ID) {
			continue
		}
		if w.importTranscript(c) {
			done++
		}
		if done >= w.d.BatchSize {
			return done, len(candidates) - i - 1, true
		}
	}
	return done, truncated(len(candidates), budget), true
}

// importTranscript carries one video's captions in and removes every .vtt it
// left behind.
//
// The source is decided by WHERE the file was found: a caption fetched to
// inform an inbox decision lives under .summaries/, and that video's analysis is
// deliberately truncated. Getting this wrong would run the full pipeline —
// category, key points, embeddings — over a video nobody has asked for yet.
func (w *Worker) importTranscript(c videos.TranscriptImportCandidate) bool {
	key := "vtt:" + c.ID
	path, source := w.locateTranscript(c)
	if path == "" {
		w.missing[key] = struct{}{}
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		if err != nil {
			w.d.Logger.Warn("diskimport: read transcript failed", "video_id", c.ID, "err", err)
			w.blocked = true
		}
		w.missing[key] = struct{}{}
		return false
	}
	if err := w.d.Videos.SetTranscript(c.ID, source, string(data)); err != nil {
		w.d.Logger.Warn("diskimport: store transcript failed", "video_id", c.ID, "err", err)
		w.missing[key] = struct{}{}
		w.blocked = true
		return false
	}
	// Every .vtt beside it, not just the one that was read: yt-dlp may write
	// several language and auto-caption variants, and the code that recorded
	// subtitle_path took the first match, so the rest were referenced by nothing
	// even before this.
	media.RemoveSubtitleSidecars(path)
	removeIfEmpty(filepath.Dir(path))
	return true
}

// locateTranscript finds a video's .vtt and says where it came from: the path
// the row recorded, the conventional download location, or the inbox caption
// directory. Only the last implies a caption read.
func (w *Worker) locateTranscript(c videos.TranscriptImportCandidate) (path, source string) {
	if c.SubtitlePath != "" {
		if safe, err := media.SafeMediaPath(w.d.MediaDir, c.SubtitlePath); err == nil {
			if _, serr := os.Stat(safe); serr == nil {
				return safe, sourceForPath(c.SubtitlePath)
			}
		}
	}
	if c.ChannelID != "" {
		if p := w.firstMatch(filepath.Join(c.ChannelID, c.ID), c.ID+"*.vtt"); p != "" {
			return p, videos.TranscriptSourceDownload
		}
	}
	if p := w.firstMatch(filepath.Join(ytdlp.SummaryDirName, c.ID), "*.vtt"); p != "" {
		return p, videos.TranscriptSourceCaption
	}
	return "", ""
}

// sourceForPath reads the provenance out of a recorded path, which is exactly
// what the code did inline before migration 0023 gave it a column.
func sourceForPath(relPath string) string {
	if strings.HasPrefix(filepath.ToSlash(relPath), ytdlp.SummaryDirName+"/") {
		return videos.TranscriptSourceCaption
	}
	return videos.TranscriptSourceDownload
}

// firstMatch returns the first file in relDir matching pattern, resolved safely
// under the media dir, or "" if the directory or the match is absent.
func (w *Worker) firstMatch(relDir, pattern string) string {
	dir, err := media.SafeMediaPath(w.d.MediaDir, relDir)
	if err != nil {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil || len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// importThumbnails carries video posters into video_thumbnails.
func (w *Worker) importThumbnails(ctx context.Context) (int, int, bool) {
	if w.d.Videos == nil {
		return 0, 0, true
	}
	budget := w.budget()
	candidates, err := w.d.Videos.ThumbnaillessVideos(budget)
	if err != nil {
		w.d.Logger.Error("diskimport: list thumbnailless videos failed", "err", err)
		return 0, 1, true
	}
	done := 0
	for i, c := range candidates {
		if ctx.Err() != nil {
			return done, len(candidates) - i, false
		}
		if w.skip("thumb:" + c.ID) {
			continue
		}
		if w.importThumbnail(c) {
			done++
		}
		if done >= w.d.BatchSize {
			return done, len(candidates) - i - 1, true
		}
	}
	return done, truncated(len(candidates), budget), true
}

// importThumbnail carries one video's poster into the database, reporting
// whether it found anything.
func (w *Worker) importThumbnail(c videos.ThumbnailImportCandidate) bool {
	key := "thumb:" + c.ID
	path, rel := w.locateThumbnail(c)
	if path == "" {
		w.missing[key] = struct{}{}
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		w.d.Logger.Warn("diskimport: read thumbnail failed", "video_id", c.ID, "err", err)
		w.missing[key] = struct{}{}
		w.blocked = true
		return false
	}
	if err := w.d.Videos.SetThumbnail(c.ID, media.ThumbnailMime(path), data); err != nil {
		// Oversized or a write failure — retrying every 30s would not help, so
		// write it off for this process.
		w.d.Logger.Warn("diskimport: store thumbnail failed", "video_id", c.ID, "err", err)
		w.missing[key] = struct{}{}
		w.blocked = true
		return false
	}
	// Record where it was found before removing it, so a hard delete over a
	// half-imported library still has a truthful path to unlink.
	if rel != "" {
		if err := w.d.Videos.SetThumbnailPath(c.ID, rel); err != nil {
			w.d.Logger.Warn("diskimport: repair thumbnail path failed", "video_id", c.ID, "err", err)
		}
	}
	_ = os.Remove(path)
	removeIfEmpty(filepath.Dir(path))
	return true
}

// locateThumbnail finds the poster file for a candidate: first where the row
// says it is, then where a download would have written it. The second lookup is
// what rescues a row whose thumbnail_path was blanked by a metadata write while
// the file itself survived.
//
// A remote url in thumbnail_path (metadata preflight used to store one) is
// rejected by SafeMediaPath, which is exactly right — it falls through to the
// conventional location.
//
// It returns the path to READ plus, only when the file turned up somewhere other
// than where the row pointed, the mediaDir-relative path to record. That second
// value is built here rather than derived from the first: SafeMediaPath returns
// a symlink-resolved path (on macOS /var/... resolves to /private/var/...), so
// filepath.Rel against the configured media dir would yield a ../../.. escape
// hatch rather than the tidy relative path the column wants.
func (w *Worker) locateThumbnail(c videos.ThumbnailImportCandidate) (readPath, recordRel string) {
	if c.ThumbnailPath != "" {
		if safe, err := media.SafeMediaPath(w.d.MediaDir, c.ThumbnailPath); err == nil {
			if _, err := os.Stat(safe); err == nil {
				return safe, ""
			}
		}
	}
	if c.ChannelID == "" {
		return "", ""
	}
	for _, ext := range media.ThumbnailExts {
		rel := filepath.Join(c.ChannelID, c.ID, c.ID+ext)
		safe, err := media.SafeMediaPath(w.d.MediaDir, rel)
		if err != nil {
			continue
		}
		if _, err := os.Stat(safe); err == nil {
			return safe, rel
		}
	}
	return "", ""
}

// importChannelImages carries avatars and banners into channel_images.
func (w *Worker) importChannelImages(ctx context.Context) (int, int, bool) {
	if w.d.Channels == nil {
		return 0, 0, true
	}
	budget := w.budget()
	candidates, err := w.d.Channels.ImagelessChannels(budget)
	if err != nil {
		w.d.Logger.Error("diskimport: list imageless channels failed", "err", err)
		return 0, 1, true
	}
	done := 0
	for i, c := range candidates {
		if ctx.Err() != nil {
			return done, len(candidates) - i, false
		}
		key := "chan:" + c.ChannelID + ":" + c.Kind
		if w.skip(key) {
			continue
		}
		safe, serr := media.SafeMediaPath(w.d.MediaDir, c.Path)
		if serr != nil {
			w.missing[key] = struct{}{}
			continue
		}
		data, rerr := os.ReadFile(safe)
		if rerr != nil {
			w.missing[key] = struct{}{}
			if !os.IsNotExist(rerr) {
				w.blocked = true
			}
			continue
		}
		if err := w.d.Channels.SetImage(c.ChannelID, c.Kind, media.ThumbnailMime(safe), data); err != nil {
			w.d.Logger.Warn("diskimport: store channel image failed",
				"channel_id", c.ChannelID, "kind", c.Kind, "err", err)
			w.missing[key] = struct{}{}
			w.blocked = true
			continue
		}
		_ = os.Remove(safe)
		removeIfEmpty(filepath.Dir(safe))
		done++
		if done >= w.d.BatchSize {
			return done, len(candidates) - i - 1, true
		}
	}
	return done, truncated(len(candidates), budget), true
}

// importPendingThumbnails carries the inbox posters cached under .pending/ into
// pending_thumbnails.
//
// Unlike the other three this has no recorded path to work from — that cache was
// only ever addressed by convention — so it walks the pending ledger ids and
// looks each one up.
func (w *Worker) importPendingThumbnails(ctx context.Context) (int, int, bool) {
	if w.d.Ledger == nil {
		return 0, 0, true
	}
	budget := w.budget()
	ids, err := w.d.Ledger.ThumbnaillessPendingIDs(budget)
	if err != nil {
		w.d.Logger.Error("diskimport: list pending videos failed", "err", err)
		return 0, 1, true
	}
	done := 0
	for i, id := range ids {
		if ctx.Err() != nil {
			return done, len(ids) - i, false
		}
		key := "pending:" + id
		if w.skip(key) {
			continue
		}
		path := w.locatePendingThumbnail(id)
		if path == "" {
			w.missing[key] = struct{}{}
			continue
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			w.missing[key] = struct{}{}
			w.blocked = true
			continue
		}
		if err := w.d.Ledger.SetThumbnail(id, media.ThumbnailMime(path), data); err != nil {
			w.d.Logger.Warn("diskimport: store pending thumbnail failed", "video_id", id, "err", err)
			w.missing[key] = struct{}{}
			w.blocked = true
			continue
		}
		_ = os.Remove(path)
		removeIfEmpty(filepath.Dir(path))
		done++
		if done >= w.d.BatchSize {
			return done, len(ids) - i - 1, true
		}
	}
	return done, truncated(len(ids), budget), true
}

// locatePendingThumbnail finds one cached inbox poster under .pending/<id>/.
func (w *Worker) locatePendingThumbnail(videoID string) string {
	for _, ext := range media.PendingThumbExts {
		rel := filepath.Join(media.PendingThumbDir(videoID), "thumbnail"+ext)
		safe, err := media.SafeMediaPath(w.d.MediaDir, rel)
		if err != nil {
			continue
		}
		if _, err := os.Stat(safe); err == nil {
			return safe
		}
	}
	return ""
}

// truncated reports one unattempted candidate when the query came back full,
// because a full page means there may be more behind it. Without this a batch
// that happened to hold only written-off rows would look drained while real
// files still queued up behind them — and the sweep would delete those files.
func truncated(got, budget int) int {
	if got >= budget {
		return 1
	}
	return 0
}

// skip reports whether this key was already looked for and not found.
func (w *Worker) skip(key string) bool {
	_, found := w.missing[key]
	return found
}

// removeIfEmpty drops a directory once the import has taken the last file out of
// it, so the media tree does not keep a skeleton of empty folders.
//
// os.Remove, not RemoveAll: it refuses a non-empty directory, which is the
// safety property wanted — anything unexpected survives and can be found.
func removeIfEmpty(dir string) {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}
}
