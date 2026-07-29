// Package playbackgrant persists the short-lived, auth-free URLs that let an
// AirPlay receiver fetch a video's media file directly.
//
// A grant is a capability URL, like a share link, but aimed at a device rather
// than a person: AirPlay hands the media URL to the Apple TV, which requests it
// with no session cookie, so the URL itself has to be the credential. Three
// properties make that acceptable:
//
//   - It is unguessable. peeq's video id IS the YouTube id, so any public route
//     keyed on the video id would be enumerable; a grant is 32 bytes of entropy
//     instead, and the token→video mapping never crosses the wire.
//   - It expires. Unlike a share link, which may live forever by design, a grant
//     is always bounded (see migration 0017_direct_stream.sql).
//   - It is revocable in bulk. The stream handler re-reads the
//     direct_stream_enabled setting on every request, so turning the feature off
//     kills every outstanding grant at once, without a restart.
//
// This is a separate table and a separate token namespace from sharelink on
// purpose: holding a grant must never reveal a share token, or the reverse.
package playbackgrant

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

// tokenBytes is the raw entropy per grant token. Twice sharelink's 16, because
// this token is never displayed to a human or typed by one — it only ever
// appears inside a <video> src — so there is no compactness to trade against.
// base64url-encodes to a 43-char path segment.
const tokenBytes = 32

// sqliteTime is the UTC datetime layout grant expiries are stored in, matching
// share_links and sessions so `expires_at > datetime('now')` compares correctly.
const sqliteTime = "2006-01-02 15:04:05"

// DefaultTTL is how long a minted grant stays valid. It has to outlive a whole
// viewing, not just the request that mints it: the receiver keeps issuing range
// requests for as long as playback continues, and a paused film resumed after
// dinner must not die mid-stream with no way to re-authenticate from the TV.
// Twelve hours covers any video plus a long interruption while still ensuring a
// leaked URL is dead the same day.
const DefaultTTL = 12 * time.Hour

// Store persists playback grants.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// generateToken reads tokenBytes of entropy from r and base64url-encodes it. A
// short read is an error — a truncated token would be a weak credential.
func generateToken(r io.Reader) (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("read grant entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Mint issues a fresh grant for a video and returns its token and expiry.
//
// Unlike sharelink.Upsert it never reuses a live token. A share link is reused
// so that adjusting its expiry doesn't invalidate a URL the owner already sent
// to someone; a grant is never sent to anyone, so reuse would buy nothing and
// cost the ability to let two players hold independent, independently-expiring
// grants for the same video.
//
// A ttl of zero or less falls back to DefaultTTL: the column is NOT NULL, so
// "never expires" is not a representable state here, and silently persisting a
// zero-length grant would mint a URL that is dead on arrival.
func (s *Store) Mint(ctx context.Context, videoID string, ttl time.Duration) (token string, expiresAt string, err error) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	// Prune before minting rather than from a background worker: grants are
	// only ever created here, so this is the one place the table can grow, and
	// it keeps the feature from needing its own goroutine. Best-effort — a
	// failed prune must not stop a viewer from playing a video.
	_ = s.PruneExpired(ctx)

	token, err = generateToken(rand.Reader)
	if err != nil {
		return "", "", err
	}
	expiresAt = time.Now().UTC().Add(ttl).Format(sqliteTime)
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO playback_grants (token, video_id, expires_at)
VALUES (?, ?, ?)`, token, videoID, expiresAt); err != nil {
		return "", "", fmt.Errorf("create playback grant: %w", err)
	}
	return token, expiresAt, nil
}

// Resolve returns the video id a live token grants access to. A token that is
// unknown OR expired resolves to ok=false with no error — the caller must treat
// both identically (a plain 404) so a dead grant can't be told apart from one
// that never existed.
func (s *Store) Resolve(ctx context.Context, token string) (videoID string, ok bool, err error) {
	if token == "" {
		return "", false, nil
	}
	err = s.db.QueryRowContext(ctx, `
SELECT video_id FROM playback_grants
WHERE token = ? AND expires_at > datetime('now')`, token).Scan(&videoID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve playback grant: %w", err)
	}
	return videoID, true, nil
}

// PruneExpired deletes grants that are past their expiry. Expired rows are
// already unresolvable, so this is housekeeping, not enforcement.
func (s *Store) PruneExpired(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM playback_grants WHERE expires_at <= datetime('now')`); err != nil {
		return fmt.Errorf("prune playback grants: %w", err)
	}
	return nil
}
