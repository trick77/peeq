// Package retention implements the auto-delete sweep (Task 12): watched,
// non-favorite videos past settings.RetentionDays are tombstoned
// automatically, exactly like the manual DELETE endpoint (media file
// unlinked, row kept for watched history), except a video currently being
// streamed is protected regardless of age.
package retention

import (
	"context"
	"log/slog"
	"time"

	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/videos"
)

// NowPlayingGuard reports whether a video is currently being streamed, so
// the sweeper can skip it even if it otherwise qualifies for deletion.
// StreamAccessTracker is the production implementation, fed by the video
// stream handler; tests can inject a trivial fake instead.
type NowPlayingGuard interface {
	IsActive(id string) bool
}

// VideoStore is the subset of *videos.Store the sweeper needs.
type VideoStore interface {
	SweepCandidates(cutoffUTC string) ([]videos.Video, error)
	Tombstone(id string) error
}

// SettingsStore is the subset of *settings.Store the sweeper needs.
type SettingsStore interface {
	Get(ctx context.Context) (settings.Settings, error)
}

// alwaysInactiveGuard is used when Deps.Guard is left nil: nothing is ever
// considered "now playing", so the sweeper falls back to age/favorite/
// watched rules alone.
type alwaysInactiveGuard struct{}

func (alwaysInactiveGuard) IsActive(string) bool { return false }

// Deps are the sweeper's collaborators and tunables.
type Deps struct {
	Videos   VideoStore
	Settings SettingsStore
	// MediaDir is the root directory downloaded media lives under; media
	// files are resolved safely against it before being unlinked (see
	// package media).
	MediaDir string
	// Guard protects a currently-playing video from deletion regardless of
	// age. Optional: nil disables the protection (every otherwise-eligible
	// video is swept).
	Guard NowPlayingGuard
	// Now returns the current time; defaults to time.Now. Overridable so
	// tests can pin the sweeper's notion of "now" instead of depending on
	// wall-clock timing.
	Now func() time.Time
	// Interval is how often Run ticks; defaults to 1 hour.
	Interval time.Duration
	// Logger is used for sweep errors and per-video deletion logging.
	Logger *slog.Logger
}

// Sweeper periodically tombstones aged, watched, non-favorite,
// not-currently-playing videos. Construct with New and drive with Run (or
// call SweepOnce directly, e.g. from a test or an admin trigger).
type Sweeper struct {
	deps Deps
}

// New builds a Sweeper, filling in defaults for the optional Deps fields.
func New(deps Deps) *Sweeper {
	if deps.Guard == nil {
		deps.Guard = alwaysInactiveGuard{}
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Interval <= 0 {
		deps.Interval = time.Hour
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &Sweeper{deps: deps}
}

// Run ticks every Deps.Interval, calling SweepOnce, until ctx is cancelled.
// It sweeps once immediately on start (rather than waiting a full interval
// before the first pass) so a long-idle server catches up promptly on boot.
func (s *Sweeper) Run(ctx context.Context) {
	s.sweepAndLog()

	ticker := time.NewTicker(s.deps.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepAndLog()
		}
	}
}

func (s *Sweeper) sweepAndLog() {
	if err := s.SweepOnce(); err != nil {
		s.deps.Logger.Error("retention sweep failed", "err", err)
	}
}

// SweepOnce runs one sweep pass: it loads the current retention_days,
// queries every watched/non-favorite/non-tombstoned video whose watched_at
// is older than the cutoff, skips any the NowPlayingGuard reports active,
// and tombstones the rest — unlinking media from disk first via the same
// media.RemoveVideoFiles path the manual DELETE endpoint uses, then calling
// videos.Store.Tombstone to clear media_path and mark the row deleted while
// keeping it for watched history.
func (s *Sweeper) SweepOnce() error {
	ctx := context.Background()
	cfg, err := s.deps.Settings.Get(ctx)
	if err != nil {
		return err
	}

	// Defense in depth against a bad retention value slipping past the API
	// validation: a negative retention_days would move the cutoff into the
	// FUTURE, so EVERY watched non-favorite video would match and be
	// tombstoned in one pass (unrecoverable). Skip the sweep entirely rather
	// than delete the whole library.
	if cfg.RetentionDays < 0 {
		s.deps.Logger.Warn("retention sweep: skipping, retention_days is negative", "retention_days", cfg.RetentionDays)
		return nil
	}

	cutoff := s.deps.Now().Add(-time.Duration(cfg.RetentionDays) * 24 * time.Hour)
	cutoffUTC := cutoff.UTC().Format("2006-01-02 15:04:05")

	candidates, err := s.deps.Videos.SweepCandidates(cutoffUTC)
	if err != nil {
		return err
	}

	for _, v := range candidates {
		if s.deps.Guard.IsActive(v.ID) {
			s.deps.Logger.Info("retention sweep: skipping currently-playing video", "video_id", v.ID)
			continue
		}
		media.RemoveVideoFiles(s.deps.MediaDir, v.MediaPath, v.ThumbnailPath, v.SubtitlePath)
		if err := s.deps.Videos.Tombstone(v.ID); err != nil {
			s.deps.Logger.Error("retention sweep: tombstone failed", "video_id", v.ID, "err", err)
			continue
		}
		s.deps.Logger.Info("retention sweep: tombstoned video", "video_id", v.ID, "watched_at", v.WatchedAt)
	}
	return nil
}
