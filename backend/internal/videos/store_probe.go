package videos

import (
	"context"
	"fmt"
)

// ProbeResult is what mediaprobe read out of a downloaded file. A zero
// ProbeResult is a legitimate value: SetProbed stores it after a failed or
// empty probe so the attempt is still recorded.
type ProbeResult struct {
	Container   string
	VideoCodec  string
	VideoHeight int64
	AudioCodec  string
}

// SetProbed records a probe attempt against id.
//
// probed_at is stamped unconditionally — the caller is expected to call this
// on the failure path too, passing a zero ProbeResult. That is what stops the
// backfill sweep re-probing a deleted or corrupt file on every boot.
func (s *Store) SetProbed(id string, res ProbeResult) error {
	_, err := s.db.ExecContext(context.Background(), `
UPDATE videos
SET media_container = ?, video_codec = ?, video_height = ?, audio_codec = ?,
	probed_at = datetime('now')
WHERE id = ?`,
		res.Container, res.VideoCodec, res.VideoHeight, res.AudioCodec, id,
	)
	if err != nil {
		return fmt.Errorf("set video %s probed: %w", id, err)
	}
	return nil
}

// ProbeCandidate is one video whose media file has never been probed.
type ProbeCandidate struct {
	ID        string
	MediaPath string
}

// UnprobedDownloaded returns up to limit downloaded videos that still have a
// media file and have never been probed. Selecting on probed_at IS NULL (not
// on the value columns) is what makes the sweep converge: a file that yields
// nothing is still marked attempted and never comes back.
//
// Oldest first, so a library backfilled over several boots fills in the order
// it was built.
func (s *Store) UnprobedDownloaded(limit int) ([]ProbeCandidate, error) {
	rows, err := s.db.QueryContext(context.Background(), `
SELECT id, media_path FROM videos
WHERE status = 'downloaded' AND media_path != '' AND probed_at IS NULL
ORDER BY downloaded_at ASC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list unprobed videos: %w", err)
	}
	defer rows.Close()

	var out []ProbeCandidate
	for rows.Next() {
		var c ProbeCandidate
		if err := rows.Scan(&c.ID, &c.MediaPath); err != nil {
			return nil, fmt.Errorf("scan unprobed video: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unprobed videos: %w", err)
	}
	return out, nil
}
