// Package playback persists the "now playing" pointer: the single video the
// user last opened in the Player (migration 0011_playback_state.sql). It exists
// so the pointer is a property of the account rather than of one browser tab —
// open a video on one device and the rail on another opens the same thing.
//
// Deliberately NOT part of package settings: that one holds user-configured
// preference, this is mutable session state with its own lifecycle (written on
// every Player mount, cleared by a watched toggle). Keeping them apart also
// keeps the pointer off the Settings page's PUT surface.
package playback

import (
	"context"
	"database/sql"
	"fmt"
)

// DBTX is the subset of *sql.DB used by this store, mirroring settings.DBTX.
type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// State is the now-playing pointer as the API returns it. VideoID is "" when
// nothing is playing — which covers both "never set" and "set, but the target is
// no longer playable" (see Get).
type State struct {
	VideoID   string `json:"video_id"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Store persists the playback_state singleton row.
type Store struct {
	db DBTX
}

// New returns a playback store backed by db.
func New(db DBTX) *Store {
	return &Store{db: db}
}

// Get returns the current pointer, or the zero State when there is nothing
// usable to point at.
//
// The INNER JOIN plus the status filter is the load-bearing part: a video the
// retention sweeper (or the user) deleted is TOMBSTONED, not removed, so the
// column's ON DELETE SET NULL never fires and a raw read would hand back an id
// whose media is gone. Anything not 'downloaded' — tombstoned, errored, still
// queued — reads as nothing playing, so the rail falls back to its empty state
// instead of opening a player that can't play.
func (s *Store) Get(ctx context.Context) (State, error) {
	var st State
	err := s.db.QueryRowContext(ctx, `
SELECT p.video_id, p.updated_at
FROM playback_state p
JOIN videos v ON v.id = p.video_id
WHERE p.id = 1 AND v.status = 'downloaded'`).Scan(&st.VideoID, &st.UpdatedAt)
	if err == sql.ErrNoRows {
		return State{}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("get playback state: %w", err)
	}
	return st, nil
}

// Set points at videoID. Idempotent, and never needs an upsert: migration 0011
// seeds the singleton row. The caller is responsible for having checked that the
// video exists and is playable — Get filters anyway, but storing a pointer at a
// video that was never downloaded would just be silently dead.
func (s *Store) Set(ctx context.Context, videoID string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE playback_state SET video_id = ?, updated_at = datetime('now') WHERE id = 1`, videoID)
	if err != nil {
		return fmt.Errorf("set playback state: %w", err)
	}
	return nil
}

// Clear drops the pointer, whatever it currently is.
func (s *Store) Clear(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE playback_state SET video_id = NULL, updated_at = datetime('now') WHERE id = 1`)
	if err != nil {
		return fmt.Errorf("clear playback state: %w", err)
	}
	return nil
}

// ClearIfVideo drops the pointer only when it is pointing at videoID.
//
// The conditional is not defensive noise: the callers are "this video was marked
// watched" and "this video was deleted", and by the time either runs the user
// may already be watching something else. An unconditional Clear would wipe a
// pointer that has legitimately moved on.
func (s *Store) ClearIfVideo(ctx context.Context, videoID string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE playback_state SET video_id = NULL, updated_at = datetime('now')
WHERE id = 1 AND video_id = ?`, videoID)
	if err != nil {
		return fmt.Errorf("clear playback state for %s: %w", videoID, err)
	}
	return nil
}
