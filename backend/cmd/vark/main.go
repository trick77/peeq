// Command vark is the all-in-one server: API + embedded SPA, backed by
// SQLite. This is the Task-5 boot milestone: config, DB, and auth are wired
// end to end (dev auto-login or OIDC) in front of an empty video library; the
// actual YouTube archiving pipeline arrives in later tasks.
package main

import (
	"context"
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

	"github.com/trick77/vark/internal/auth"
	"github.com/trick77/vark/internal/config"
	"github.com/trick77/vark/internal/download"
	"github.com/trick77/vark/internal/httpapi"
	"github.com/trick77/vark/internal/jobs"
	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/sse"
	"github.com/trick77/vark/internal/store"
	"github.com/trick77/vark/internal/version"
	"github.com/trick77/vark/internal/videos"
	"github.com/trick77/vark/internal/ytdlp"
	"github.com/trick77/vark/web"
)

func main() {
	slog.Info("starting vark", "version", version.Version)
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := store.Migrate(db); err != nil {
		return err
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
		devClaims = auth.Claims{
			Subject:           cfg.Dev.Subject,
			PreferredUsername: cfg.Dev.Username,
			Email:             cfg.Dev.Email,
			Name:              cfg.Dev.DisplayName,
		}
	default:
		// AuthMode is unset. config.Load() accepts this without error (it
		// stays permissive so callers can validate at whichever layer fits),
		// but every route vark serves is requireAuth-gated, so a server that
		// boots with no way to ever obtain a session is just broken with
		// extra steps. Reject at boot rather than come up silently inert.
		return fmt.Errorf("VARK_AUTH_MODE must be %q or %q (got %q)", config.AuthModeOIDC, config.AuthModeDev, cfg.AuthMode)
	}

	authSvc := auth.NewService(oidcSvc, sessions, users)
	settingsStore := settings.New(db)
	jobsStore := jobs.New(db)
	videosStore := videos.New(db)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The throttle floor is read once at boot; the Runner clamps whatever is
	// configured up to its own hard 20s minimum regardless.
	initialSettings, err := settingsStore.Get(ctx)
	if err != nil {
		return err
	}
	runner := ytdlp.New(ytdlp.RunnerConfig{
		Bin: resolveYtdlpBin(cfg.YtdlpDir),
		CookieProvider: func() (string, string) {
			// Read fresh on every call (not just at boot) so a cookie pasted
			// or invalidated while the worker is running takes effect on the
			// very next yt-dlp invocation.
			return settingsStore.CookieCredentials(context.Background())
		},
		ThrottleFloor: time.Duration(initialSettings.ThrottleBaseSeconds) * time.Second,
		MediaDir:      cfg.MediaDir,
	})

	sseHub := sse.NewHub()
	worker := download.New(download.Deps{
		Jobs:     jobsStore,
		Videos:   videosStore,
		Settings: settingsStore,
		Runner:   runner,
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
	// Bound the worker goroutine's lifetime to the process: wg.Wait() below
	// (after serve returns, i.e. after ctx is cancelled) blocks until Run has
	// actually observed ctx.Done() and returned, rather than exiting the
	// process out from under it. Run's own loop already exits promptly on
	// ctx.Done(), so this wait is short.
	var workerWG sync.WaitGroup
	workerWG.Add(1)
	go func() {
		defer workerWG.Done()
		worker.Run(ctx)
	}()

	deps := httpapi.Deps{
		Version:        version.Version,
		Static:         web.Handler(),
		AuthService:    authSvc,
		AuthMiddleware: authMW,
		Settings:       settingsStore,
		DevAuthClaims:  devClaims,
		Jobs:           jobsStore,
		Videos:         videosStore,
		MediaDir:       cfg.MediaDir,
		Runner:         runner,
		Worker:         worker,
		SSEHub:         sseHub,
	}
	handler := httpapi.New(deps)

	srv := &http.Server{Addr: cfg.Addr, Handler: handler}

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

// resolveYtdlpBin returns the path to the yt-dlp binary: <dir>/yt-dlp if it
// exists there, otherwise the bare "yt-dlp" name so exec falls back to
// resolving it from PATH.
func resolveYtdlpBin(dir string) string {
	if dir == "" {
		return "yt-dlp"
	}
	candidate := filepath.Join(dir, "yt-dlp")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return "yt-dlp"
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
