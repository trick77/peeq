// Package videos persists the videos table (migration 0001_init.sql): one
// row per tracked YouTube video, holding its metadata and download state.
// Task 9 needs only what the download worker touches — Upsert (seed a row
// before enqueuing), Get (read the URL to build a download request),
// SetStatus (mark downloading/error), and SetDownloaded (record a finished
// download). The watched/tombstone lifecycle lands in a later task.
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
	ID                   string
	URL                  string
	Title                string
	ChannelID            string
	ChannelName          string
	DurationSeconds      int64
	PublishedAt          string
	Description          string
	ThumbnailPath        string
	MediaPath            string
	FilesizeBytes        int64
	FormatUsed           string
	Availability         string
	Status               string
	ErrorMessage         string
	SponsorblockSegments string
	DownloadedAt         string
}

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

// Get returns the video row for id, or (nil, nil) if there is none.
func (s *Store) Get(id string) (*Video, error) {
	var v Video
	var duration, filesize sql.NullInt64
	var publishedAt, downloadedAt sql.NullString
	err := s.db.QueryRowContext(context.Background(), `
SELECT id, url, title, channel_id, channel_name, duration_seconds, published_at,
	description, thumbnail_path, media_path, filesize_bytes, format_used,
	availability, status, error_message, sponsorblock_segments, downloaded_at
FROM videos WHERE id = ?`, id,
	).Scan(
		&v.ID, &v.URL, &v.Title, &v.ChannelID, &v.ChannelName, &duration, &publishedAt,
		&v.Description, &v.ThumbnailPath, &v.MediaPath, &filesize, &v.FormatUsed,
		&v.Availability, &v.Status, &v.ErrorMessage, &v.SponsorblockSegments, &downloadedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get video %s: %w", id, err)
	}
	v.DurationSeconds = duration.Int64
	v.FilesizeBytes = filesize.Int64
	v.PublishedAt = publishedAt.String
	v.DownloadedAt = downloadedAt.String
	return &v, nil
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
