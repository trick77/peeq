package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/trick77/vark/internal/auth"
)

// handleAuthLogin starts a login. In dev mode (DevAuthClaims.Subject set) it
// short-circuits straight to a session, never contacting OIDC — vark is
// single-user and dev auth only runs loopback-bound (enforced by config).
func (s *server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.devAuthClaims.Subject != "" {
		s.createSessionFromClaims(w, r, s.devAuthClaims)
		return
	}
	if !s.authSvc.OIDCConfigured() {
		writeJSONError(w, http.StatusServiceUnavailable, "oidc is not configured")
		return
	}
	s.authSvc.StartLogin(w, r)
}

// handleAuthCallback completes an OIDC login. The transient state/nonce
// cookies StartLogin set are cleared here regardless of outcome so they don't
// linger for their full ~10 minute lifetime.
func (s *server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if !s.authSvc.OIDCConfigured() {
		writeJSONError(w, http.StatusServiceUnavailable, "oidc is not configured")
		return
	}
	claims, err := s.authSvc.HandleCallback(r)
	s.authSvc.ClearOIDCCookies(w)
	if err != nil {
		http.Redirect(w, r, "/?auth_error=oidc_callback_failed", http.StatusFound)
		return
	}
	s.createSessionFromClaims(w, r, claims)
}

// createSessionFromClaims upserts the local user from claims, creates a
// session, sets the session cookie, and redirects to the app root.
func (s *server) createSessionFromClaims(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if s.authSvc == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "auth is not configured")
		return
	}
	session, _, err := s.authSvc.CreateSessionFromClaims(r.Context(), claims)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "session create failed")
		return
	}
	http.SetCookie(w, s.authSvc.CookieFor(session.Token, session.ExpiresAt))
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if s.authSvc == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "auth is not configured")
		return
	}
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		_ = s.authSvc.Revoke(r.Context(), cookie.Value)
	}
	http.SetCookie(w, s.authSvc.ClearCookie())
	writeJSON(w, map[string]string{"redirectUrl": "/"})
}

func (s *server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	user, ok := auth.UserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, user)
}

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
