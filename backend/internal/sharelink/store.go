// Package sharelink persists the public share links that let an unauthenticated
// viewer stream a single video on the chromeless /s/<token> page.
//
// A share link is a capability URL: an opaque, high-entropy token that grants
// read access to exactly one video, optionally until an expiry, and is revocable
// at any time. There is one live link per video — re-sharing replaces the old
// token — which is what Create's upsert against the UNIQUE(video_id) index
// enforces. See migration 0008_share_links.sql for why the raw token (not just a
// hash) is stored here, unlike sessions and the machine api token.
package sharelink

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"
)

// tokenBytes is the raw entropy per share token. 16 bytes (128 bits) is ample
// for a capability URL that also expires and can be revoked, and base64url-
// encodes to a compact 22-char path segment.
const tokenBytes = 16

// sqliteTime is the UTC datetime layout share expiries are stored in, matching
// the sessions table so `expires_at > datetime('now')` compares correctly.
const sqliteTime = "2006-01-02 15:04:05"

// Store persists share links.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Link is a share link as stored: the raw token, the video it grants access to,
// its expiry (empty string = never), and when it was created.
type Link struct {
	Token     string
	VideoID   string
	ExpiresAt string
	CreatedAt string
}

// generateToken reads tokenBytes of entropy from r and base64url-encodes it. A
// short read is an error — a truncated token would be a weak credential.
func generateToken(r io.Reader) (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("read token entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Upsert sets the share link for a video to the given lifetime and returns it.
// It keeps the SAME token when the video already has a live link — so changing
// the expiry from the popover updates the existing link in place rather than
// invalidating one the owner may have already handed out — and mints a fresh
// token only when there is no live link yet (never shared, or the previous one
// expired). A ttl of zero or less means the link never expires. The two cases
// run in one transaction so a concurrent share of the same video can't
// double-mint.
func (s *Store) Upsert(ctx context.Context, videoID string, ttl time.Duration) (Link, error) {
	var expires sql.NullString
	if ttl > 0 {
		expires = sql.NullString{
			String: time.Now().UTC().Add(ttl).Format(sqliteTime),
			Valid:  true,
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Link{}, fmt.Errorf("begin share upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Is there a live link already? If so, keep its token and only re-stamp
	// the expiry.
	var token string
	err = tx.QueryRowContext(ctx, `
SELECT token FROM share_links
WHERE video_id = ? AND (expires_at IS NULL OR expires_at > datetime('now'))`, videoID).Scan(&token)
	switch {
	case err == nil:
		if _, err := tx.ExecContext(ctx,
			`UPDATE share_links SET expires_at = ? WHERE video_id = ?`, expires, videoID); err != nil {
			return Link{}, fmt.Errorf("update share expiry: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		token, err = generateToken(rand.Reader)
		if err != nil {
			return Link{}, err
		}
		// ON CONFLICT(video_id) replaces a stale (expired) row for this video.
		if _, err := tx.ExecContext(ctx, `
INSERT INTO share_links (token, video_id, expires_at)
VALUES (?, ?, ?)
ON CONFLICT(video_id) DO UPDATE SET
  token = excluded.token,
  expires_at = excluded.expires_at,
  created_at = datetime('now')`, token, videoID, expires); err != nil {
			return Link{}, fmt.Errorf("create share link: %w", err)
		}
	default:
		return Link{}, fmt.Errorf("look up live share link: %w", err)
	}

	var createdAt string
	if err := tx.QueryRowContext(ctx,
		`SELECT created_at FROM share_links WHERE video_id = ?`, videoID).Scan(&createdAt); err != nil {
		return Link{}, fmt.Errorf("read share created_at: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Link{}, fmt.Errorf("commit share upsert: %w", err)
	}
	return Link{
		Token:     token,
		VideoID:   videoID,
		ExpiresAt: expires.String,
		CreatedAt: createdAt,
	}, nil
}

// Resolve returns the video id a live token grants access to. A token that is
// unknown OR expired resolves to ok=false with no error — the caller must treat
// both identically (a plain 404) so a dead token can't be told apart from one
// that never existed.
func (s *Store) Resolve(ctx context.Context, token string) (videoID string, ok bool, err error) {
	if token == "" {
		return "", false, nil
	}
	err = s.db.QueryRowContext(ctx, `
SELECT video_id FROM share_links
WHERE token = ? AND (expires_at IS NULL OR expires_at > datetime('now'))`, token).Scan(&videoID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve share link: %w", err)
	}
	return videoID, true, nil
}

// GetByVideo returns the current live link for a video, or nil if the video has
// no share link or its link has expired. Expired links read as "not shared" so
// the owner UI can offer a fresh share rather than a stale one.
func (s *Store) GetByVideo(ctx context.Context, videoID string) (*Link, error) {
	var l Link
	var expires sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT token, video_id, expires_at, created_at FROM share_links
WHERE video_id = ? AND (expires_at IS NULL OR expires_at > datetime('now'))`, videoID).
		Scan(&l.Token, &l.VideoID, &expires, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get share link by video: %w", err)
	}
	l.ExpiresAt = expires.String
	return &l, nil
}

// DeleteByVideo removes any share link for a video ("stop sharing"). Removing a
// link that isn't there is not an error — stop-sharing is idempotent.
func (s *Store) DeleteByVideo(ctx context.Context, videoID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM share_links WHERE video_id = ?`, videoID); err != nil {
		return fmt.Errorf("delete share link: %w", err)
	}
	return nil
}
