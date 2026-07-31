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
