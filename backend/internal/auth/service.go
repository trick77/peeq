package auth

import (
	"context"
	"net/http"
	"time"
)

// Service composes OIDC login/callback handling with session and user
// persistence. It is the entry point other packages (HTTP handlers) use.
type Service struct {
	oidc     *OIDCService
	sessions *SessionStore
	users    *UserStore
}

// NewService returns a Service wiring together OIDC, sessions, and users.
func NewService(oidc *OIDCService, sessions *SessionStore, users *UserStore) *Service {
	return &Service{oidc: oidc, sessions: sessions, users: users}
}

// StartLogin redirects the browser to Authentik and sets state/nonce cookies.
func (s *Service) StartLogin(w http.ResponseWriter, r *http.Request) {
	s.oidc.StartLogin(w, r)
}

// HandleCallback validates the OIDC callback and returns the verified claims.
// It does not create a user or session; call CreateSessionFromClaims next.
func (s *Service) HandleCallback(r *http.Request) (Claims, error) {
	return s.oidc.HandleCallback(r)
}

// CreateSessionFromClaims upserts the local user from claims and creates a
// new session for them, returning both.
func (s *Service) CreateSessionFromClaims(ctx context.Context, claims Claims) (Session, User, error) {
	user, err := s.users.UpsertFromClaims(ctx, claims)
	if err != nil {
		return Session{}, User{}, err
	}
	session, err := s.sessions.Create(ctx, user.ID, SessionTTL)
	if err != nil {
		return Session{}, User{}, err
	}
	return session, user, nil
}

// Revoke deletes the session identified by the raw token (logout).
func (s *Service) Revoke(ctx context.Context, token string) error {
	return s.sessions.Revoke(ctx, token)
}

// CookieFor builds the browser session cookie for a raw token.
func (s *Service) CookieFor(token string, expires time.Time) *http.Cookie {
	return s.sessions.CookieFor(token, expires)
}

// ClearCookie returns a cookie that clears the browser session (logout).
func (s *Service) ClearCookie() *http.Cookie {
	return s.sessions.ClearCookie()
}
