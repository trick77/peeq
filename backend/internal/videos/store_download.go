package videos

import (
	"context"
	"fmt"
)

// DownloadedResult is the outcome of a successful download, mapped from
// ytdlp.Result by the worker. SponsorblockSegments is the JSON text stored
// verbatim in the sponsorblock_segments column.
type DownloadedResult struct {
	MediaPath            string
	ThumbnailPath        string
	FilesizeBytes        int64
	FormatUsed           string
	SponsorblockSegments string
	SubtitleRelPath      string
	AudioLanguage        string
	ChaptersJSON         string
	// PublishedAt is the release date (YYYY-MM-DD) yt-dlp reported in the
	// download's own info.json, or "" when it reported none.
	//
	// SetDownloaded is the only place a channel-driven download can pick one
	// up: scan.Scheduler.enqueueAuto seeds its videos row from a flat listing
	// that carries no release date, and nothing else would ever set one — the
	// library would sort those videos by download date forever. An empty value
	// leaves whatever is already stored, so a date the richer Metadata path
	// wrote is never clobbered.
	PublishedAt string
	// Description and the four YouTube-supplied fields below arrive the same
	// way and follow the same never-clobber-with-empty rule. They come from
	// the download's own info.json, which is the richest view peeq ever gets
	// of a video — for a channel-driven download it is the ONLY view.
	Description  string
	MediaType    string
	LiveStatus   string
	YTTags       string
	YTCategories string
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

// SetRequestedFormat overrides the yt-dlp format string used for this
// video's next download (empty = use the global preset). Set by the scan
// scheduler from a channel's format_override before enqueueing.
func (s *Store) SetRequestedFormat(id, format string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET requested_format = ? WHERE id = ?`, format, id)
	if err != nil {
		return fmt.Errorf("set requested_format %s: %w", id, err)
	}
	return nil
}

// SetDownloaded records a successful download: media path, filesize, the
// resolved format, the SponsorBlock segments JSON, status=downloaded, and
// the downloaded_at timestamp. error_message is cleared (a prior failed
// attempt's message must not linger on a now-successful video). It also fills
// in published_at — see DownloadedResult.PublishedAt for why that lands here.
//
// sponsorblock_refreshed_at is stamped here too: yt-dlp already asked
// SponsorBlock during this download, so the backfill worker must not
// immediately ask again for a video whose segments just arrived for free.
func (s *Store) SetDownloaded(id string, res DownloadedResult) error {
	segments := res.SponsorblockSegments
	if segments == "" {
		segments = "[]"
	}
	_, err := s.db.ExecContext(context.Background(), `
UPDATE videos
SET media_path = ?, thumbnail_path = COALESCE(NULLIF(?, ''), thumbnail_path),
	filesize_bytes = ?, format_used = ?, sponsorblock_segments = ?,
	subtitle_path = ?, audio_language = ?,
	chapters = CASE WHEN ? != '' THEN ? ELSE chapters END,
	published_at = COALESCE(NULLIF(?, ''), published_at),
	description = COALESCE(NULLIF(?, ''), description),
	media_type = COALESCE(NULLIF(?, ''), media_type),
	live_status = COALESCE(NULLIF(?, ''), live_status),
	yt_tags = COALESCE(NULLIF(?, ''), yt_tags),
	yt_categories = COALESCE(NULLIF(?, ''), yt_categories),
	sponsorblock_refreshed_at = datetime('now'),
	status = 'downloaded', error_message = '', downloaded_at = datetime('now')
WHERE id = ?`,
		res.MediaPath, res.ThumbnailPath, res.FilesizeBytes, res.FormatUsed, segments,
		res.SubtitleRelPath, res.AudioLanguage,
		res.ChaptersJSON, res.ChaptersJSON,
		res.PublishedAt,
		res.Description,
		res.MediaType, res.LiveStatus, res.YTTags, res.YTCategories,
		id,
	)
	if err != nil {
		return fmt.Errorf("set video %s downloaded: %w", id, err)
	}
	return nil
}

// Tombstone marks a video deleted-but-remembered: media_path and
// subtitle_path are cleared and status becomes 'tombstoned', but the row
// (and its watched history) is kept — a future badge can offer
// re-download. Tombstone only updates the database; the caller must unlink
// the actual media/subtitle files first (it needs config.MediaDir and
// path-safety checks the store doesn't have) via
// media.RemoveTombstonedVideoFiles.
//
// thumbnail_path is deliberately NOT cleared: the thumbnail file survives a
// tombstone, so the remembered card keeps its poster. The two must stay in
// step — clearing the column here without keeping the file (or the reverse)
// is what left tombstoned cards showing a broken image.
func (s *Store) Tombstone(id string) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("tombstone video %s: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE videos SET media_path = '', subtitle_path = '', status = 'tombstoned' WHERE id = ?`, id); err != nil {
		return fmt.Errorf("tombstone video %s: %w", id, err)
	}
	// A tombstoned video is gone from the library, so any public share link
	// must die with it — otherwise the video's title/summary/highlights keep
	// being served at /s/<token> after a "delete". Tombstone is the single
	// chokepoint both the manual DELETE endpoint and the retention sweeper go
	// through, so revoking here covers both. (A hard row delete — e.g. deleting
	// a whole channel — instead relies on the share_links ON DELETE CASCADE.)
	if _, err := tx.ExecContext(ctx, `DELETE FROM share_links WHERE video_id = ?`, id); err != nil {
		return fmt.Errorf("tombstone video %s: revoke share link: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("tombstone video %s: commit: %w", id, err)
	}
	return nil
}
