// Package videos persists the videos table (migration 0001_init.sql): one
// row per tracked YouTube video, holding its metadata, download state, and
// the watched/favorite/tombstone lifecycle.
//
// Watched semantics (Task 11, decided product rules): a video becomes
// watched automatically when its resume position reaches >= 90% of the
// duration (SetResume), or manually (SetWatched(id, true)). Re-watching
// never resets watched_at once set — no "life extension" of the retention
// clock. Manual un-watch (SetWatched(id, false)) clears watched, watched_at,
// AND resume_position_seconds, rescuing the video from the retention sweep
// and making that rescue sticky (a stale near-end resume ping can't
// immediately re-mark it watched). Tombstone keeps
// the row (for watched history and a future summary/transcript) but clears
// media_path and marks status='tombstoned'; the caller is responsible for
// unlinking the actual media/thumbnail files from disk first.
package videos

import (
	"context"
	"database/sql"
	"fmt"
)

// Video mirrors the columns of the videos table this package reads or
// writes. Fields left at their zero value on Upsert fall back to the
// column defaults on insert.
type Video struct {
	ID                    string
	URL                   string
	Title                 string
	ChannelID             string
	ChannelName           string
	DurationSeconds       int64
	PublishedAt           string
	Description           string
	ThumbnailPath         string
	MediaPath             string
	FilesizeBytes         int64
	FormatUsed            string
	Availability          string
	Status                string
	ErrorMessage          string
	SponsorblockSegments  string
	Watched               bool
	WatchedAt             string
	ResumePositionSeconds float64
	Favorite              bool
	FavoritedAt           string
	CreatedAt             string
	DownloadedAt          string
}

// watchedThreshold is the fraction of a video's duration that, once
// reached via SetResume, auto-marks it watched.
const watchedThreshold = 0.9

// DownloadedResult is the outcome of a successful download, mapped from
// ytdlp.Result by the worker. SponsorblockSegments is the JSON text stored
// verbatim in the sponsorblock_segments column.
type DownloadedResult struct {
	MediaPath            string
	ThumbnailPath        string
	FilesizeBytes        int64
	FormatUsed           string
	SponsorblockSegments string
}

// Store persists video rows.
type Store struct {
	db *sql.DB
}

