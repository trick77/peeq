// Package httpapi builds vark's HTTP handler: the JSON API plus the embedded
// SPA. Phase 1 wires health, auth (dev auto-login + OIDC), the downloads
// queue, and the videos API (list/get/delete/stream/favorite/watched/
// resume); further archiving pipeline endpoints land in later phases.
package httpapi

import (
	"context"
	"net/http"

	"github.com/trick77/vark/internal/auth"
	"github.com/trick77/vark/internal/channels"
	"github.com/trick77/vark/internal/channelvideos"
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
	// StreamAccess is notified on every /api/videos/{id}/stream request, so
	// the retention sweeper's now-playing guard (Task 12) can tell a
	// currently-playing video apart from a merely-eligible one. Optional:
	// when nil, the hook is skipped (no now-playing protection).
	StreamAccess StreamAccessRecorder
	// YTDLP backs the Settings page's yt-dlp version display and Update
	// button. Optional: when nil, both endpoints return 503 rather than
	// panic — a deployment without a resolvable yt-dlp binary still boots.
	YTDLP YTDLPVersioner

	// Channels is the tracked-channels/subscriptions store backing the
	// channels API. Optional: when nil, the channels endpoints return 503.
	Channels *channels.Store
	// ChannelResolver resolves a channel url to its authoritative UCID via
	// yt-dlp. Optional: when nil, POST /api/channels returns 503.
	ChannelResolver ChannelResolver

	// Ledger is the per-channel scan ledger (channel_videos) backing the
	// pending API. Optional: when nil, the pending endpoints return 503.
	Ledger *channelvideos.Store
}

// YTDLPVersioner is the subset of *ytdlp.Runner the settings API needs to
// show/refresh the installed yt-dlp version. Unlike Metadata/Download, these
// calls never touch YouTube, so implementations should skip the cookie gate
// and throttle entirely.
type YTDLPVersioner interface {
	Version(ctx context.Context) (string, error)
	UpdateLatest(ctx context.Context) (string, error)
}

// StreamAccessRecorder is the narrow interface handleStreamVideo needs to
// feed the retention sweeper's now-playing guard. httpapi deliberately
// defines this itself (rather than importing the retention package) so the
// dependency points the other way: retention may depend on httpapi-shaped
// concepts, httpapi never depends on retention.
type StreamAccessRecorder interface {
	RecordAccess(id string)
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

	streamAccess StreamAccessRecorder
	ytdlp        YTDLPVersioner

	channels        *channels.Store
	channelResolver ChannelResolver

	ledger *channelvideos.Store
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
		streamAccess:  d.StreamAccess,
		ytdlp:         d.YTDLP,

		channels:        d.Channels,
		channelResolver: d.ChannelResolver,

		ledger: d.Ledger,
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
	mux.Handle("GET /api/videos/{id}/thumbnail", s.requireAuth(http.HandlerFunc(s.handleVideoThumbnail)))
	mux.Handle("GET /api/settings", s.requireAuth(http.HandlerFunc(s.handleGetSettings)))
	mux.Handle("PUT /api/settings", s.requireAuth(http.HandlerFunc(s.handlePutSettings)))
	mux.Handle("PUT /api/settings/cookie", s.requireAuth(http.HandlerFunc(s.handlePutSettingsCookie)))
	mux.Handle("GET /api/cookie/health", s.requireAuth(http.HandlerFunc(s.handleCookieHealth)))
	mux.Handle("POST /api/downloads", s.requireAuth(http.HandlerFunc(s.handleDownloadsPost)))
	mux.Handle("GET /api/downloads", s.requireAuth(http.HandlerFunc(s.handleDownloadsList)))
	mux.Handle("GET /api/downloads/status", s.requireAuth(http.HandlerFunc(s.handleDownloadsStatus)))
	mux.Handle("POST /api/downloads/{id}/cancel", s.requireAuth(http.HandlerFunc(s.handleDownloadsCancel)))
	mux.Handle("GET /api/downloads/stream", s.requireAuth(http.HandlerFunc(s.handleDownloadsStream)))
	mux.Handle("GET /api/ytdlp/version", s.requireAuth(http.HandlerFunc(s.handleYTDLPVersion)))
	mux.Handle("POST /api/ytdlp/update", s.requireAuth(http.HandlerFunc(s.handleYTDLPUpdate)))
	mux.Handle("POST /api/channels", s.requireAuth(http.HandlerFunc(s.handleChannelsPost)))
	mux.Handle("GET /api/channels", s.requireAuth(http.HandlerFunc(s.handleChannelsList)))
	mux.Handle("PUT /api/channels/{id}", s.requireAuth(http.HandlerFunc(s.handleChannelsPut)))
	mux.Handle("DELETE /api/channels/{id}", s.requireAuth(http.HandlerFunc(s.handleChannelsDelete)))
	mux.Handle("POST /api/channels/{id}/subscribe", s.requireAuth(http.HandlerFunc(s.handleChannelsSubscribe)))
	mux.Handle("POST /api/channels/{id}/unsubscribe", s.requireAuth(http.HandlerFunc(s.handleChannelsUnsubscribe)))
	mux.Handle("GET /api/pending", s.requireAuth(http.HandlerFunc(s.handlePendingList)))
	mux.Handle("POST /api/pending/{id}/download", s.requireAuth(http.HandlerFunc(s.handlePendingDownload)))
	mux.Handle("POST /api/pending/{id}/ignore", s.requireAuth(http.HandlerFunc(s.handlePendingIgnore)))
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
