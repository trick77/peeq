package videos

import (
	"context"
	"database/sql"
	"fmt"
)

// MaxThumbnailBytes bounds what may be stored for one poster. yt-dlp's
// thumbnails run tens of kilobytes; anything past this is not a poster in any
// useful sense and would be paying database size for nothing. A candidate over
// the cap is skipped, not truncated — half an image is worse than none.
const MaxThumbnailBytes = 2 << 20 // 2 MiB

// Thumbnail is one stored poster: the bytes plus what is needed to serve them.
type Thumbnail struct {
	Mime      string
	Bytes     []byte
	UpdatedAt string
}

// SetThumbnail stores (or replaces) the poster for a video. The bytes live in
// the database rather than the media tree so they cannot drift away from the
// row that references them — see migration 0022 for why.
//
// Over MaxThumbnailBytes is a no-op, reported as an error so the caller can log
// it: the video is fine, only its oversized poster was declined.
func (s *Store) SetThumbnail(id, mime string, data []byte) error {
	if id == "" {
		return fmt.Errorf("set thumbnail: empty video id")
	}
	if len(data) == 0 {
		return fmt.Errorf("set thumbnail %s: empty image", id)
	}
	if len(data) > MaxThumbnailBytes {
		return fmt.Errorf("set thumbnail %s: %d bytes exceeds cap %d", id, len(data), MaxThumbnailBytes)
	}
	if mime == "" {
		return fmt.Errorf("set thumbnail %s: empty mime", id)
	}
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO video_thumbnails (video_id, mime, bytes, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT(video_id) DO UPDATE SET
	mime       = excluded.mime,
	bytes      = excluded.bytes,
	updated_at = excluded.updated_at`, id, mime, data)
	if err != nil {
		return fmt.Errorf("set thumbnail %s: %w", id, err)
	}
	return nil
}

// GetThumbnail returns the stored poster for id, or (nil, nil) when the video
// has none — which is the only "missing" case there is now: the bytes are
// either in the row or they do not exist.
func (s *Store) GetThumbnail(id string) (*Thumbnail, error) {
	var t Thumbnail
	err := s.db.QueryRowContext(context.Background(),
		`SELECT mime, bytes, updated_at FROM video_thumbnails WHERE video_id = ?`, id,
	).Scan(&t.Mime, &t.Bytes, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get thumbnail %s: %w", id, err)
	}
	return &t, nil
}

// DeleteThumbnail drops a video's stored poster. The ON DELETE CASCADE on
// video_thumbnails already handles a video row going away (foreign_keys is on —
// see store.Open), so this exists for the case where the poster alone should go.
func (s *Store) DeleteThumbnail(id string) error {
	if _, err := s.db.ExecContext(context.Background(),
		`DELETE FROM video_thumbnails WHERE video_id = ?`, id,
	); err != nil {
		return fmt.Errorf("delete thumbnail %s: %w", id, err)
	}
	return nil
}

// SetThumbnailPath repoints a row at the poster FILE that backs it. Only the
// import worker calls this, to make a blanked or stale pointer truthful again
// after it has found the file: nothing serves from the column, but a hard delete
// still uses it to take the image off disk with the row.
func (s *Store) SetThumbnailPath(id, path string) error {
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET thumbnail_path = ? WHERE id = ?`, path, id,
	); err != nil {
		return fmt.Errorf("set thumbnail path %s: %w", id, err)
	}
	return nil
}

// ThumbnailImportCandidate is a video with no stored poster yet, and the two
// facts the import worker needs to go looking for its file on disk.
type ThumbnailImportCandidate struct {
	ID            string
	ChannelID     string
	ThumbnailPath string
}

// ThumbnaillessVideos returns up to limit videos that have no row in
// video_thumbnails, newest first so a large library fills in the order the user
// is most likely to be looking at.
//
// Rows are NOT filtered by thumbnail_path: the whole point of the import worker
// is that a blanked pointer does not mean a missing file, so a candidate with an
// empty path is exactly the interesting case (the worker falls back to the
// conventional <channelID>/<videoID>/<videoID>.<ext> location).
func (s *Store) ThumbnaillessVideos(limit int) ([]ThumbnailImportCandidate, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(context.Background(), `
SELECT v.id, v.channel_id, v.thumbnail_path
FROM videos v
WHERE NOT EXISTS (SELECT 1 FROM video_thumbnails t WHERE t.video_id = v.id)
ORDER BY COALESCE(v.downloaded_at, v.created_at) DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list thumbnailless videos: %w", err)
	}
	defer rows.Close()

	var out []ThumbnailImportCandidate
	for rows.Next() {
		var c ThumbnailImportCandidate
		if err := rows.Scan(&c.ID, &c.ChannelID, &c.ThumbnailPath); err != nil {
			return nil, fmt.Errorf("scan thumbnailless video: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list thumbnailless videos: %w", err)
	}
	return out, nil
}
