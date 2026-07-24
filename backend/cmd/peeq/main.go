// Command peeq is the all-in-one server: API + embedded SPA, backed by
// SQLite. This is the Task-5 boot milestone: config, DB, and auth are wired
// end to end (dev auto-login or OIDC) in front of an empty video library; the
// actual YouTube archiving pipeline arrives in later tasks.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/auth"
	"github.com/trick77/peeq/internal/channelmeta"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/config"
	"github.com/trick77/peeq/internal/download"
	"github.com/trick77/peeq/internal/failmonitor"
	"github.com/trick77/peeq/internal/httpapi"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/retention"
	"github.com/trick77/peeq/internal/scan"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/sponsorblock"
	"github.com/trick77/peeq/internal/sse"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/summarize"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/version"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
	"github.com/trick77/peeq/web"
)

func main() {
	// Configure structured logging with an explicit handler so every line
	// carries an RFC3339 timestamp (the package default does not guarantee
	// one). Installed before anything else so the startup banner and any
	// config-load failure are timestamped too. Level: BACKEND_LOG_LEVEL.
	logLevel := parseLogLevel(envDefault("BACKEND_LOG_LEVEL", "info"))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))

	slog.Info("starting peeq", "version", version.Version)
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func envDefault(key, def string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return def
}

// parseLogLevel maps a BACKEND_LOG_LEVEL string to a slog.Level, defaulting to
// Info for empty or unrecognized values.
func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// MediaDir and YtdlpDir are written to directly (downloaded media, the
	// self-updated yt-dlp binary); DBPath's parent holds the SQLite file.
	// None of these are guaranteed to pre-exist (e.g. a fresh bind-mounted
	// volume), so create them up front rather than fail deep inside a
	// download or self-update attempt.
	for _, dir := range []string{filepath.Dir(cfg.DBPath), cfg.MediaDir, cfg.YtdlpDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create dir %q: %w", dir, err)
		}
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := store.Migrate(db); err != nil {
		return err
	}

	// Fail loud on a stale (un-migrated) DB. The migrations were squashed, so a
	// leftover Phase-1 database boots past Migrate but lacks the Phase-2 tables,
	// which then surfaces as confusing runtime 500s. Probe one Phase-2 table
	// (channel_videos): an empty table (sql.ErrNoRows) is HEALTHY — only a
	// "no such table" error means the schema is stale, which is fatal.
	if err := db.QueryRowContext(context.Background(), "SELECT 1 FROM channel_videos LIMIT 1").Scan(new(int)); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("database schema is stale (missing Phase-2 tables): %w; recreate it with `rm ./data/peeq.db*` and restart", err)
	}

	// Secure (HTTPS-only) cookies whenever the app is reachable over https;
	// dev mode is loopback-only http, so this stays false there.
	secureCookies := strings.HasPrefix(cfg.PublicURL, "https://")
	sessions := auth.NewSessionStore(db, secureCookies)
	if _, err := sessions.DeleteExpired(context.Background()); err != nil {
		return err
	}
	users := auth.NewUserStore(db)
	authMW := auth.NewMiddleware(sessions, users)

	// The OIDC backend is only constructed (and discovery only attempted)
	// when AuthMode is oidc. In dev mode oidcSvc stays nil and devClaims is
	// populated instead; httpapi's dev short-circuit in handleAuthLogin means
	// the nil OIDC backend is never dereferenced (Service.StartLogin /
	// HandleCallback would panic on a nil backend otherwise).
	var oidcSvc *auth.OIDCService
	var devClaims auth.Claims
	switch cfg.AuthMode {
	case config.AuthModeOIDC:
		discovered, err := auth.NewOIDCServiceFromDiscovery(context.Background(), auth.OIDCServiceConfig{
			Issuer:       cfg.OIDC.Issuer,
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			RedirectURL:  cfg.OIDC.RedirectURL,
			SecureCookie: secureCookies,
		})
		if err != nil {
			return err
		}
		oidcSvc = discovered
	case config.AuthModeDev:
		slog.Warn("dev auth enabled; loopback only")
		if cfg.AllowAnonymousYoutube {
			// config.Load already refuses to boot with this set outside dev
			// auth, so reaching here means it's safe (loopback-only), but the
			// cookie invariant is still being relaxed and that must be loud.
			slog.Warn("anonymous YouTube access enabled; cookie invariant relaxed; dev only")
		}
		devClaims = auth.Claims{
			Subject:           cfg.Dev.Subject,
			PreferredUsername: cfg.Dev.Username,
			Email:             cfg.Dev.Email,
			Name:              cfg.Dev.DisplayName,
		}
	default:
		// AuthMode is unset. config.Load() accepts this without error (it
		// stays permissive so callers can validate at whichever layer fits),
		// but every route peeq serves is requireAuth-gated, so a server that
		// boots with no way to ever obtain a session is just broken with
		// extra steps. Reject at boot rather than come up silently inert.
		return fmt.Errorf("BACKEND_AUTH_MODE must be %q or %q (got %q)", config.AuthModeOIDC, config.AuthModeDev, cfg.AuthMode)
	}

	authSvc := auth.NewService(oidcSvc, sessions, users)
	settingsStore := settings.New(db)
	jobsStore := jobs.New(db)
	videosStore := videos.New(db)
	channelsStore := channels.New(db)
	ledgerStore := channelvideos.New(db)
	summaryJobsStore := summaryjobs.New(db)
	activityStore := activity.New(db)
	ragStore := rag.NewStore(db)
	embedClient := rag.NewEmbedClient(rag.EmbedConfig{
		BaseURL: cfg.EmbedBaseURL, APIKey: cfg.EmbedAPIKey, Model: cfg.EmbedModel,
		Logger: slog.Default(),
	}, nil)
	chatClient := llm.NewClient(llm.Config{
		BaseURL: cfg.ChatBaseURL, APIKey: cfg.ChatAPIKey,
		RequestInterval: cfg.SummarizeRequestDelay, Logger: slog.Default(),
		StreamIdleTimeout: cfg.ChatStreamIdleTimeout,
	}, nil)
	summarizer := summarize.New(chatClient)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Boot dim-guard: if the configured embedding dim differs from the vec_chunks
	// table's built dim, the whole vector table is invalid. This only warns —
	// it does not rebuild anything; recreating the DB is the operator's job.
	if builtDim, err := ragStore.BuiltDim(ctx); err == nil && builtDim != cfg.EmbedDim {
		slog.Warn("embedding dimension mismatch; vector table is stale",
			"built", builtDim, "configured", cfg.EmbedDim,
			"action", "recreate the database (rm ./data/peeq.db*) to rebuild vec_chunks at the new dimension")
	} else if err != nil {
		slog.Warn("dim-guard: could not read vec_chunks dimension", "err", err)
	}

	// The throttle floor is read once at boot; the Runner clamps whatever is
	// configured up to its own hard 20s minimum regardless.
	initialSettings, err := settingsStore.Get(ctx)
	if err != nil {
		return err
	}
	// failMonitor is shared between the download worker and scan scheduler:
	// it tracks consecutive distinct-entity failures across both, and once
	// threshold distinct videos/channels have failed in a row it engages the
	// youtube_paused kill-switch automatically (yt-dlp is most likely broken
	// against a YouTube change, not any single video/channel).
	const autoPauseReason = "Auto-paused after repeated extractor failures — yt-dlp may need updating. Update yt-dlp, then resume."
	failMonitor := failmonitor.New(3, func() {
		if err := settingsStore.SetYoutubePaused(context.Background(), true, autoPauseReason); err != nil {
			slog.Error("auto-pause: set youtube_paused failed", "err", err)
		}
	})

	runner := ytdlp.New(ytdlp.RunnerConfig{
		// Resolve the binary fresh on every invocation so the 24h self-update
		// (which writes into cfg.YtdlpDir) is picked up without a restart.
		BinResolver: func() string { return resolveYtdlpBin(cfg.YtdlpDir) },
		CookieProvider: func() (string, string) {
			// Read fresh on every call (not just at boot) so a cookie pasted
			// or invalidated while the worker is running takes effect on the
			// very next yt-dlp invocation.
			return settingsStore.CookieCredentials(context.Background())
		},
		PauseProvider: func() (bool, string) {
			return settingsStore.YoutubePaused(context.Background())
		},
		ThrottleFloor:  time.Duration(initialSettings.ThrottleBaseSeconds) * time.Second,
		MediaDir:       cfg.MediaDir,
		AllowAnonymous: cfg.AllowAnonymousYoutube,
	})

	sseHub := sse.NewHub()
	// Fan every recorded activity event out over the same SSE hub the download
	// progress and summary phases already ride (the frontend filters by event
	// name), so the Activity page's live row appears without a reload.
	activityStore.OnRecord = func(e activity.Event) {
		data, err := json.Marshal(e)
		if err != nil {
			return
		}
		sseHub.Publish("activity", string(data))
	}
	worker := download.New(download.Deps{
		Jobs:           jobsStore,
		Videos:         videosStore,
		Settings:       settingsStore,
		Runner:         runner,
		MediaDir:       cfg.MediaDir,
		SummaryJobs:    summaryJobsStore,
		DefaultSubLang: cfg.DefaultSubLang,
		YoutubePaused:  func() bool { p, _ := settingsStore.YoutubePaused(context.Background()); return p },
		FailMonitor:    failMonitor,
		Activity:       activityStore,
		OnProgress: func(jobID int64, p ytdlp.Progress) {
			data, err := json.Marshal(map[string]any{
				"job_id":  jobID,
				"percent": p.Percent,
				"speed":   p.Speed,
				"eta":     p.ETA,
			})
			if err != nil {
				return
			}
			sseHub.Publish("progress", string(data))
		},
	})
	// streamTracker is the retention sweeper's now-playing guard: the videos
	// stream handler records access here, and the sweeper consults it before
	// tombstoning a video that would otherwise qualify by age.
	streamTracker := retention.NewStreamAccessTracker()
	sweeper := retention.New(retention.Deps{
		Videos:   videosStore,
		Settings: settingsStore,
		MediaDir: cfg.MediaDir,
		Guard:    streamTracker,
		Activity: activityStore,
	})

	scheduler := scan.New(scan.Deps{
		Channels:       channelsStore,
		Ledger:         ledgerStore,
		Videos:         videosStore,
		Jobs:           jobsStore,
		Settings:       settingsStore,
		Lister:         runner,
		CookieStatus:   func(ctx context.Context) string { return settingsStore.CookieStatus(ctx) },
		AllowAnonymous: cfg.AllowAnonymousYoutube,
		YoutubePaused:  func(ctx context.Context) bool { p, _ := settingsStore.YoutubePaused(ctx); return p },
		FailMonitor:    failMonitor,
		Activity:       activityStore,
	})

	summarizeWorker := summarize.NewWorker(summarize.WorkerDeps{
		Jobs: summaryJobsStore, Videos: videosStore, Rag: ragStore,
		Summarizer: summarizer, Embedder: embedClient, MediaDir: cfg.MediaDir,
		EmbedModel: cfg.EmbedModel, EmbedDim: cfg.EmbedDim,
		VideoDelay: cfg.SummarizeVideoDelay,
		Activity:   activityStore,
		OnPhase: func(videoID, status, phase string) {
			data, err := json.Marshal(map[string]any{
				"video_id": videoID,
				"status":   status,
				"phase":    phase,
			})
			if err != nil {
				return
			}
			sseHub.Publish("summary", string(data))
		},
	})

	// metaRefresher is shared: the HTTP layer resolves a channel the first time
	// someone opens its page, and metaWorker re-reads subscribed channels once
	// a week. One instance, one implementation of fetch-and-store.
	metaRefresher := &channelmeta.Refresher{
		Channels: channelsStore,
		Resolver: runner,
		MediaDir: cfg.MediaDir,
	}
	metaWorker := channelmeta.NewWorker(channelmeta.Deps{
		Refresher:      metaRefresher,
		CookieStatus:   func(ctx context.Context) string { return settingsStore.CookieStatus(ctx) },
		AllowAnonymous: cfg.AllowAnonymousYoutube,
		YoutubePaused:  func(ctx context.Context) bool { p, _ := settingsStore.YoutubePaused(ctx); return p },
		Activity:       activityStore,
	})

	// sponsorblockWorker backfills and refreshes the segments the player skips
	// and marks. It is the only background loop that talks to a host other than
	// YouTube, and so deliberately carries none of the YouTube guards: no
	// cookie gate, no throttle, no kill-switch (see the package doc).
	sponsorblockWorker := sponsorblock.NewWorker(sponsorblock.Deps{
		Fetcher: sponsorblock.NewClient("", nil),
		Videos:  videosStore,
	})

	// Bound all seven background goroutines' lifetimes to the process: the
	// download worker, the retention sweeper, the yt-dlp self-update ticker,
	// the scan scheduler, the summarize worker, the channel-metadata
	// refresher, and the SponsorBlock backfill. workerWG.Wait() below (after
	// serve returns, i.e. after ctx is cancelled) blocks until all seven have
	// actually observed ctx.Done() and returned, rather than exiting the
	// process out from under them. All seven loops exit promptly on
	// ctx.Done(), so this wait is short.
	var workerWG sync.WaitGroup
	workerWG.Add(7)
	go func() {
		defer workerWG.Done()
		slog.Info("download worker started")
		worker.Run(ctx)
	}()
	go func() {
		defer workerWG.Done()
		slog.Info("retention sweeper started")
		sweeper.Run(ctx)
	}()
	go func() {
		defer workerWG.Done()
		runYtdlpSelfUpdateTicker(ctx, cfg.YtdlpDir, ytdlpSelfUpdateInterval, activityStore)
	}()
	go func() {
		defer workerWG.Done()
		slog.Info("scan scheduler started")
		scheduler.Run(ctx)
	}()
	go func() {
		defer workerWG.Done()
		slog.Info("summarize worker started")
		summarizeWorker.Run(ctx)
	}()
	go func() {
		defer workerWG.Done()
		slog.Info("channel metadata refresher started")
		metaWorker.Run(ctx)
	}()
	go func() {
		defer workerWG.Done()
		slog.Info("sponsorblock backfill started")
		sponsorblockWorker.Run(ctx)
	}()

	slog.Info("SSE hub ready")

	deps := httpapi.Deps{
		Version:         version.Version,
		Static:          web.Handler(),
		AuthService:     authSvc,
		AuthMiddleware:  authMW,
		TokenMiddleware: auth.NewTokenMiddleware(settingsStore),
		Settings:        settingsStore,
		DevAuthClaims:   devClaims,
		Jobs:            jobsStore,
		Videos:          videosStore,
		MediaDir:        cfg.MediaDir,
		Runner:          runner,
		Worker:          worker,
		SSEHub:          sseHub,
		StreamAccess:    streamTracker,
		YTDLP:           ytdlpVersioner{dir: cfg.YtdlpDir},
		OnResumeYoutube: failMonitor.Reset,

		Channels:        channelsStore,
		ChannelResolver: runner,
		Metadata:        metaRefresher,
		Ledger:          ledgerStore,

		Rag:         ragStore,
		Embedder:    embedClient,
		SummaryJobs: summaryJobsStore,
		SummaryList: summaryJobsStore,
		Activity:    activityStore,
	}
	handler := httpapi.New(deps)

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: handler,
		// net/http writes its own failures (request-parse errors, superfluous
		// WriteHeader, TLS handshake failures) here. Without this they bypass
		// slog entirely: unstructured, untimestamped, unfiltered by
		// BACKEND_LOG_LEVEL.
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelError),
	}

	err = serve(ctx, srv, sseHub)
	// serve can return either because ctx was cancelled (signal) or because
	// the listener itself failed to start/serve, in which case ctx is still
	// live. Stop it explicitly (idempotent; the deferred stop() above is a
	// no-op on top of this) so the worker's ctx.Done() check unblocks and
	// workerWG.Wait() below is always bounded, not just on the signal path.
	stop()
	workerWG.Wait()
	return err
}