// New returns a videos store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Upsert inserts a video row or, if it already exists, refreshes only its
// metadata columns. It deliberately does NOT touch download-owned columns
// (status, media_path, filesize_bytes, format_used, downloaded_at,
// sponsorblock_segments): re-running metadata for an already-downloaded
// video must not wipe its downloaded state.
func (s *Store) Upsert(v Video) error {
	availability := v.Availability
	if availability == "" {
		availability = "unknown"
	}
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO videos (id, url, title, channel_id, channel_name, duration_seconds,
	published_at, description, thumbnail_path, availability)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	url             = excluded.url,
	title           = excluded.title,
	channel_id      = excluded.channel_id,
	channel_name    = excluded.channel_name,
	duration_seconds = excluded.duration_seconds,
	published_at    = excluded.published_at,
	description     = excluded.description,
	thumbnail_path  = excluded.thumbnail_path,
	availability    = excluded.availability`,
		v.ID, v.URL, v.Title, v.ChannelID, v.ChannelName, nullInt(v.DurationSeconds),
		nullStr(v.PublishedAt), v.Description, v.ThumbnailPath, availability,
	)
	if err != nil {
		return fmt.Errorf("upsert video %s: %w", v.ID, err)
	}
	return nil
}

// videoColumns is the column list shared by Get and List, in the order
// scanRow expects.
const videoColumns = `id, url, title, channel_id, channel_name, duration_seconds, published_at,
	description, thumbnail_path, media_path, filesize_bytes, format_used,
	availability, status, error_message, sponsorblock_segments,
	watched, watched_at, resume_position_seconds, favorite, favorited_at,
	created_at, downloaded_at`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanVideo scans one row in the videoColumns order into a Video.
func scanVideo(rs rowScanner) (Video, error) {
	var v Video
	var duration, filesize sql.NullInt64
	var publishedAt, watchedAt, favoritedAt, downloadedAt sql.NullString
	var watched, favorite int
	err := rs.Scan(
		&v.ID, &v.URL, &v.Title, &v.ChannelID, &v.ChannelName, &duration, &publishedAt,
		&v.Description, &v.ThumbnailPath, &v.MediaPath, &filesize, &v.FormatUsed,
		&v.Availability, &v.Status, &v.ErrorMessage, &v.SponsorblockSegments,
		&watched, &watchedAt, &v.ResumePositionSeconds, &favorite, &favoritedAt,
		&v.CreatedAt, &downloadedAt,
	)
	if err != nil {
		return Video{}, err
	}
	v.DurationSeconds = duration.Int64
	v.FilesizeBytes = filesize.Int64
	v.PublishedAt = publishedAt.String
	v.Watched = watched != 0
	v.WatchedAt = watchedAt.String
	v.Favorite = favorite != 0
	v.FavoritedAt = favoritedAt.String
	v.DownloadedAt = downloadedAt.String
	return v, nil
}

// Get returns the video row for id, or (nil, nil) if there is none.
func (s *Store) Get(id string) (*Video, error) {
	row := s.db.QueryRowContext(context.Background(),
		"SELECT "+videoColumns+" FROM videos WHERE id = ?", id,
	)
	v, err := scanVideo(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get video %s: %w", id, err)
	}
	return &v, nil
}

// List returns videos matching filter, newest first:
//   - "unwatched": downloaded and not watched (available to watch now)
//   - "watched": watched = true
//   - "favorites": favorite = true
//   - "downloading": status is queued or downloading
//   - anything else (including "all"/""): every row, tombstoned included
func (s *Store) List(filter string) ([]Video, error) {
	where := ""
	switch filter {
	case "unwatched":
		where = "WHERE status = 'downloaded' AND watched = 0"
	case "watched":
		where = "WHERE watched = 1"
	case "favorites":
		where = "WHERE favorite = 1"
	case "downloading":
		where = "WHERE status IN ('queued', 'downloading')"
	}
	rows, err := s.db.QueryContext(context.Background(),
		"SELECT "+videoColumns+" FROM videos "+where+" ORDER BY created_at DESC, id DESC",
	)
	if err != nil {
		return nil, fmt.Errorf("list videos (filter=%s): %w", filter, err)
	}
	defer rows.Close()

	out := []Video{}
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, fmt.Errorf("list videos (filter=%s): %w", filter, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list videos (filter=%s): %w", filter, err)
	}
	return out, nil
}

// SetStatus sets a video's status and error_message. Used by the worker to
// mark a video 'downloading' when its job is claimed and 'error' (with a
// message) when the download fails terminally.
func (s *Store) SetStatus(id, status, errMsg string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET status = ?, error_message = ? WHERE id = ?`,
		status, errMsg, id,
	)
	if err != nil {
		return fmt.Errorf("set video %s status: %w", id, err)
	}
	return nil
}

// SetDownloaded records a successful download: media path, filesize, the
// resolved format, the SponsorBlock segments JSON, status=downloaded, and
// the downloaded_at timestamp. error_message is cleared (a prior failed
// attempt's message must not linger on a now-successful video).
func (s *Store) SetDownloaded(id string, res DownloadedResult) error {
	segments := res.SponsorblockSegments
	if segments == "" {
		segments = "[]"
	}
	_, err := s.db.ExecContext(context.Background(), `
UPDATE videos
SET media_path = ?, thumbnail_path = COALESCE(NULLIF(?, ''), thumbnail_path),
	filesize_bytes = ?, format_used = ?, sponsorblock_segments = ?,
	status = 'downloaded', error_message = '', downloaded_at = datetime('now')
WHERE id = ?`,
		res.MediaPath, res.ThumbnailPath, res.FilesizeBytes, res.FormatUsed, segments, id,
	)
	if err != nil {
		return fmt.Errorf("set video %s downloaded: %w", id, err)
	}
	return nil
}

// SetFavorite sets a video's favorite flag, stamping (or clearing)
// favorited_at to match.
func (s *Store) SetFavorite(id string, fav bool) error {
	var err error
	if fav {
		_, err = s.db.ExecContext(context.Background(),
			`UPDATE videos SET favorite = 1, favorited_at = datetime('now') WHERE id = ?`, id)
	} else {
		_, err = s.db.ExecContext(context.Background(),
			`UPDATE videos SET favorite = 0, favorited_at = NULL WHERE id = ?`, id)
	}
	if err != nil {
		return fmt.Errorf("set video %s favorite: %w", id, err)
	}
	return nil
}

