// Package config loads peeq's runtime configuration from environment variables.
package config

import (
	"fmt"
	"math"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/trick77/peeq/internal/rag"
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

	// AI integration: chat + embeddings endpoints (required at boot).
	ChatBaseURL  string
	ChatAPIKey   string
	EmbedBaseURL string
	EmbedAPIKey  string
	EmbedModel   string
	EmbedDim     int
	// SearchMaxDistance bounds the semantic lane: hits at or beyond this L2
	// distance are dropped rather than ranked. Vectors are unit length, so
	// L2 = sqrt(2-2*cos); see rag.DefaultMaxDistance for the calibration.
	SearchMaxDistance float64
	DefaultSubLang    string

	// SummarizeRequestDelay is the minimum gap between chat requests, and
	// SummarizeVideoDelay the gap between videos, so a rate-limited or slow LLM
	// endpoint gets room to breathe. Both are tunable; 0 disables the pacing.
	SummarizeRequestDelay time.Duration
	SummarizeVideoDelay   time.Duration

	// SummaryChunkTokens is the coarse chunk budget for the prose summary. The
	// chat model has a ~1M-token context window, so a whole transcript fits in a
	// single call for all but multi-hour videos; this sizes that budget (in
	// estimated tokens) so the common case is one call and only a marathon fans
	// out into a few coarse sections. 0 uses the summarizer's default.
	SummaryChunkTokens int

	// ChatStreamIdleTimeout is how long a started chat stream may go completely
	// silent before the call is abandoned and retried. Tunable because it is the
	// one bound whose right value depends on the endpoint: it must sit above the
	// longest gap that endpoint leaves between events while thinking, and below
	// the point where waiting is pointless. The header bound is fixed in
	// internal/llm; the whole-call cap is ChatCallTimeout below.
	ChatStreamIdleTimeout time.Duration

	// ChatCallTimeout is the backstop on a single chat call's total duration.
	// Tunable because single-pass summaries send a whole transcript in one call,
	// so the ceiling that used to bound ~600-token map calls now has to cover a
	// much larger request. 0 uses internal/llm's default (15m).
	ChatCallTimeout time.Duration

	// AllowAnonymousYoutube is a dev-only escape hatch: when true, the yt-dlp
	// Runner is permitted to run WITHOUT a cookie (see internal/ytdlp
	// cookieGate). It exists because authenticated yt-dlp requests currently
	// get served no usable formats by YouTube while anonymous ones work, so
	// local development needs a way around the normally-absolute
	// no-call-without-a-cookie invariant. Gated hard below: it may only be
	// true when BACKEND_AUTH_MODE=dev, which is itself loopback-only
	// (validateDevAuthLocalOnly), so this can never be reachable from the
	// network.
	AllowAnonymousYoutube bool
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

// envDuration reads a Go duration (e.g. "2s", "500ms") from key, returning def
// when unset/empty and an error on an unparseable or negative value.
func envDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration like 2s or 500ms: %w", key, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s must not be negative", key)
	}
	return d, nil
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

	cfg.ChatBaseURL = env("BACKEND_CHAT_BASE_URL", "")
	cfg.ChatAPIKey = env("BACKEND_CHAT_API_KEY", "")
	cfg.EmbedBaseURL = env("BACKEND_EMBED_BASE_URL", "")
	cfg.EmbedAPIKey = env("BACKEND_EMBED_API_KEY", "")
	cfg.EmbedModel = env("BACKEND_EMBED_MODEL", "")
	cfg.DefaultSubLang = env("BACKEND_DEFAULT_SUB_LANG", "en")
	cfg.EmbedDim = 1536
	cfg.SearchMaxDistance = rag.DefaultMaxDistance
	if v := env("BACKEND_ALLOW_ANONYMOUS_YOUTUBE", ""); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("BACKEND_ALLOW_ANONYMOUS_YOUTUBE must be a boolean")
		}
		cfg.AllowAnonymousYoutube = b
	}
	if v := env("BACKEND_SEARCH_MAX_DISTANCE", ""); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		// NaN and Inf parse cleanly and survive an `f < 0` test, so they have to
		// be rejected by name: NaN makes every distance comparison false, which
		// would silently drop EVERY semantic hit with no error to explain it.
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
			return Config{}, fmt.Errorf("BACKEND_SEARCH_MAX_DISTANCE must be a finite non-negative number")
		}
		// 0 disables the cutoff, restoring the pre-cutoff behaviour of always
		// returning the k nearest chunks however far away they are.
		cfg.SearchMaxDistance = f
	}
	if v := env("BACKEND_EMBED_DIM", ""); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("BACKEND_EMBED_DIM must be a positive integer")
		}
		cfg.EmbedDim = n
	}

	reqDelay, err := envDuration("BACKEND_SUMMARIZE_REQUEST_DELAY", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.SummarizeRequestDelay = reqDelay
	vidDelay, err := envDuration("BACKEND_SUMMARIZE_VIDEO_DELAY", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.SummarizeVideoDelay = vidDelay
	idle, err := envDuration("BACKEND_CHAT_STREAM_IDLE_TIMEOUT", 90*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.ChatStreamIdleTimeout = idle
	callTimeout, err := envDuration("BACKEND_CHAT_CALL_TIMEOUT", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}
	cfg.ChatCallTimeout = callTimeout

	if v := os.Getenv("BACKEND_SUMMARIZE_SUMMARY_TOKENS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("BACKEND_SUMMARIZE_SUMMARY_TOKENS must be a positive integer")
		}
		cfg.SummaryChunkTokens = n
	}

	if cfg.SessionSecret == "" {
		return Config{}, fmt.Errorf("BACKEND_SESSION_SECRET is required")
	}

	if cfg.ChatBaseURL == "" {
		return Config{}, fmt.Errorf("BACKEND_CHAT_BASE_URL is required")
	}
	if cfg.EmbedBaseURL == "" {
		return Config{}, fmt.Errorf("BACKEND_EMBED_BASE_URL is required")
	}
	if cfg.EmbedModel == "" {
		return Config{}, fmt.Errorf("BACKEND_EMBED_MODEL is required")
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

	// BACKEND_ALLOW_ANONYMOUS_YOUTUBE relaxes the cookie invariant, so it must
	// only ever be reachable through dev auth (loopback-only, enforced above).
	// A stray true in a production environment (oidc or unset auth mode) is a
	// hard boot failure, never a silent behavior change.
	if cfg.AllowAnonymousYoutube && cfg.AuthMode != AuthModeDev {
		return Config{}, fmt.Errorf("BACKEND_ALLOW_ANONYMOUS_YOUTUBE=true requires BACKEND_AUTH_MODE=dev")
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
