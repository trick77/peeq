package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// SessionCookieName is the browser cookie holding the raw (unhashed) session
// token. Only the SHA-256 hash of the token is ever persisted server-side.
const SessionCookieName = "peeq_session"

// SessionTTL is how long a session remains valid after creation.
const SessionTTL = 30 * 24 * time.Hour

// Session is the server-side representation of an authenticated browser session.
type Session struct {
	Token     string
	UserID    string
	ExpiresAt time.Time
}

// SessionStore persists opaque browser sessions.
type SessionStore struct {
	db     DBTX
	secure bool
}

// NewSessionStore returns a SQLite-backed session store. secure controls the
// Secure flag on issued cookies and should be true whenever PublicURL is https.
func NewSessionStore(db DBTX, secure bool) *SessionStore {
	return &SessionStore{db: db, secure: secure}
}

// Create stores a hashed session token and returns the raw token for the cookie.
func (s *SessionStore) Create(ctx context.Context, userID string, ttl time.Duration) (Session, error) {
	token := randomToken()
	expiresAt := time.Now().UTC().Add(ttl)
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (token_hash, user_id, expires_at)
VALUES (?, ?, ?)`,
		hashToken(token), userID, formatTime(expiresAt),
	)
	if err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}
	return Session{Token: token, UserID: userID, ExpiresAt: expiresAt}, nil
}

// lastSeenGranularity is how stale last_seen_at may be before a lookup
// refreshes it. The column is a coarse "still in use" marker, and a write
// on every authenticated request — every thumbnail, every media range —
// would take SQLite's write lock behind whatever a worker is writing.
const lastSeenGranularity = time.Minute

// Lookup returns the active session for token.
func (s *SessionStore) Lookup(ctx context.Context, token string) (Session, bool, error) {
	var session Session
	var expires, lastSeen string
	hash := hashToken(token)
	err := s.db.QueryRowContext(ctx, `
SELECT user_id, expires_at, last_seen_at
FROM sessions
WHERE token_hash = ? AND expires_at > datetime('now')`,
		hash,
	).Scan(&session.UserID, &expires, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("lookup session: %w", err)
	}
	expiresAt, err := parseDBTime(expires)
	if err != nil {
		return Session{}, false, err
	}
	session.Token = token
	session.ExpiresAt = expiresAt
	s.touch(ctx, hash, lastSeen)
	return session, true, nil
}

// touch refreshes last_seen_at when it is at least lastSeenGranularity old.
// The decision is made from the row Lookup just read, so a request inside
// the window issues no statement at all: an UPDATE whose WHERE matches
// nothing still takes the write lock. One cutoff, computed here, serves both
// the decision and the statement's guard (which stays as the tiebreak
// between two concurrent stale lookups), so the two cannot disagree at the
// edge. A stamp that does not parse is stale by definition and is rewritten
// without the guard, or it would never heal.
func (s *SessionStore) touch(ctx context.Context, hash, lastSeen string) {
	cutoff := time.Now().UTC().Add(-lastSeenGranularity)
	seen, perr := parseDBTime(lastSeen)
	if perr == nil && seen.After(cutoff) {
		return
	}
	query := `UPDATE sessions SET last_seen_at = datetime('now') WHERE token_hash = ? AND last_seen_at <= ?`
	args := []any{hash, formatTime(cutoff)}
	if perr != nil {
		query = `UPDATE sessions SET last_seen_at = datetime('now') WHERE token_hash = ?`
		args = args[:1]
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		// Not a failure of the lookup: the marker is coarse and the next
		// request retries. Debug, so a busy lock is diagnosable.
		slog.Debug("session last_seen refresh failed", "err", err)
	}
}

// DeleteExpired removes expired sessions and returns how many rows were deleted.
func (s *SessionStore) DeleteExpired(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= datetime('now')`)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count deleted sessions: %w", err)
	}
	return deleted, nil
}

// Revoke deletes the session for token.
func (s *SessionStore) Revoke(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// CookieFor builds the browser session cookie.
func (s *SessionStore) CookieFor(token string, expires time.Time) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // HttpOnly and SameSite are set below; Secure is config-driven so local development over plain HTTP still works
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// ClearCookie returns a cookie that clears the browser session.
func (s *SessionStore) ClearCookie() *http.Cookie {
	return &http.Cookie{ //nolint:gosec // HttpOnly and SameSite are set below; Secure is config-driven so local development over plain HTTP still works
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

func randomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}

func parseDBTime(value string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse db time: %w", err)
	}
	return t, nil
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
