// Package httpapi builds peeq's HTTP handler: the JSON API plus the embedded
// SPA. Phase 1 wires health, auth (dev auto-login + OIDC), the downloads
// queue, and the videos API (list/get/delete/stream/favorite/watched/
// resume); further archiving pipeline endpoints land in later phases.
package httpapi

import (
	"context"
	"net/http"

	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/channelmeta"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/sse"
	"github.com/trick77/peeq/internal/videos"
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
	// TokenMiddleware gates the machine endpoints on the API token. Optional:
	// when nil, PUT /api/machine/cookie returns 401 rather than being open.
	TokenMiddleware *auth.TokenMiddleware
	// Settings is the singleton settings store backing the Settings API.
	Settings *settings.Store
	// DevAuthClaims, when Subject is non-empty, makes /api/auth/login create a
	// session directly from these claims instead of redirecting to OIDC. Only
	// ever set when BACKEND_AUTH_MODE=dev (see config's loopback-only guard).
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
	// Metadata is the shared channel-metadata refresher: the fetch-and-store
	// step behind both the first-visit resolve here and the background weekly
	// refresh. Optional — when nil but Channels and ChannelResolver are set,
	// New builds an equivalent one, so a caller that does not run the
	// background worker (every test, today) needs no extra wiring. Production
	// passes the same instance the worker uses.
	Metadata *channelmeta.Refresher

	// Ledger is the per-channel scan ledger (channel_videos) backing the
	// pending API. Optional: when nil, the pending endpoints return 503.
	Ledger *channelvideos.Store

	// Rag is the transcript-chunk/embedding store backing semantic search
	// and the delete-purge path. Optional: when nil, /api/search returns
	// 503.
	Rag *rag.Store
	// Embedder turns search query text into an embedding vector to match
	// against Rag. Optional: when nil, /api/search returns 503.
	Embedder SearchEmbedder
	// SummaryJobs enqueues a (re)summarize job for a video. Optional: when
	// nil, /api/videos/{id}/resummarize returns 503.
	SummaryJobs SummaryEnqueuer
	// SummaryList reads the in-flight summary queue backing GET /api/summaries
	// (the Queue page's "being summarized" lane). Optional: when nil, that
	// endpoint reports an empty queue. Production wires the same
	// *summaryjobs.Store as SummaryJobs.
	SummaryList SummaryLister

	// OnResumeYoutube is invoked after POST /api/youtube/resume clears the
	// kill-switch, so the shared failure monitor gets reset and the user gets
	// a fresh auto-pause window. Optional: when nil, resume only clears the
	// settings flag.
	OnResumeYoutube func()

	// OnChannelResolved fires after a background channel-metadata resolve
	// settles, successfully or not. Test-only: it exists so a test can wait
	// for the goroutine instead of sleeping. nil in production.
	OnChannelResolved func(channelID string)
}

// SearchEmbedder embeds free-text search queries into vectors comparable
// against Rag's stored chunk embeddings.
type SearchEmbedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
}

// SummaryEnqueuer schedules a (re)summarize job for a video, returning the
// enqueued job's id.
type SummaryEnqueuer interface {
	Enqueue(videoID string) (int64, error)
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
	tokenMW       *auth.TokenMiddleware
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
	metadata        *channelmeta.Refresher

	ledger *channelvideos.Store

	rag         *rag.Store
	embedder    SearchEmbedder
	summaryJobs SummaryEnqueuer
	summaryList SummaryLister

	onResumeYoutube func()

	onChannelResolved func(channelID string)
}

