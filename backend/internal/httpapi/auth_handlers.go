package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/trick77/peeq/internal/logx"
	"github.com/trick77/ship/kit/auth"
	"github.com/trick77/ship/kit/httpx"
)

// SessionCookie names the session cookie (kit/auth's Sessions): the claims
// travel in it, signed, so there is no session table to look up or revoke.
const SessionCookie = "peeq_session"

// handleAuthLogin starts a login. In dev mode (DevAuthClaims.Subject set) it
// short-circuits straight to a session, never contacting OIDC — peeq is
// single-user and dev auth only runs loopback-bound (enforced by config).
func (s *server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.devAuthClaims.Subject != "" {
		s.issueSession(w, r, s.devAuthClaims)
		return
	}
	if s.oidc == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "oidc is not configured")
		return
	}
	s.oidc.Start(w, r)
}

// handleAuthCallback completes an OIDC login. kit clears the transient state,
// nonce and PKCE cookies whatever the outcome.
func (s *server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "oidc is not configured")
		return
	}
	claims, err := s.oidc.Callback(w, r)
	if err != nil {
		// The browser only ever sees the generic code — the failure modes
		// (provider denied, bad state, bad nonce, code exchange rejected, token
		// verification failed) are otherwise indistinguishable from the
		// outside, which makes a misconfigured provider undebuggable.
		attrs := []any{"err", logx.RedactErr(err)}
		if errors.Is(err, auth.ErrNotInGroup) {
			attrs = append(attrs, "user", claims.Username())
		}
		slog.Warn("oidc callback failed", attrs...)
		http.Redirect(w, r, "/?auth_error=oidc_callback_failed", http.StatusFound)
		return
	}
	s.issueSession(w, r, claims)
}

// issueSession sets the session cookie for claims and redirects to the app root.
func (s *server) issueSession(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if s.sessions == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "auth is not configured")
		return
	}
	if err := s.sessions.Issue(w, claims); err != nil {
		httpx.ServerError(w, r, err, "session create failed")
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleAuthLogout clears the session cookie. That is all a logout can do: the
// session lives in the cookie, so a copy taken before stays valid until it
// expires (BACKEND_SESSION_TTL); rotating BACKEND_SESSION_SECRET ends them all.
func (s *server) handleAuthLogout(w http.ResponseWriter, _ *http.Request) {
	if s.sessions == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "auth is not configured")
		return
	}
	s.sessions.Clear(w)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"redirectUrl": "/"})
}

// handleAuthMe answers the session's identity, the claims the login carried.
func (s *server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := auth.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, claims)
}