// ytdlpVersioner adapts the ytdlp package's free functions (Version,
// UpdateLatest) to the httpapi.YTDLPVersioner interface the Settings page's
// version display/Update button need. dir is the yt-dlp install directory:
// Version resolves the binary from it fresh on every call (so a self-updated
// binary is reported without a restart), and UpdateLatest downloads the new
// release into it (see resolveYtdlpBin — dir/yt-dlp).
type ytdlpVersioner struct {
	dir string
}

func (v ytdlpVersioner) Version(ctx context.Context) (string, error) {
	return ytdlp.Version(ctx, resolveYtdlpBin(v.dir))
}

func (v ytdlpVersioner) UpdateLatest(ctx context.Context) (string, error) {
	return ytdlp.UpdateLatest(ctx, v.dir)
}

// ytdlpSelfUpdateInterval is how often the background ticker refreshes the
// yt-dlp binary in cfg.YtdlpDir. yt-dlp ships frequent releases that track
// YouTube's ever-changing player/extraction internals, so a stale binary is
// the single most common cause of downloads silently starting to fail; a
// daily check keeps that window small without hammering GitHub's releases
// endpoint.
const ytdlpSelfUpdateInterval = 24 * time.Hour

// runYtdlpSelfUpdateTicker logs the yt-dlp version already on disk at boot,
// then periodically re-runs ytdlp.UpdateLatest to fetch newer releases into
// dir. It never returns an error to the caller: a failed update (e.g. no
// network, GitHub rate limit) is logged and retried on the next tick rather
// than treated as fatal, since the existing binary keeps working in the
// meantime.
func runYtdlpSelfUpdateTicker(ctx context.Context, dir string, interval time.Duration, rec *activity.Store) {
	// Track the running version so the Activity record fires only on a real
	// change (the silence rule): the daily update is a no-op almost every day,
	// and a "yt-dlp updated" row every 24h with no version change is noise.
	var current string
	if v, err := ytdlp.Version(ctx, resolveYtdlpBin(dir)); err != nil {
		slog.Warn("yt-dlp not available at boot; downloads will fail until self-update succeeds", "err", err)
	} else {
		current = v
		slog.Info("yt-dlp self-update ticker started", "version", v, "interval", interval)
	}
	logJSRuntime(ctx, jsRuntimeBin)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			v, err := ytdlp.UpdateLatest(ctx, dir)
			if err != nil {
				slog.Warn("yt-dlp self-update failed", "err", err)
				continue
			}
			slog.Info("yt-dlp self-update succeeded", "version", v)
			if v != "" && v != current {
				if rec != nil {
					rec.Record(activity.Event{
						Kind: activity.KindYtdlp, Outcome: activity.OutcomeOK,
						Summary: "Updated to " + v,
					})
				}
				current = v
			}
		}
	}
}