// New returns the fully wired HTTP handler.
func New(d Deps) http.Handler {
	s := &server{
		version:       d.Version,
		static:        d.Static,
		authSvc:       d.AuthService,
		authMW:        d.AuthMiddleware,
		tokenMW:       d.TokenMiddleware,
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
		metadata:        d.Metadata,

		ledger: d.Ledger,

		rag:         d.Rag,
		embedder:    d.Embedder,
		summaryJobs: d.SummaryJobs,
		summaryList: d.SummaryList,

		onResumeYoutube: d.OnResumeYoutube,

		onChannelResolved: d.OnChannelResolved,
	}
	if s.metadata == nil && s.channels != nil && s.channelResolver != nil {
		s.metadata = &channelmeta.Refresher{
			Channels: s.channels,
			Resolver: s.channelResolver,
			MediaDir: s.mediaDir,
		}
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
	mux.Handle("POST /api/videos/{id}/category", s.requireAuth(http.HandlerFunc(s.handleCategoryVideo)))
	mux.Handle("POST /api/videos/{id}/resume", s.requireAuth(http.HandlerFunc(s.handleResumeVideo)))
	mux.Handle("POST /api/videos/{id}/redownload", s.requireAuth(http.HandlerFunc(s.handleRedownloadVideo)))
	mux.Handle("GET /api/videos/{id}/stream", s.requireAuth(http.HandlerFunc(s.handleStreamVideo)))
	mux.Handle("GET /api/videos/{id}/thumbnail", s.requireAuth(http.HandlerFunc(s.handleVideoThumbnail)))
	mux.Handle("GET /api/videos/{id}/subtitles", s.requireAuth(http.HandlerFunc(s.handleVideoSubtitles)))
	mux.Handle("GET /api/settings", s.requireAuth(http.HandlerFunc(s.handleGetSettings)))
	mux.Handle("PUT /api/settings", s.requireAuth(http.HandlerFunc(s.handlePutSettings)))
	mux.Handle("PUT /api/settings/cookie", s.requireAuth(http.HandlerFunc(s.handlePutSettingsCookie)))
	mux.Handle("GET /api/cookie/health", s.requireAuth(http.HandlerFunc(s.handleCookieHealth)))
	mux.Handle("GET /api/settings/token", s.requireAuth(http.HandlerFunc(s.handleGetAPIToken)))
	mux.Handle("POST /api/settings/token", s.requireAuth(http.HandlerFunc(s.handlePostAPIToken)))
	// The only route in peeq that bypasses OIDC. Token-gated, cookie-write
	// only — deliberately not a general machine surface.
	mux.Handle("PUT /api/machine/cookie", s.requireToken(http.HandlerFunc(s.handleMachineCookie)))
	mux.Handle("POST /api/downloads", s.requireAuth(http.HandlerFunc(s.handleDownloadsPost)))
	mux.Handle("GET /api/downloads", s.requireAuth(http.HandlerFunc(s.handleDownloadsList)))
	mux.Handle("GET /api/summaries", s.requireAuth(http.HandlerFunc(s.handleSummariesList)))
	mux.Handle("GET /api/downloads/status", s.requireAuth(http.HandlerFunc(s.handleDownloadsStatus)))
	mux.Handle("POST /api/downloads/{id}/cancel", s.requireAuth(http.HandlerFunc(s.handleDownloadsCancel)))
	mux.Handle("GET /api/downloads/stream", s.requireAuth(http.HandlerFunc(s.handleDownloadsStream)))
	mux.Handle("POST /api/youtube/pause", s.requireAuth(http.HandlerFunc(s.handlePauseYoutube)))
	mux.Handle("POST /api/youtube/resume", s.requireAuth(http.HandlerFunc(s.handleResumeYoutube)))
	mux.Handle("GET /api/ytdlp/version", s.requireAuth(http.HandlerFunc(s.handleYTDLPVersion)))
	mux.Handle("POST /api/ytdlp/update", s.requireAuth(http.HandlerFunc(s.handleYTDLPUpdate)))
	mux.Handle("POST /api/channels", s.requireAuth(http.HandlerFunc(s.handleChannelsPost)))
	mux.Handle("GET /api/channels", s.requireAuth(http.HandlerFunc(s.handleChannelsList)))
	mux.Handle("GET /api/channels/{id}", s.requireAuth(http.HandlerFunc(s.handleChannelDetail)))
	mux.Handle("PUT /api/channels/{id}", s.requireAuth(http.HandlerFunc(s.handleChannelsPut)))
	mux.Handle("DELETE /api/channels/{id}", s.requireAuth(http.HandlerFunc(s.handleChannelsDelete)))
	mux.Handle("POST /api/channels/{id}/subscribe", s.requireAuth(http.HandlerFunc(s.handleChannelsSubscribe)))
	mux.Handle("POST /api/channels/{id}/unsubscribe", s.requireAuth(http.HandlerFunc(s.handleChannelsUnsubscribe)))
	mux.Handle("POST /api/channels/{id}/scan", s.requireAuth(http.HandlerFunc(s.handleChannelScan)))
	mux.Handle("GET /api/channels/{id}/avatar", s.requireAuth(http.HandlerFunc(s.handleChannelAvatar)))
	mux.Handle("GET /api/channels/{id}/banner", s.requireAuth(http.HandlerFunc(s.handleChannelBanner)))
	mux.Handle("GET /api/channels/auto-unsubscribed", s.requireAuth(http.HandlerFunc(s.handleChannelsAutoUnsubscribedList)))
	mux.Handle("POST /api/channels/{id}/dismiss-dormant", s.requireAuth(http.HandlerFunc(s.handleChannelsDismissDormant)))
	mux.Handle("POST /api/channels/{id}/resubscribe", s.requireAuth(http.HandlerFunc(s.handleChannelsResubscribe)))
	mux.Handle("GET /api/pending", s.requireAuth(http.HandlerFunc(s.handlePendingList)))
	mux.Handle("POST /api/pending/{id}/download", s.requireAuth(http.HandlerFunc(s.handlePendingDownload)))
	mux.Handle("POST /api/pending/{id}/ignore", s.requireAuth(http.HandlerFunc(s.handlePendingIgnore)))
	mux.Handle("GET /api/search", s.requireAuth(http.HandlerFunc(s.handleSearch)))
	mux.Handle("POST /api/videos/{id}/resummarize", s.requireAuth(http.HandlerFunc(s.handleResummarize)))
	if s.static != nil {
		mux.Handle("/", s.static)
	}

	// recovery innermost so its 500 is written into the statusRecorder and the
	// request still gets an access-log line; logging's own body cannot panic.
	return logging(recovery(mux))
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

// requireToken gates a machine route on the API token. Mirrors requireAuth's
// nil-safety: an unwired middleware rejects rather than opens the route.
func (s *server) requireToken(next http.Handler) http.Handler {
	if s.tokenMW == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		})
	}
	return s.tokenMW.RequireToken(next)
}