// SetWatched is the manual watched toggle. Setting true marks the video
// watched, stamping watched_at only if it isn't already set (no life
// extension on a manual re-confirmation); it leaves resume_position_seconds
// untouched. Setting false clears watched, watched_at, AND
// resume_position_seconds — this rescues the video from the retention
// sweep, per the decided un-watch rule. Zeroing the resume position makes
// the rescue sticky: without it, a player resume ping still sitting at or
// above the 90% threshold would immediately re-cross SetResume's
// auto-watched check and undo the un-watch.
func (s *Store) SetWatched(id string, watched bool) error {
	var err error
	if watched {
		_, err = s.db.ExecContext(context.Background(), `
UPDATE videos SET watched = 1, watched_at = COALESCE(watched_at, datetime('now'))
WHERE id = ?`, id)
	} else {
		_, err = s.db.ExecContext(context.Background(), `
UPDATE videos SET watched = 0, watched_at = NULL, resume_position_seconds = 0
WHERE id = ?`, id)
	}
	if err != nil {
		return fmt.Errorf("set video %s watched: %w", id, err)
	}
	return nil
}

// SetResume records the player's resume position and, per the decided
// watched rule, auto-marks the video watched once position reaches >= 90%
// of its duration (this also covers "reaches the end", since position can't
// exceed duration in practice). watched_at is stamped only the first time —
// a later call at or above the threshold (re-watching) never resets it.
// Duration 0/unknown never auto-marks watched (there is no ratio to check).
//
// A negative position is clamped to 0 rather than stored as-is: the HTTP
// handler already rejects negative positions with a 400, but the store
// clamps too as defense-in-depth against any other caller.
func (s *Store) SetResume(id string, position float64) error {
	if position < 0 {
		position = 0
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set video %s resume: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	var duration sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT duration_seconds FROM videos WHERE id = ?`, id,
	).Scan(&duration); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("set video %s resume: not found", id)
		}
		return fmt.Errorf("set video %s resume: %w", id, err)
	}

	autoWatched := duration.Valid && duration.Int64 > 0 &&
		position >= watchedThreshold*float64(duration.Int64)

	if autoWatched {
		_, err = tx.ExecContext(ctx, `
UPDATE videos
SET resume_position_seconds = ?, watched = 1, watched_at = COALESCE(watched_at, datetime('now'))
WHERE id = ?`, position, id)
	} else {
		_, err = tx.ExecContext(ctx,
			`UPDATE videos SET resume_position_seconds = ? WHERE id = ?`, position, id)
	}
	if err != nil {
		return fmt.Errorf("set video %s resume: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set video %s resume: %w", id, err)
	}
	return nil
}

// SweepCandidates returns videos eligible for the retention sweeper
// (Task 12): watched, not favorited, not already tombstoned, and last
// watched strictly before cutoff (an absolute point in time, formatted
// "2006-01-02 15:04:05" UTC to match the format datetime('now') stores in
// watched_at — the caller computes cutoff from settings.RetentionDays and
// its own clock, so the sweeper stays testable without depending on
// SQLite's notion of "now"). Oldest-watched first, so the sweeper's log
// order reads chronologically.
func (s *Store) SweepCandidates(cutoffUTC string) ([]Video, error) {
	rows, err := s.db.QueryContext(context.Background(),
		"SELECT "+videoColumns+` FROM videos
WHERE watched = 1 AND favorite = 0 AND status != 'tombstoned' AND watched_at < ?
ORDER BY watched_at ASC`, cutoffUTC,
	)
	if err != nil {
		return nil, fmt.Errorf("sweep candidates: %w", err)
	}
	defer rows.Close()

	out := []Video{}
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, fmt.Errorf("sweep candidates: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sweep candidates: %w", err)
	}
	return out, nil
}

// Tombstone marks a video deleted-but-remembered: media_path is cleared and
// status becomes 'tombstoned', but the row (and its watched history) is
// kept — a future badge can offer re-download. Tombstone only updates the
// database; the caller must unlink the actual media/thumbnail files first
// (it needs config.MediaDir and path-safety checks the store doesn't have).
func (s *Store) Tombstone(id string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET media_path = '', status = 'tombstoned' WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("tombstone video %s: %w", id, err)
	}
	return nil
}

// nullInt maps 0 to a NULL (the schema leaves duration/filesize nullable
// for "unknown"), any other value to itself.
func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// nullStr maps "" to NULL, any other value to itself.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