// resolveYtdlpBin returns the path to the yt-dlp binary: <dir>/yt-dlp if it
// exists there as an executable regular file, otherwise the bare "yt-dlp"
// name so exec falls back to resolving it from PATH. It is called fresh on
// every yt-dlp invocation (via RunnerConfig.BinResolver and ytdlpVersioner),
// so once the self-update writes a binary into dir it is picked up without a
// restart.
//
// On Linux (the container target) the self-update writes exactly "yt-dlp"
// (see ytdlp.binaryName), so this matches. On macOS the self-update writes
// "yt-dlp_macos", so a dev box relies on the PATH binary rather than the
// self-updated one — acceptable, as production runs on Linux.
func resolveYtdlpBin(dir string) string {
	if dir == "" {
		return "yt-dlp"
	}
	candidate := filepath.Join(dir, "yt-dlp")
	if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
		return candidate
	}
	return "yt-dlp"
}

// jsRuntimeBin is the runtime yt-dlp auto-detects on PATH. deno is the only
// runtime yt-dlp enables by default, which is why no --js-runtimes flag is
// passed anywhere in this codebase.
const jsRuntimeBin = "deno"

// logJSRuntime reports which JavaScript runtime yt-dlp will find at run time.
// It is observability only and NEVER fatal: dev hosts legitimately have no
// runtime installed, and a warning that stops the process would block `make
// dev`. In production the warning is the point — a missing runtime otherwise
// degrades extraction silently, with formats disappearing rather than erroring.
func logJSRuntime(ctx context.Context, bin string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	v, err := ytdlp.JSRuntime(ctx, bin)
	if err != nil {
		slog.Warn("no JavaScript runtime for yt-dlp; YouTube extraction is on the deprecated path and some formats may be missing",
			"want", bin, "err", err)
		return
	}
	slog.Info("yt-dlp JavaScript runtime detected", "runtime", v)
}

// serve starts srv and blocks until either the server fails to start/serve
// (in which case that error is returned immediately, without waiting for
// ctx) or ctx is cancelled (in which case srv is shut down gracefully).
//
// hub.Close() is called before srv.Shutdown: http.Server.Shutdown does NOT
// cancel in-flight request contexts, it only waits for handlers to return —
// so an open SSE stream (which otherwise blocks on r.Context().Done(), which
// never fires during a graceful shutdown while the client stays connected)
// would make Shutdown block for the full 10s timeout and return
// context.DeadlineExceeded, one connected client at a time. Closing the hub
// first closes every subscriber channel, so the stream handler's select sees
// its channel close and returns immediately, and Shutdown completes fast.
func serve(ctx context.Context, srv *http.Server, hub *sse.Hub) error {
	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr, "version", version.Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	hub.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
