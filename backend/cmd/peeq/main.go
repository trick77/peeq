// Command peeq is the all-in-one server: the JSON API and the embedded SPA,
// backed by SQLite, with the scan, download, summarize and upkeep workers
// running beside it in the same process.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/captionfetch"
	"github.com/trick77/peeq/internal/channelmeta"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/config"
	"github.com/trick77/peeq/internal/download"
	"github.com/trick77/peeq/internal/failmonitor"
	"github.com/trick77/peeq/internal/httpapi"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/logx"
	"github.com/trick77/peeq/internal/mediaprobe"
	"github.com/trick77/peeq/internal/playback"
	"github.com/trick77/peeq/internal/playbackgrant"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/retention"
	"github.com/trick77/peeq/internal/scan"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/sharelink"
	"github.com/trick77/peeq/internal/sponsorblock"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/summarize"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/tokenauth"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
	"github.com/trick77/peeq/web"
	"github.com/trick77/ship/kit/auth"
	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/logging"
	kitrun "github.com/trick77/ship/kit/run"
	"github.com/trick77/ship/kit/sse"
	"github.com/trick77/ship/kit/version"
)

func main() {
	// Installed before anything else, so the startup banner and a config-load
	// failure go out in the configured shape. kit's Setup refuses an unknown
	// BACKEND_LOG_LEVEL rather than fall back to info. logx.Handler scrubs
	// query strings and userinfo from every error logged under "err", so the
	// "never log a URL" rule holds for every line rather than only where a
	// call site remembered to wrap.
	if err := setupLogging(os.Stderr, os.Getenv("BACKEND_LOG_LEVEL")); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}

	slog.Info("starting peeq", "version", version.String())
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// setupLogging installs kit's logger at level, behind peeq's redactor.
func setupLogging(w io.Writer, level string) error {
	l, err := logging.Setup(logging.Options{Level: level, LevelVar: "BACKEND_LOG_LEVEL", Writer: w})
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(logx.Handler(l.Handler())))
	return nil
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
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create dir %q: %w", dir, err)
		}
	}

	// Sign-in before the database: a provider that cannot be reached, or a
	// config it refuses, stops the boot before any migration has run (0043
	// drops tables, and a boot that dies after it cannot be rolled back
	// without a restore). Discovery is bounded, so a blackholed provider
	// fails the boot instead of hanging it before anything listens.
	authCtx, cancelAuth := context.WithTimeout(context.Background(), 30*time.Second)
	oidc, sessions, devClaims, err := newAuth(authCtx, cfg)
	cancelAuth()
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := store.Migrate(context.Background(), db); err != nil {
		return err
	}

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
	embedClient, chatClient, askClient, err := newModelClients(cfg)
	if err != nil {
		return err
	}
	summarizer := summarize.New(chatClient, summarize.WithSummaryChunkTokens(cfg.SummaryChunkTokens))

	ctx, stop := kitrun.Context(context.Background())
	defer stop()

	// Boot width check: vec_chunks is built at one width (a literal in its
	// migration), and the configured embedding model must return exactly that.
	// A mismatch refuses boot: it is not degraded search but every insert
	// failing. Switching to a model of another width is a re-index — a migration
	// that rebuilds vec_chunks, then every video re-embedded — which this does
	// not do on its own.
	if builtDim, err := ragStore.BuiltDim(ctx); err != nil {
		slog.Warn("vector width check: could not read vec_chunks dimension", "err", err)
	} else if err := rag.CheckVecWidth(builtDim, embedClient.Model(), embedClient.Dim()); err != nil {
		return err
	}
	// Same width is not the same model: two models of one width embed into
	// different spaces, so the model that wrote the stored vectors must be the
	// configured one too.
	if err := ragStore.CheckEmbedModel(ctx, embedClient.Model()); err != nil {
		return err
	}
	// vec0 never frees a deleted vector's storage, and every search reads all
	// of it (rag.CompactVectors). Compacted here, before any worker or request
	// can write, and vacuumed only then: the rewrite blocks writers, which at
	// this point is nobody.
	rag.LogVectorIndexAtBoot(ctx, db, slog.Default())

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
			// A read error already reports paused; the Runner needs no more
			// than that to refuse the call.
			paused, reason, _ := settingsStore.YoutubePaused(context.Background())
			return paused, reason
		},
		IPFamilyProvider: func() string {
			// Per call, like the cookie: a Settings change applies to the
			// next yt-dlp invocation. Bounded: it runs while the call holds
			// the one YouTube turn, and a write lock held for minutes (the
			// boot-time vector compaction) must not stall the queue behind it.
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return settingsStore.YtdlpIPFamily(ctx)
		},
		ThrottleFloor:  time.Duration(initialSettings.ThrottleBaseSeconds) * time.Second,
		MediaDir:       cfg.MediaDir,
		AllowAnonymous: cfg.AllowAnonymousYoutube,
	})

	sseHub := sse.NewHub[httpapi.Event](sse.HubOptions{})
	// Fan every recorded activity event out over the same SSE hub the download
	// progress and summary phases already ride (the frontend filters by event
	// name), so the Activity page's live row appears without a reload.
	activityStore.OnRecord = func(e activity.Event) {
		data, err := json.Marshal(e)
		if err != nil {
			return
		}
		sseHub.Publish(httpapi.Event{Name: "activity", Data: string(data)})
	}
	// prober reads what a downloaded file actually is (container, codecs,
	// resolution) with ffprobe, which ships in the image alongside the ffmpeg
	// yt-dlp already needs for merging.
	prober := mediaprobe.New(mediaprobe.Config{})

	worker := download.New(download.Deps{
		DB:             db,
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
		YoutubePaused:  func() bool { return settingsStore.Paused(context.Background()) },
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
			sseHub.Publish(httpapi.Event{Name: "progress", Data: string(data)})
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
		CookieStatus:   settingsStore.CookieStatus,
		AllowAnonymous: cfg.AllowAnonymousYoutube,
		YoutubePaused:  settingsStore.Paused,
		FailMonitor:    failMonitor,
		Activity:       activityStore,
		Images:         runner.FetchImage,
	})

	summarizeWorker := summarize.NewWorker(summarize.WorkerDeps{
		Jobs: summaryJobsStore, Videos: videosStore, Channels: channelsStore, Rag: ragStore,
		Summarizer: summarizer, Embedder: embedClient,
		EmbedModel: embedClient.Model(), EmbedDim: embedClient.Dim(),
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
			sseHub.Publish(httpapi.Event{Name: "summary", Data: string(data)})
		},
	})

	// captionWorker reads pending inbox videos: it fetches captions on their own
	// and hands them to summarizeWorker, so a video can be judged before it is
	// downloaded. It shares the YouTube queue with the scans, and deliberately
	// does not mark its calls interactive — nobody is waiting on one.
	captionWorker := captionfetch.NewWorker(captionfetch.Deps{
		Fetcher:   runner,
		Ledger:    ledgerStore,
		Videos:    videosStore,
		Summaries: summaryJobsStore,
		MediaDir:  cfg.MediaDir,
		// Must match the download worker's, three declarations up: if the two
		// ever ask YouTube for different caption languages, the transcript
		// summarized from the Inbox is not the one the library ends up with.
		DefaultSubLang: cfg.DefaultSubLang,
		// The card's caption state lives on rows this worker writes and no
		// other event covers, so an open Inbox learns it changed here.
		OnChange: func(videoID string) { httpapi.PublishInboxChanged(sseHub, videoID) },
	})

	// metaRefresher is shared: the HTTP layer resolves a channel the first time
	// someone opens its page, and metaWorker re-reads subscribed channels once
	// a week. One instance, one implementation of fetch-and-store.
	metaRefresher := &channelmeta.Refresher{
		Channels: channelsStore,
		Resolver: runner,
		Images:   runner.FetchImage,
		MediaDir: cfg.MediaDir,
	}
	metaWorker := channelmeta.NewWorker(channelmeta.Deps{
		Refresher:      metaRefresher,
		CookieStatus:   settingsStore.CookieStatus,
		AllowAnonymous: cfg.AllowAnonymousYoutube,
		YoutubePaused:  settingsStore.Paused,
		Activity:       activityStore,
	})

	// sponsorblockWorker keeps current the segments the player skips
	// and marks. Despite the name it has always used, this never drains: it
	// re-reads every video on a 30-day cycle, because segments keep being
	// submitted long after a video is published. It is the only background loop
	// that talks to a host other than YouTube, and so deliberately carries none
	// of the YouTube guards: no cookie gate, no throttle, no kill-switch (see
	// the package doc).
	sponsorblockWorker := sponsorblock.NewWorker(sponsorblock.Deps{
		Fetcher: sponsorblock.NewClient("", nil),
		Videos:  videosStore,
	})

	// mediaprobeWorker fills in the media facts for any video with no probe
	// attempt recorded, so the stat strip is complete. It does not drain
	// either: the download worker's inline probe leaves probed_at NULL when it
	// fails, so this is the retry path for every one of those (see the package
	// doc). Like the SponsorBlock loop it touches no network at all — here, not
	// even another host: it reads local files with a local binary.
	mediaprobeWorker := mediaprobe.NewWorker(mediaprobe.Deps{
		Prober: prober,
		Videos: videosStore,
	})

	// ytdlpStatus is written by the version-check ticker and read by the
	// version endpoint, so the Settings page and the nav rail can report an
	// available yt-dlp update without a GitHub call per request.
	ytdlpStatus := ytdlp.NewStatusCache()

	// Bound every long-running loop started here to the process. workerWG.Wait()
	// below (after serve returns, i.e. after ctx is cancelled) blocks until each
	// loop has actually observed ctx.Done() and returned, rather than exiting the
	// process out from under it. Every loop exits promptly on ctx.Done(), so the
	// wait is short. One helper rather than a hand-kept Add(N) and nine copied
	// closures: the count cannot drift from the starts.
	var workerWG sync.WaitGroup
	startWorker := func(name string, run func(context.Context)) {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			slog.Info("worker started", "worker", name)
			run(ctx)
		}()
	}
	startWorker("download worker", worker.Run)
	startWorker("retention sweeper", sweeper.Run)
	startWorker("yt-dlp version check", func(ctx context.Context) {
		runYtdlpVersionCheckTicker(ctx, cfg.YtdlpDir, ytdlpCheckInterval, ytdlp.LatestVersion, ytdlpStatus, activityStore)
	})
	startWorker("scan scheduler", scheduler.Run)
	startWorker("summarize worker", summarizeWorker.Run)
	startWorker("channel metadata refresher", metaWorker.Run)
	startWorker("sponsorblock refresher", sponsorblockWorker.Run)
	startWorker("media probe worker", mediaprobeWorker.Run)
	startWorker("inbox caption fetcher", captionWorker.Run)

	slog.Info("SSE hub ready")

	// The shell backs the share page's link-preview meta. A read failure is not
	// fatal — the SPA still serves; only the unfurl degrades to a bare title.
	shell, err := web.IndexHTML()
	if err != nil {
		slog.Warn("index.html unreadable; share links will not unfurl", "err", err)
	}

	deps := httpapi.Deps{
		Version:         version.String(),
		Ready:           []httpx.Check{{Name: "db", Run: db.PingContext}},
		Static:          web.Handler(),
		Shell:           shell,
		OIDC:            oidc,
		Sessions:        sessions,
		TokenMiddleware: tokenauth.NewTokenMiddleware(settingsStore),
		Settings:        settingsStore,
		Playback:        playbackStore,
		DevAuthClaims:   devClaims,
		Jobs:            jobsStore,
		Videos:          videosStore,
		ShareLinks:      shareLinksStore,
		PlaybackGrants:  playbackGrantStore,
		PublicURL:       cfg.PublicURL,
		MCPToken:        cfg.MCPToken,
		MediaDir:        cfg.MediaDir,
		Runner:          runner,
		Worker:          worker,
		SSEHub:          sseHub,
		StreamAccess:    streamTracker,
		YTDLP:           ytdlpVersioner{dir: cfg.YtdlpDir, status: ytdlpStatus, installed: &ytdlp.VersionCache{}},
		OnResumeYoutube: failMonitor.Reset,

		Channels:        channelsStore,
		ChannelResolver: runner,
		Metadata:        metaRefresher,
		Images:          runner.FetchImage,
		QueueThumbnail:  scheduler.QueueThumbnail,
		Background:      ctx,
		StartWorker:     startWorker,
		Ledger:          ledgerStore,

		Rag:               ragStore,
		Embedder:          embedClient,
		SearchMaxDistance: cfg.SearchMaxDistance,
		Ask:               askClient,
		// The same unpaced client as Ask, and for the same reason: this call also
		// has a person waiting on it. It routes itself to the short-gate
		// deployment per-call (see understand.go), so it needs no client of its
		// own — the deployment is a context knob, not a connection.
		Understand: askClient,

		SummaryJobs:   summaryJobsStore,
		SummaryList:   summaryJobsStore,
		Activity:      activityStore,
		ActivityWrite: activityStore,

		AllowAnonymous: cfg.AllowAnonymousYoutube,
	}
	handler := httpapi.New(deps)

	srv := newServer(cfg.Addr, handler, sseHub)
	slog.Info("listening", "addr", srv.Addr, "version", version.String())
	err = httpx.Serve(ctx, srv, 10*time.Second)
	// Serve can return either because ctx was cancelled (signal) or because
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
// Version resolves the binary from it on every call and re-reads it whenever
// the file changed (so an updated binary is reported without a restart), and
// UpdateLatest downloads the new release into it (see resolveYtdlpBin and
// ytdlp.InstalledBin).
//
// status is the shared cache the version-check ticker fills, so the same
// endpoint can also report the newest published release without making a
// GitHub call per request.
type ytdlpVersioner struct {
	dir    string
	status *ytdlp.StatusCache
	// installed remembers the binary's answer per file, so the per-page-load
	// version read does not start yt-dlp each time. See ytdlp.VersionCache.
	installed *ytdlp.VersionCache
}

