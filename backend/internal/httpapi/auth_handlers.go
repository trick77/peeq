package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/logx"
	"github.com/trick77/ship/kit/httpx"
)

// handleAuthLogin starts a login. In dev mode (DevAuthClaims.Subject set) it
// short-circuits straight to a session, never contacting OIDC — peeq is
// single-user and dev auth only runs loopback-bound (enforced by config).
func (s *server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.devAuthClaims.Subject != "" {
		s.createSessionFromClaims(w, r, s.devAuthClaims)
		return
	}
	if !s.authSvc.OIDCConfigured() {
		httpx.WriteError(w, http.StatusServiceUnavailable, "oidc is not configured")
		return
	}
	s.authSvc.StartLogin(w, r)
}

// handleAuthCallback completes an OIDC login. The transient state/nonce
// cookies StartLogin set are cleared here regardless of outcome so they don't
// linger for their full ~10 minute lifetime.
func (s *server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if !s.authSvc.OIDCConfigured() {
		httpx.WriteError(w, http.StatusServiceUnavailable, "oidc is not configured")
		return
	}
	claims, err := s.authSvc.HandleCallback(r)
	s.authSvc.ClearOIDCCookies(w)
	if err != nil {
		// The browser only ever sees the generic code — the five distinct
		// failure modes in HandleCallback (bad state, bad nonce, code
		// exchange rejected, token verification failed) are otherwise
		// indistinguishable from the outside, which makes a misconfigured
		// provider undebuggable.
		slog.Warn("oidc callback failed", "err", logx.RedactErr(err))
		http.Redirect(w, r, "/?auth_error=oidc_callback_failed", http.StatusFound)
		return
	}
	s.createSessionFromClaims(w, r, claims)
}

// createSessionFromClaims upserts the local user from claims, creates a
// session, sets the session cookie, and redirects to the app root.
func (s *server) createSessionFromClaims(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if s.authSvc == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "auth is not configured")
		return
	}
	session, _, err := s.authSvc.CreateSessionFromClaims(r.Context(), claims)
	if err != nil {
		httpx.ServerError(w, r, err, "session create failed")
		return
	}
	http.SetCookie(w, s.authSvc.CookieFor(session.Token, session.ExpiresAt))
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if s.authSvc == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "auth is not configured")
		return
	}
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		if err := s.authSvc.Revoke(r.Context(), cookie.Value); err != nil {
			// The cookie is cleared regardless, but a session left live in
			// the DB is security-relevant — don't let it vanish silently.
			slog.Error("session revoke failed", "err", logx.RedactErr(err))
		}
	}
	http.SetCookie(w, s.authSvc.ClearCookie())
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"redirectUrl": "/"})
}

func (s *server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, user)
}
