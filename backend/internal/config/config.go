// Package config loads peeq's runtime configuration from environment variables.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
)

// AuthMode selects how peeq signs users in.
type AuthMode string

const (
	AuthModeNone AuthMode = ""
	AuthModeOIDC AuthMode = "oidc"
	AuthModeDev  AuthMode = "dev"
)

// Config holds all runtime settings. Secrets come from ENV only.
type Config struct {
	Addr          string // HTTP listen address
	PublicURL     string // externally reachable base URL
	SessionSecret string
	DBPath        string // path to the SQLite file
	MediaDir      string // root for downloaded media
	YtdlpDir      string // directory holding the yt-dlp binary
	AuthMode      AuthMode
	OIDC          OIDCConfig
	Dev           DevUserConfig
	LogLevel      string
}

// OIDCConfig holds OpenID Connect settings.
type OIDCConfig struct {
	Issuer                string
	ClientID              string
	ClientSecret          string
	RedirectURL           string
	PostLogoutRedirectURL string
	AdminGroup            string
}

// DevUserConfig holds the fixed local-only development identity.
type DevUserConfig struct {
	Subject     string
	Username    string
	Email       string
	DisplayName string
	Role        string
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// Load reads configuration from the environment, applying defaults.
func Load() (Config, error) {
	cfg := Config{
		Addr:          env("BACKEND_ADDR", ":8080"),
		PublicURL:     env("BACKEND_PUBLIC_URL", ""),
		SessionSecret: env("BACKEND_SESSION_SECRET", ""),
		DBPath:        env("BACKEND_DB_PATH", "/data/peeq.db"),
		MediaDir:      env("BACKEND_MEDIA_DIR", "/data/media"),
		YtdlpDir:      env("BACKEND_YTDLP_DIR", "/data/bin"),
		AuthMode:      AuthMode(env("BACKEND_AUTH_MODE", "")),
		LogLevel:      env("BACKEND_LOG_LEVEL", "info"),
		OIDC: OIDCConfig{
			Issuer:                env("BACKEND_OIDC_ISSUER", ""),
			ClientID:              env("BACKEND_OIDC_CLIENT_ID", ""),
			ClientSecret:          env("BACKEND_OIDC_CLIENT_SECRET", ""),
			RedirectURL:           env("BACKEND_OIDC_REDIRECT_URL", ""),
			PostLogoutRedirectURL: env("BACKEND_OIDC_POST_LOGOUT_REDIRECT_URL", ""),
			AdminGroup:            env("BACKEND_OIDC_ADMIN_GROUP", ""),
		},
		Dev: DevUserConfig{
			Subject:     env("BACKEND_DEV_USER_SUBJECT", "dev-admin"),
			Username:    env("BACKEND_DEV_USER_USERNAME", "dev"),
			Email:       env("BACKEND_DEV_USER_EMAIL", "dev@example.local"),
			DisplayName: env("BACKEND_DEV_USER_NAME", "Dev Admin"),
			Role:        "admin",
		},
	}

	if cfg.SessionSecret == "" {
		return Config{}, fmt.Errorf("BACKEND_SESSION_SECRET is required")
	}

	switch cfg.AuthMode {
	case AuthModeNone:
	case AuthModeOIDC:
		if cfg.OIDC.Issuer == "" {
			return Config{}, fmt.Errorf("BACKEND_OIDC_ISSUER is required when BACKEND_AUTH_MODE=oidc")
		}
		if cfg.OIDC.ClientID == "" {
			return Config{}, fmt.Errorf("BACKEND_OIDC_CLIENT_ID is required when BACKEND_AUTH_MODE=oidc")
		}
		if cfg.OIDC.ClientSecret == "" {
			return Config{}, fmt.Errorf("BACKEND_OIDC_CLIENT_SECRET is required when BACKEND_AUTH_MODE=oidc")
		}
		if cfg.OIDC.RedirectURL == "" {
			return Config{}, fmt.Errorf("BACKEND_OIDC_REDIRECT_URL is required when BACKEND_AUTH_MODE=oidc")
		}
	case AuthModeDev:
		if err := validateDevAuthLocalOnly(cfg); err != nil {
			return Config{}, err
		}
	default:
		return Config{}, fmt.Errorf("BACKEND_AUTH_MODE must be one of: oidc, dev")
	}

	return cfg, nil
}

// validateDevAuthLocalOnly ensures dev auth can only be enabled when the
// server is bound to a loopback address, preventing the unauthenticated
// dev-login shortcut from ever being reachable from the network.
func validateDevAuthLocalOnly(cfg Config) error {
	if !isLoopbackAddr(cfg.Addr) {
		return fmt.Errorf("BACKEND_AUTH_MODE=dev requires BACKEND_ADDR to bind to localhost or a loopback address")
	}
	if cfg.PublicURL != "" && !isLoopbackPublicURL(cfg.PublicURL) {
		return fmt.Errorf("BACKEND_AUTH_MODE=dev requires BACKEND_PUBLIC_URL to be empty or loopback")
	}
	return nil
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return isLoopbackHost(host)
}

func isLoopbackPublicURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return isLoopbackHost(parsed.Hostname())
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