func (v ytdlpVersioner) Version(ctx context.Context) (string, error) {
	return v.installed.Version(ctx, resolveYtdlpBin(v.dir))
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

func (v ytdlpVersioner) Latest(_ context.Context) (string, time.Time, string) {
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
	rec activity.Recorder,
) {
	// Track the release last reported so the Activity record fires only when a
	// release is newly discovered (the silence rule): most checks find the same
	// version, and a "new yt-dlp release" row every few hours would be noise.
	//
	// The FIRST check to complete seeds this WITHOUT recording. Nothing here is
	// persisted, so a restart re-discovers whatever is pending — and a user who
	// leaves an update unapplied for a week while restarting daily would
	// otherwise collect one identical row per boot. The rail indicator and the
	// Settings note are what carry a standing pending update; Activity logs the
	// discovery.
	//
	// Seeding hangs off the first check that actually reached GitHub, not off
	// the boot check specifically: a boot check during a GitHub outage learns
	// nothing, so the tick that finally answers is still the first sighting and
	// must seed rather than announce a release that predates the process.
	var announced string
	var checked bool

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
		// firstSighting is read before `checked` flips, so it describes this
		// check: true only while no earlier check has ever seen a release list.
		firstSighting := !checked
		checked = true

		got := status.Get()
		if boot {
			slog.Info("yt-dlp version checked",
				"version", installed, "latest", latest, "interval", interval.String())
		}
		if !got.UpdateAvailable() {
			return
		}
		slog.Info("yt-dlp update available", "version", installed, "latest", latest)
		if latest == announced {
			return
		}
		// Seed on the first sighting rather than record. See announced's
		// declaration: an update that was already pending before this process
		// started is not news, and logging it again on every restart is exactly
		// the noise the silence rule exists to keep out of the agenda.
		if !firstSighting {
			activity.Record(rec, activity.Event{
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

// resolveYtdlpBin returns the path to the yt-dlp binary: the self-update's
// install in dir (ytdlp.InstalledBin) if it exists there as an executable
// regular file, otherwise the bare "yt-dlp" name so exec falls back to
// resolving it from PATH. It is called fresh on every yt-dlp invocation (via
// RunnerConfig.BinResolver and ytdlpVersioner), so once the self-update
// installs into dir it is picked up without a restart.
//
// On the image's architectures that is dir/yt-dlp_linux/<exe>, the unpacked
// self-contained build, so a plain dir/yt-dlp an older self-update left
// behind is not considered: it is a zipapp needing a python the image no
// longer has.
//
// The path returned is the real one, through the install link: it names the
// release tree the run will use, so an update swapping the link while yt-dlp
// starts cannot hand it another release's modules.
func resolveYtdlpBin(dir string) string {
	if dir == "" {
		return "yt-dlp"
	}
	candidate, err := filepath.EvalSymlinks(ytdlp.InstalledBin(dir))
	if err != nil {
		return "yt-dlp"
	}
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

// newServer is kit's server with hub.Close run on Shutdown:
// http.Server.Shutdown does NOT cancel in-flight request contexts, it only
// waits for handlers to return, so an open SSE stream (blocked on
// r.Context().Done(), which never fires while the client stays connected)
// would hold the drain for its full timeout. Closing the hub closes every
// subscriber channel, so the stream handler returns at once.
func newServer(addr string, h http.Handler, hub *sse.Hub[httpapi.Event]) *http.Server {
	srv := httpx.NewServer(addr, h)
	srv.RegisterOnShutdown(hub.Close)
	return srv
}

// newAuth builds sign-in for cfg.AuthMode: the OIDC flow (discovery is a
// network call, made once here) or, in dev mode, the fixed identity login
// issues instead. Sessions are kit/auth's signed cookies, keyed by
// BACKEND_SESSION_SECRET; dev mode without one uses a key per process, so a
// restart signs out.
func newAuth(ctx context.Context, cfg config.Config) (*auth.OIDC, *auth.Sessions, auth.Claims, error) {
	// Secure (HTTPS-only) cookies whenever the app is reachable over https;
	// dev mode is loopback-only http, so this stays false there.
	secure := strings.HasPrefix(cfg.PublicURL, "https://")
	key := auth.RandomKey()
	if cfg.SessionSecret != "" {
		key = auth.DeriveKey(cfg.SessionSecret, "peeq session")
	}
	sessions, err := auth.NewSessions(auth.SessionConfig{Name: httpapi.SessionCookie, Key: key, TTL: cfg.SessionTTL, Secure: secure})
	if err != nil {
		return nil, nil, auth.Claims{}, err
	}
	switch cfg.AuthMode {
	case config.AuthModeOIDC:
		oidc, err := auth.NewOIDC(ctx, auth.OIDCConfig{
			Issuer:       cfg.OIDC.Issuer,
			ClientID:     cfg.OIDC.ClientID,
			ClientSecret: cfg.OIDC.ClientSecret,
			RedirectURL:  cfg.OIDC.RedirectURL,
			// Not kit's default: peeq checks no group, and asking for the
			// groups scope only makes the session cookie bigger.
			Scopes:       []string{"openid", "profile", "email"},
			CookiePrefix: "peeq_",
			Secure:       secure,
		})
		if err != nil {
			return nil, nil, auth.Claims{}, err
		}
		return oidc, sessions, auth.Claims{}, nil
	case config.AuthModeDev:
		slog.Warn("dev auth enabled; loopback only")
		if cfg.AllowAnonymousYoutube {
			// config.Load already refuses to boot with this set outside dev
			// auth, so reaching here means it's safe (loopback-only), but the
			// cookie invariant is still being relaxed and that must be loud.
			slog.Warn("anonymous YouTube access enabled; cookie invariant relaxed; dev only")
		}
		return nil, sessions, auth.Claims{
			Subject:           cfg.Dev.Subject,
			PreferredUsername: cfg.Dev.Username,
			Email:             cfg.Dev.Email,
			Name:              cfg.Dev.DisplayName,
		}, nil
	}
	// AuthMode is unset. config.Load() accepts this without error, but every
	// route peeq serves is requireAuth-gated, so a server that boots with no
	// way to ever obtain a session is just broken with extra steps. Reject at
	// boot rather than come up silently inert.
	return nil, nil, auth.Claims{}, fmt.Errorf("BACKEND_AUTH_MODE must be %q or %q (got %q)",
		config.AuthModeOIDC, config.AuthModeDev, cfg.AuthMode)
}

// newModelClients builds the embedding client and the two chat clients on the
// configured models. Endpoints and keys come from the env vars each model's
// llmwire profile names; a missing key, or a model that is unset, unknown or
// cannot do its job, is the error here, named, before anything listens.
//
// There are TWO chat clients on purpose. The first serializes every call
// through a pacing mutex sized for a background summarize queue, so sharing
// it would park a typed question behind however much of that backlog is in
// flight. The Ask one has no pacing and a much shorter cap: a person is
// waiting for it, and an answer that has not started arriving in a minute or
// so is better abandoned than waited out.
func newModelClients(cfg config.Config) (*rag.EmbedClient, *llm.Client, *llm.Client, error) {
	embedClient, err := rag.NewEmbedClient(rag.EmbedConfig{Model: cfg.EmbedModel, Logger: slog.Default()}, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	chatClient, err := llm.NewClient(llm.Config{
		Model: cfg.ChatModel, GateModel: cfg.GateModel,
		RequestInterval: cfg.SummarizeRequestDelay, Logger: slog.Default(),
		StreamIdleTimeout: cfg.ChatStreamIdleTimeout, CallTimeout: cfg.ChatCallTimeout,
	}, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	askClient, err := llm.NewClient(llm.Config{
		Model: cfg.ChatModel, GateModel: cfg.GateModel,
		RequestInterval: 0, Logger: slog.Default(),
		StreamIdleTimeout: cfg.ChatStreamIdleTimeout, CallTimeout: cfg.AskCallTimeout,
	}, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	return embedClient, chatClient, askClient, nil
}
