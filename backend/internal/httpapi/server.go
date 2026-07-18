// Package httpapi builds vark's HTTP handler: the JSON API plus the embedded
// SPA. Phase 1 wires health, auth (dev auto-login + OIDC), and a placeholder
// videos listing; the archiving pipeline endpoints land in later phases.
package httpapi

import (
	"net/http"

	"github.com/trick77/vark/internal/auth"
)

// Deps are the dependencies needed to build the server.
type Deps struct {
	// Version is the running build version, surfaced on /healthz.
	Version string
	// Static serves the embedded SPA; may be nil in tests.
	Static http.Handler

	// AuthService drives login/callback/logout and session creation.
	AuthService *auth.Service
	// AuthMiddleware protects authenticated routes.
	AuthMiddleware *auth.Middleware
	// DevAuthClaims, when Subject is non-empty, makes /api/auth/login create a
	// session directly from these claims instead of redirecting to OIDC. Only
	// ever set when VARK_AUTH_MODE=dev (see config's loopback-only guard).
	DevAuthClaims auth.Claims
}

type server struct {
	version       string
	static        http.Handler
	authSvc       *auth.Service
	authMW        *auth.Middleware
	devAuthClaims auth.Claims
}

// New returns the fully wired HTTP handler.
func New(d Deps) http.Handler {
	s := &server{
		version:       d.Version,
		static:        d.Static,
		authSvc:       d.AuthService,
		authMW:        d.AuthMiddleware,
		devAuthClaims: d.DevAuthClaims,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/auth/login", s.handleAuthLogin)
	mux.HandleFunc("GET /api/auth/callback", s.handleAuthCallback)
	mux.Handle("GET /api/auth/logout", s.requireAuth(http.HandlerFunc(s.handleAuthLogout)))
	mux.Handle("GET /api/auth/me", s.requireAuth(http.HandlerFunc(s.handleAuthMe)))
	mux.Handle("GET /api/videos", s.requireAuth(http.HandlerFunc(s.handleListVideos)))
	if s.static != nil {
		mux.Handle("/", s.static)
	}

	return mux
}

// requireAuth wraps next with session authentication. If no middleware is
// configured at all (a wiring bug, not a supported mode) requests are
// rejected rather than let through unauthenticated — requireAuth must never
// fail open.
func (s *server) requireAuth(next http.Handler) http.Handler {
	if s.authMW == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		})
	}
	return s.authMW.RequireAuth(next)
}

// handleListVideos is a Phase 1 placeholder: the archiving pipeline and its
// storage land in a later task. It proves the authenticated route table and
// the "empty library" boot milestone.
func (s *server) handleListVideos(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, []any{})
}
