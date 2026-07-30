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
	"github.com/trick77/peeq/internal/embedjobs"
	"github.com/trick77/peeq/internal/failmonitor"
	"github.com/trick77/peeq/internal/httpapi"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/mediaprobe"
	"github.com/trick77/peeq/internal/playback"
	"github.com/trick77/peeq/internal/playbackgrant"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/reembed"
	"github.com/trick77/peeq/internal/retention"
	"github.com/trick77/peeq/internal/scan"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/sharelink"
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
	playbackStore := playback.New(db)
	jobsStore := jobs.New(db)
	videosStore := videos.New(db)
	shareLinksStore := sharelink.New(db)
	playbackGrantStore := playbackgrant.New(db)
	channelsStore := channels.New(db)
	ledgerStore := channelvideos.New(db)
	summaryJobsStore := summaryjobs.New(db)
	activityStore := activity.New(db)
	// Settings records cookie/access transitions to the same feed (post-construction, like OnRecord below).
	settingsStore.Activity = activityStore
	ragStore := rag.NewStore(db)
	embedClient := rag.NewEmbedClient(rag.EmbedConfig{
		BaseURL: cfg.EmbedBaseURL, APIKey: cfg.EmbedAPIKey, Model: cfg.EmbedModel,
		Logger: slog.Default(),
	}, nil)
	chatClient := llm.NewClient(llm.Config{
		BaseURL: cfg.ChatBaseURL, APIKey: cfg.ChatAPIKey,
		RequestInterval: cfg.SummarizeRequestDelay, Logger: slog.Default(),
		StreamIdleTimeout: cfg.ChatStreamIdleTimeout, CallTimeout: cfg.ChatCallTimeout,
	}, nil)
	summarizer := summarize.New(chatClient, summarize.WithSummaryChunkTokens(cfg.SummaryChunkTokens))

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
	// prober reads what a downloaded file actually is (container, codecs,
	// resolution) with ffprobe, which ships in the image alongside the ffmpeg
	// yt-dlp already needs for merging.
	prober := mediaprobe.New(mediaprobe.Config{})

	worker := download.New(download.Deps{
		Jobs:           jobsStore,
		Videos:         videosStore,
		Settings:       settingsStore,
		Runner:         runner,
		Prober:         prober,
		Channels:       channelsStore,
		Ledger:         ledgerStore,
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
		Prober:         runner,
		CookieStatus:   func(ctx context.Context) string { return settingsStore.CookieStatus(ctx) },
		AllowAnonymous: cfg.AllowAnonymousYoutube,
		YoutubePaused:  func(ctx context.Context) bool { p, _ := settingsStore.YoutubePaused(ctx); return p },
		FailMonitor:    failMonitor,
		Activity:       activityStore,
		MediaDir:       cfg.MediaDir,
	})

	// Re-embed backfill. Rebuilds the search index of any video whose chunks
	// predate the current content recipe — the boot sweep below is what actually
	// re-indexes an existing library after a recipe change. It makes no chat
	// calls: everything a rebuild needs is already stored.
	//
	// This is one-shot migration machinery, not permanent infrastructure. Issue
	// #240 tracks removing it once the drain has completed everywhere.
	embedJobsStore := embedjobs.New(db)
	if n, err := embedJobsStore.ResetOrphans(); err != nil {
		slog.Error("re-embed: reset orphans failed", "err", err)
	} else if n > 0 {
		slog.Info("re-embed: reset orphaned jobs", "jobs", n)
	}
	if n, err := embedJobsStore.EnqueueStale(rag.ChunkRecipeRev); err != nil {
		slog.Error("re-embed: backfill sweep failed", "err", err)
	} else if n > 0 {
		slog.Info("re-embed: backfill enqueued", "videos", n, "rev", rag.ChunkRecipeRev)
	}
	reembedWorker := reembed.New(reembed.Deps{
		Jobs: embedJobsStore, Videos: videosStore, Rag: ragStore, Embedder: embedClient,
		MediaDir:   cfg.MediaDir,
		EmbedModel: cfg.EmbedModel, EmbedDim: cfg.EmbedDim,
		PollInterval: cfg.ReembedPollInterval,
		VideoDelay:   cfg.ReembedVideoDelay,
		BatchDelay:   cfg.ReembedBatchDelay,
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

	// mediaprobeWorker fills in the media facts for everything downloaded
	// before peeq probed anything, so an existing library shows a full stat
	// strip without being re-downloaded. It goes idle once drained. Like the
	// SponsorBlock backfill it touches no network at all — here, not even
	// another host: it reads local files with a local binary.
	mediaprobeWorker := mediaprobe.NewWorker(mediaprobe.Deps{
		Prober: prober,
		Videos: videosStore,
	})

	// ytdlpStatus is written by the version-check ticker and read by the
	// version endpoint, so the Settings page and the nav rail can report an
	// available yt-dlp update without a GitHub call per request.
	ytdlpStatus := ytdlp.NewStatusCache()

	// Bound all nine background goroutines' lifetimes to the process: the
	// download worker, the retention sweeper, the yt-dlp version-check ticker,
	// the scan scheduler, the summarize worker, the channel-metadata
	// refresher, the SponsorBlock backfill, the media-probe backfill and the
	// re-embed backfill. workerWG.Wait() below (after serve returns, i.e. after
	// ctx is cancelled) blocks until all nine have actually observed ctx.Done()
	// and returned, rather than exiting the process out from under them. All
	// nine loops exit promptly on ctx.Done(), so this wait is short.
	var workerWG sync.WaitGroup
	workerWG.Add(9)
	go func() {
		defer workerWG.Done()
		slog.Info("re-embed worker started")
		reembedWorker.Run(ctx)
	}()
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
		runYtdlpVersionCheckTicker(ctx, cfg.YtdlpDir, ytdlpCheckInterval, ytdlp.LatestVersion, ytdlpStatus, activityStore)
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
	go func() {
		defer workerWG.Done()
		slog.Info("media probe backfill started")
		mediaprobeWorker.Run(ctx)
	}()

	slog.Info("SSE hub ready")

	// The shell backs the share page's link-preview meta. A read failure is not
	// fatal — the SPA still serves; only the unfurl degrades to a bare title.
	shell, err := web.IndexHTML()
	if err != nil {
		slog.Warn("index.html unreadable; share links will not unfurl", "err", err)
	}

	deps := httpapi.Deps{
		Version:         version.Version,
		Static:          web.Handler(),
		Shell:           shell,
		AuthService:     authSvc,
		AuthMiddleware:  authMW,
		TokenMiddleware: auth.NewTokenMiddleware(settingsStore),
		Settings:        settingsStore,
		Playback:        playbackStore,
		DevAuthClaims:   devClaims,
		Jobs:            jobsStore,
		Videos:          videosStore,
		ShareLinks:      shareLinksStore,
		PlaybackGrants:  playbackGrantStore,
		PublicURL:       cfg.PublicURL,
		MediaDir:        cfg.MediaDir,
		Runner:          runner,
		Worker:          worker,
		SSEHub:          sseHub,
		StreamAccess:    streamTracker,
		YTDLP:           ytdlpVersioner{dir: cfg.YtdlpDir, status: ytdlpStatus},
		OnResumeYoutube: failMonitor.Reset,

		Channels:        channelsStore,
		ChannelResolver: runner,
		Metadata:        metaRefresher,
		Ledger:          ledgerStore,

		Rag:               ragStore,
		Embedder:          embedClient,
		SearchMaxDistance: cfg.SearchMaxDistance,

		SummaryJobs: summaryJobsStore,
		SummaryList: summaryJobsStore,
		Activity:    activityStore,

		AllowAnonymous: cfg.AllowAnonymousYoutube,
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
// Version resolves the binary from it fresh on every call (so an updated
// binary is reported without a restart), and UpdateLatest downloads the new
// release into it (see resolveYtdlpBin — dir/yt-dlp).
//
// status is the shared cache the version-check ticker fills, so the same
// endpoint can also report the newest published release without making a
// GitHub call per request.
type ytdlpVersioner struct {
	dir    string
	status *ytdlp.StatusCache
}

func (v ytdlpVersioner) Version(ctx context.Context) (string, error) {
	return ytdlp.Version(ctx, resolveYtdlpBin(v.dir))
}

func (v ytdlpVersioner) UpdateLatest(ctx context.Context) (string, error) {
	version, err := ytdlp.UpdateLatest(ctx, v.dir)
	if err != nil {
		return "", err
	}
	// Fold the new version straight into the cache so an "update available"
	// indicator clears the moment the user acts on it, rather than staying up
	// for up to a full check interval after the update it was asking for.
	if v.status != nil {
		v.status.SetInstalled(version)
	}
	return version, nil
}

func (v ytdlpVersioner) Latest(ctx context.Context) (string, time.Time, string) {
	if v.status == nil {
		return "", time.Time{}, ""
	}
	got := v.status.Get()
	return got.Latest, got.CheckedAt, got.CheckErr
}

// ytdlpCheckInterval is how often the background ticker asks GitHub which
// yt-dlp release is newest. yt-dlp ships frequent releases that track
// YouTube's ever-changing player/extraction internals, so a stale binary is
// the single most common cause of downloads silently starting to fail —
// noticing a new release within a few hours keeps that window small, while
// four unauthenticated API calls a day sit far under GitHub's rate limit.
const ytdlpCheckInterval = 6 * time.Hour

// runYtdlpVersionCheckTicker records the yt-dlp version on disk and the
// newest published release into status, at boot and on every tick, so the UI
// can report that an update is available.
//
// It deliberately does NOT install anything. peeq used to blind-download the
// latest release here every 24h, which swapped the extractor underneath the
// user with no announcement and no way to correlate a behaviour change with
// the binary that caused it. Installing is now the Settings page's Update
// button and nothing else; this ticker only ever reports.
//
// It never returns an error to the caller: a failed check (no network, GitHub
// rate limit) is recorded on the cache, logged, and retried on the next tick.
// The last known release is kept rather than blanked, so a temporary outage
// does not silently downgrade "an update is waiting" to "you look current".
func runYtdlpVersionCheckTicker(
	ctx context.Context,
	dir string,
	interval time.Duration,
	fetchLatest func(context.Context) (string, error),
	status *ytdlp.StatusCache,
	rec *activity.Store,
) {
	// Track the release last reported so the Activity record fires only when a
	// release is newly discovered (the silence rule): most checks find the same
	// version, and a "new yt-dlp release" row every few hours would be noise.
	//
	// The boot check seeds this WITHOUT recording, which is why announced is
	// assigned on the boot path below. Nothing here is persisted, so a restart
	// re-discovers whatever is pending — and a user who leaves an update
	// unapplied for a week while restarting daily would otherwise collect one
	// identical row per boot. The rail indicator and the Settings note are what
	// carry a standing pending update; Activity logs the discovery.
	var announced string

	check := func(boot bool) {
		installed, err := ytdlp.Version(ctx, resolveYtdlpBin(dir))
		if err != nil {
			installed = ""
			if boot {
				slog.Warn("yt-dlp not available at boot; downloads will fail until it is installed", "err", err)
			} else {
				slog.Warn("yt-dlp version unreadable", "err", err)
			}
		}

		latest, err := fetchLatest(ctx)
		if err != nil {
			// ctx cancellation on shutdown is not a check failure worth
			// recording; it just means we are on our way out.
			if ctx.Err() != nil {
				return
			}
			status.SetCheckErr(installed, err.Error())
			slog.Warn("yt-dlp release check failed", "err", err)
			return
		}
		status.SetChecked(installed, latest, time.Now())

		got := status.Get()
		if boot {
			slog.Info("yt-dlp version check started",
				"version", installed, "latest", latest, "interval", interval)
		}
		if !got.UpdateAvailable() {
			return
		}
		slog.Info("yt-dlp update available", "version", installed, "latest", latest)
		if latest == announced {
			return
		}
		// Seed on boot rather than record. See announced's declaration: an
		// update that was already pending before this process started is not
		// news, and logging it again on every restart is exactly the noise the
		// silence rule exists to keep out of the agenda.
		if !boot && rec != nil {
			rec.Record(activity.Event{
				Kind: activity.KindYtdlp, Outcome: activity.OutcomeWarn,
				Summary: "yt-dlp " + latest + " available",
				Detail:  "Installed " + installed + ". Update from Settings.",
			})
		}
		announced = latest
	}

	check(true)
	logJSRuntime(ctx, jsRuntimeBin)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			check(false)
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
