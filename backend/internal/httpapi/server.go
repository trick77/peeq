// Package httpapi builds vark's HTTP handler: the JSON API plus the embedded
// SPA. Phase 1 wires health, auth (dev auto-login + OIDC), the downloads
// queue, and the videos API (list/get/delete/stream/favorite/watched/
// resume); further archiving pipeline endpoints land in later phases.
package httpapi

import (
	"net/http"

	"github.com/trick77/vark/internal/auth"
	"github.com/trick77/vark/internal/jobs"
	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/sse"
	"github.com/trick77/vark/internal/videos"
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
	// Settings is the singleton settings store backing the Settings API.
	Settings *settings.Store
	// DevAuthClaims, when Subject is non-empty, makes /api/auth/login create a
	// session directly from these claims instead of redirecting to OIDC. Only
	// ever set when VARK_AUTH_MODE=dev (see config's loopback-only guard).
	DevAuthClaims auth.Claims

	// Jobs is the download queue store backing the downloads API.
	Jobs *jobs.Store
	// Videos is the video metadata store backing the downloads API.
	Videos *videos.Store
	// MediaDir is the root directory downloaded media lives under. The
	// videos API resolves every stored media_path against it (rejecting
	// traversal/escape) before streaming or unlinking a file.
	MediaDir string
	// Runner fetches metadata for a video before it is enqueued. May be a
	// fake in tests; the real *ytdlp.Runner satisfies DownloadsRunner.
	Runner DownloadsRunner
	// Worker cancels running/pending jobs. Optional: when nil, cancel falls
	// back to marking a pending job canceled directly in the Jobs store.
	Worker DownloadsWorker
	// SSEHub fans out download progress/queue events to /api/downloads/stream
	// subscribers. Optional: when nil, the stream endpoint returns 503.
	SSEHub *sse.Hub
}

type server struct {
	version       string
	static        http.Handler
	authSvc       *auth.Service
	authMW        *auth.Middleware
	settings      *settings.Store
	devAuthClaims auth.Claims

	jobs     *jobs.Store
	videos   *videos.Store
	mediaDir string
	runner   DownloadsRunner
	worker   DownloadsWorker
	sseHub   *sse.Hub
}

// New returns the fully wired HTTP handler.
func New(d Deps) http.Handler {
	s := &server{
		version:       d.Version,
		static:        d.Static,
		authSvc:       d.AuthService,
		authMW:        d.AuthMiddleware,
		settings:      d.Settings,
		devAuthClaims: d.DevAuthClaims,
		jobs:          d.Jobs,
		videos:        d.Videos,
		mediaDir:      d.MediaDir,
		runner:        d.Runner,
		worker:        d.Worker,
		sseHub:        d.SSEHub,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/auth/login", s.handleAuthLogin)
	mux.HandleFunc("GET /api/auth/callback", s.handleAuthCallback)
	mux.Handle("GET /api/auth/logout", s.requireAuth(http.HandlerFunc(s.handleAuthLogout)))
	mux.Handle("GET /api/auth/me", s.requireAuth(http.HandlerFunc(s.handleAuthMe)))
	mux.Handle("GET /api/videos", s.requireAuth(http.HandlerFunc(s.handleListVideos)))
	mux.Handle("GET /api/videos/{id}", s.requireAuth(http.HandlerFunc(s.handleGetVideo)))
	mux.Handle("DELETE /api/videos/{id}", s.requireAuth(http.HandlerFunc(s.handleDeleteVideo)))
	mux.Handle("POST /api/videos/{id}/favorite", s.requireAuth(http.HandlerFunc(s.handleFavoriteVideo)))
	mux.Handle("POST /api/videos/{id}/watched", s.requireAuth(http.HandlerFunc(s.handleWatchedVideo)))
	mux.Handle("POST /api/videos/{id}/resume", s.requireAuth(http.HandlerFunc(s.handleResumeVideo)))
	mux.Handle("GET /api/videos/{id}/stream", s.requireAuth(http.HandlerFunc(s.handleStreamVideo)))
	mux.Handle("GET /api/settings", s.requireAuth(http.HandlerFunc(s.handleGetSettings)))
	mux.Handle("PUT /api/settings", s.requireAuth(http.HandlerFunc(s.handlePutSettings)))
	mux.Handle("PUT /api/settings/cookie", s.requireAuth(http.HandlerFunc(s.handlePutSettingsCookie)))
	mux.Handle("GET /api/cookie/health", s.requireAuth(http.HandlerFunc(s.handleCookieHealth)))
	mux.Handle("POST /api/downloads", s.requireAuth(http.HandlerFunc(s.handleDownloadsPost)))
	mux.Handle("GET /api/downloads", s.requireAuth(http.HandlerFunc(s.handleDownloadsList)))
	mux.Handle("POST /api/downloads/{id}/cancel", s.requireAuth(http.HandlerFunc(s.handleDownloadsCancel)))
	mux.Handle("GET /api/downloads/stream", s.requireAuth(http.HandlerFunc(s.handleDownloadsStream)))
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
