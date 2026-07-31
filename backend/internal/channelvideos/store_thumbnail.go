package channelvideos

import (
	"context"
	"database/sql"
	"fmt"
)

// MaxThumbnailBytes bounds one cached inbox poster, matching the cap
// media.FetchImage enforces on the wire.
const MaxThumbnailBytes = 8 << 20 // 8 MiB

// Thumbnail is one cached inbox poster: the copy peeq serves so the browser
// never loads an inbox card's image from YouTube's CDN directly.
type Thumbnail struct {
	Mime      string
	Bytes     []byte
	UpdatedAt string
}

// SetThumbnail stores (or replaces) the cached poster for a pending video.
func (s *Store) SetThumbnail(videoID, mime string, data []byte) error {
	if videoID == "" {
		return fmt.Errorf("set pending thumbnail: empty video id")
	}
	if len(data) == 0 {
		return fmt.Errorf("set pending thumbnail %s: empty image", videoID)
	}
	if len(data) > MaxThumbnailBytes {
		return fmt.Errorf("set pending thumbnail %s: %d bytes exceeds cap %d", videoID, len(data), MaxThumbnailBytes)
	}
	if mime == "" {
		return fmt.Errorf("set pending thumbnail %s: empty mime", videoID)
	}
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO pending_thumbnails (video_id, mime, bytes, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT(video_id) DO UPDATE SET
	mime       = excluded.mime,
	bytes      = excluded.bytes,
	updated_at = excluded.updated_at`, videoID, mime, data)
	if err != nil {
		return fmt.Errorf("set pending thumbnail %s: %w", videoID, err)
	}
	return nil
}

// GetThumbnail returns the cached poster for videoID, or (nil, nil) if it has
// not been fetched yet — which is the ordinary state for a freshly scanned
// inbox item, and what makes the serve path fetch on demand.
func (s *Store) GetThumbnail(videoID string) (*Thumbnail, error) {
	var t Thumbnail
	err := s.db.QueryRowContext(context.Background(),
		`SELECT mime, bytes, updated_at FROM pending_thumbnails WHERE video_id = ?`, videoID,
	).Scan(&t.Mime, &t.Bytes, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pending thumbnail %s: %w", videoID, err)
	}
	return &t, nil
}

// DeleteThumbnail drops a cached inbox poster.
//
// This is the PRIMARY reclaim path, not a nicety: a channel_videos row survives
// approve, queue and ignore — only its state flips — so the ON DELETE CASCADE
// never fires on those transitions. It is the cascade that is the backstop
// here, covering channel deletion.
func (s *Store) DeleteThumbnail(videoID string) error {
	if _, err := s.db.ExecContext(context.Background(),
		`DELETE FROM pending_thumbnails WHERE video_id = ?`, videoID,
	); err != nil {
		return fmt.Errorf("delete pending thumbnail %s: %w", videoID, err)
	}
	return nil
}

// ThumbnaillessPendingIDs returns up to limit inbox items with no cached poster
// yet, for the import worker to look for under the old .pending/ cache.
//
// Scoped to state 'pending': a decided item is out of the inbox and its poster
// is never rendered again, so carrying one in would be work with no reader.
func (s *Store) ThumbnaillessPendingIDs(limit int) ([]string, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(context.Background(), `
SELECT cv.video_id
FROM channel_videos cv
WHERE cv.state = 'pending'
  AND NOT EXISTS (SELECT 1 FROM pending_thumbnails t WHERE t.video_id = cv.video_id)
ORDER BY cv.discovered_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list thumbnailless pending: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan thumbnailless pending: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list thumbnailless pending: %w", err)
	}
	return out, nil
}
