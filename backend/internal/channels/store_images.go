package channels

import (
	"context"
	"database/sql"
	"fmt"
)

// Image kinds. A channel has at most one of each.
const (
	ImageAvatar = "avatar"
	ImageBanner = "banner"
)

// MaxImageBytes bounds one stored image, matching the cap media.FetchImage
// already enforces on the wire. Avatars and banners are well under it; the cap
// is there so a hostile or broken server cannot bloat the database.
const MaxImageBytes = 8 << 20 // 8 MiB

// Image is one stored channel avatar or banner.
type Image struct {
	Mime      string
	Bytes     []byte
	UpdatedAt string
}

// SetImage stores (or replaces) one of a channel's images.
//
// Only ever called with bytes actually fetched: a failed refresh must skip this
// entirely rather than write an empty row, or it would blank artwork that is
// perfectly good — the same "filling a hole is fine, punching one is not" rule
// the avatar_path/banner_path COALESCE guards encode.
func (s *Store) SetImage(channelID, kind, mime string, data []byte) error {
	if channelID == "" {
		return fmt.Errorf("set channel image: empty channel id")
	}
	if kind != ImageAvatar && kind != ImageBanner {
		return fmt.Errorf("set channel image %s: unknown kind %q", channelID, kind)
	}
	if len(data) == 0 {
		return fmt.Errorf("set channel %s %s: empty image", channelID, kind)
	}
	if len(data) > MaxImageBytes {
		return fmt.Errorf("set channel %s %s: %d bytes exceeds cap %d", channelID, kind, len(data), MaxImageBytes)
	}
	if mime == "" {
		return fmt.Errorf("set channel %s %s: empty mime", channelID, kind)
	}
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO channel_images (channel_id, kind, mime, bytes, updated_at)
VALUES (?, ?, ?, ?, datetime('now'))
ON CONFLICT(channel_id, kind) DO UPDATE SET
	mime       = excluded.mime,
	bytes      = excluded.bytes,
	updated_at = excluded.updated_at`, channelID, kind, mime, data)
	if err != nil {
		return fmt.Errorf("set channel %s %s: %w", channelID, kind, err)
	}
	return nil
}

// GetImage returns one stored image, or (nil, nil) when the channel has none of
// that kind.
func (s *Store) GetImage(channelID, kind string) (*Image, error) {
	var img Image
	err := s.db.QueryRowContext(context.Background(),
		`SELECT mime, bytes, updated_at FROM channel_images WHERE channel_id = ? AND kind = ?`,
		channelID, kind,
	).Scan(&img.Mime, &img.Bytes, &img.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel %s %s: %w", channelID, kind, err)
	}
	return &img, nil
}
