// Command vark is the all-in-one server: API + embedded SPA, backed by
// SQLite. This is the Task-5 boot milestone: config, DB, and auth are wired
// end to end (dev auto-login or OIDC) in front of an empty video library; the
// actual YouTube archiving pipeline arrives in later tasks.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/trick77/vark/internal/auth"
	"github.com/trick77/vark/internal/config"
	"github.com/trick77/vark/internal/httpapi"
	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/store"
	"github.com/trick77/vark/internal/version"
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

	deps := httpapi.Deps{
		Version:        version.Version,
		Static:         web.Handler(),
		AuthService:    authSvc,
		AuthMiddleware: authMW,
		Settings:       settingsStore,
		DevAuthClaims:  devClaims,
	}
	handler := httpapi.New(deps)

	srv := &http.Server{Addr: cfg.Addr, Handler: handler}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return serve(ctx, srv)
}

// serve starts srv and blocks until either the server fails to start/serve
// (in which case that error is returned immediately, without waiting for
// ctx) or ctx is cancelled (in which case srv is shut down gracefully).
func serve(ctx context.Context, srv *http.Server) error {
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
