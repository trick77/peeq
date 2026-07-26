package videos

import (
	"context"
	"fmt"
)

// SponsorblockCandidate is one video due a SponsorBlock segment lookup.
// DurationSeconds travels with it because the client needs it to reject
// segments submitted against a differently-cut copy of the video.
type SponsorblockCandidate struct {
	ID              string
	DurationSeconds int64
}

// SponsorblockRefreshInterval is how long stored segments are allowed to stand
// before they are re-read. Segments keep being submitted and voted on after a
// video is published, so a video peeq downloaded on release day is exactly the
// one most likely to have gained segments since.
const SponsorblockRefreshInterval = "-30 days"

// ClaimSponsorblockStale returns up to limit downloaded videos whose
// SponsorBlock segments need reading: never-fetched ones first (empty
// sponsorblock_refreshed_at sorts before any timestamp), then the oldest
// reads. Like the channel-metadata claim it is a plain SELECT rather than an
// atomic state flip — the worker is a single goroutine, so there is no second
// claimant to race against.
//
// Tombstoned videos are excluded: their media is gone, so there is nothing
// left to skip through.
func (s *Store) ClaimSponsorblockStale(limit int) ([]SponsorblockCandidate, error) {
	// duration_seconds is nullable and IS null for rows that never got full
	// metadata (imports, in particular). COALESCE keeps that a scannable zero,
	// which the client reads as "duration unknown" and handles by skipping its
	// stale-submission check rather than rejecting every segment.
	rows, err := s.db.QueryContext(context.Background(), `
SELECT id, COALESCE(duration_seconds, 0)
FROM videos
WHERE status = 'downloaded'
  AND (sponsorblock_refreshed_at = ''
       OR sponsorblock_refreshed_at <= datetime('now', ?))
ORDER BY sponsorblock_refreshed_at ASC
LIMIT ?`, SponsorblockRefreshInterval, limit)
	if err != nil {
		return nil, fmt.Errorf("claim sponsorblock stale: %w", err)
	}
	defer rows.Close()

	var out []SponsorblockCandidate
	for rows.Next() {
		var c SponsorblockCandidate
		if err := rows.Scan(&c.ID, &c.DurationSeconds); err != nil {
			return nil, fmt.Errorf("claim sponsorblock stale: scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim sponsorblock stale: %w", err)
	}
	return out, nil
}

// SetSponsorblockSegments stores the segments JSON for id and stamps the
// refresh time. It is called with "[]" for a video that genuinely has no
// segments, and the stamp is what stops that video being asked about again on
// every pass — the far more common case than a video with segments.
func (s *Store) SetSponsorblockSegments(id, segmentsJSON string) error {
	if segmentsJSON == "" {
		segmentsJSON = "[]"
	}
	_, err := s.db.ExecContext(context.Background(), `
UPDATE videos
SET sponsorblock_segments = ?, sponsorblock_refreshed_at = datetime('now')
WHERE id = ?`, segmentsJSON, id)
	if err != nil {
		return fmt.Errorf("set video %s sponsorblock segments: %w", id, err)
	}
	return nil
}

// ResetSponsorblockRefresh clears sponsorblock_refreshed_at back to the
// never-fetched sentinel (”), which sorts first in ClaimSponsorblockStale, so
// the SponsorBlock worker re-fetches this video's segments on its next claim.
// Used by the Player's Reprocess action to force a fresh fetch; the stored
// segments are left in place until the re-fetch overwrites them.
func (s *Store) ResetSponsorblockRefresh(id string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET sponsorblock_refreshed_at = '' WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("reset video %s sponsorblock refresh: %w", id, err)
	}
	return nil
}
