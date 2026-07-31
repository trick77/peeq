// Package thumbimport carries video posters that live on disk into the
// database (migration 0022), so an existing library keeps its cards without
// every video having to be downloaded again.
//
// It is also the repair path for the bug that motivated the move: videos.Upsert
// used to blank thumbnail_path on any metadata write, so a card could lose its
// poster while the image file sat untouched in the media tree. Such a row has no
// usable pointer, so the worker also looks in the conventional location a
// download writes to — which is what gets those posters back.
//
// Everything here is local: os.Stat and os.ReadFile under the media dir. No
// network, no YouTube, no cookies. A poster whose file is genuinely gone (the
// pre-#239 tombstone behaviour unlinked it) cannot be recovered here and keeps
// the UI's gradient placeholder.
package thumbimport

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/sched"
	"github.com/trick77/peeq/internal/videos"
)

const (
	// pollInterval is how often the worker looks for videos with no stored
	// poster. Local file reads only, so it can be brisk, and it goes idle once
	// the library is carried over.
	pollInterval = 30 * time.Second
	// batchSize bounds one pass so a large library spreads its disk reads out
	// instead of reading every poster it owns in one burst at boot.
	batchSize = 25
)

// VideoStore is the slice of videos.Store the worker needs.
type VideoStore interface {
	ThumbnaillessVideos(limit int) ([]videos.ThumbnailImportCandidate, error)
	SetThumbnail(id, mime string, data []byte) error
	SetThumbnailPath(id, path string) error
}

// Deps are the worker's collaborators. Videos and MediaDir are required.
type Deps struct {
	Videos   VideoStore
	MediaDir string
	// PollInterval defaults to pollInterval; BatchSize to batchSize.
	PollInterval time.Duration
	BatchSize    int
	Logger       *slog.Logger
}

// Worker imports on-disk posters into the database, then idles.
type Worker struct {
	d Deps
	// missing is the set of video ids this process already looked for and did
	// not find on disk. Without it every pass would re-stat the same hopeless
	// rows forever: the candidate query selects "has no stored poster", and a
	// video whose file is gone never stops matching. Deliberately in-process
	// rather than a database column — the cost of forgetting is one stat per
	// boot, and a file that reappears (a re-download, a restored backup) then
	// gets picked up instead of being permanently written off.
	missing map[string]struct{}
}

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

// pass handles one batch. It reports false when ctx was cancelled mid-batch, so
// Run stops promptly rather than working through the rest of the list.
//
// The batch is read one larger than it is worked through: rows already known
// missing still match the candidate query, so without over-reading a library
// whose first 25 candidates are all hopeless would make no progress at all.
func (w *Worker) pass(ctx context.Context) bool {
	candidates, err := w.d.Videos.ThumbnaillessVideos(w.d.BatchSize + len(w.missing))
	if err != nil {
		w.d.Logger.Error("thumbimport: list candidates failed", "err", err)
		return true
	}
	imported := 0
	for _, c := range candidates {
		if ctx.Err() != nil {
			return false
		}
		if _, skip := w.missing[c.ID]; skip {
			continue
		}
		if w.importOne(c) {
			imported++
		}
		if imported >= w.d.BatchSize {
			break
		}
	}
	if imported > 0 {
		w.d.Logger.Info("thumbimport: imported posters", "count", imported)
	}
	return true
}

// importOne carries one video's poster into the database, reporting whether it
// found anything. A row it cannot satisfy joins the missing set so the next pass
// does not stat it again.
func (w *Worker) importOne(c videos.ThumbnailImportCandidate) bool {
	path, rel := w.locate(c)
	if path == "" {
		w.missing[c.ID] = struct{}{}
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		w.d.Logger.Warn("thumbimport: read failed", "video_id", c.ID, "err", err)
		w.missing[c.ID] = struct{}{}
		return false
	}
	if err := w.d.Videos.SetThumbnail(c.ID, media.ThumbnailMime(path), data); err != nil {
		// Oversized or a write failure — either way retrying every 30s would not
		// help, so write it off for this process.
		w.d.Logger.Warn("thumbimport: store failed", "video_id", c.ID, "err", err)
		w.missing[c.ID] = struct{}{}
		return false
	}
	// Repair the pointer when the file was found somewhere other than where the
	// row said. Nothing serves from the column any more, but a truthful one is
	// what makes a later hard delete take the file with it.
	if rel != "" {
		if err := w.d.Videos.SetThumbnailPath(c.ID, rel); err != nil {
			w.d.Logger.Warn("thumbimport: repair path failed", "video_id", c.ID, "err", err)
		}
	}
	return true
}

// locate finds the poster file for a candidate: first where the row says it is,
// then where a download would have written it. The second lookup is the one that
// rescues a row whose thumbnail_path was blanked by a metadata write while the
// file itself survived.
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
func (w *Worker) locate(c videos.ThumbnailImportCandidate) (readPath, recordRel string) {
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
