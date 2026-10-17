// Package config loads peeq's runtime configuration from environment variables.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/trick77/ship/kit/env"
)

// defaultSearchMaxDistance mirrors rag.DefaultMaxDistance, deliberately as a
// local literal rather than an import.
//
// config is the first package every binary initialises, and importing rag for
// one float would drag rag -> store (sqlite, sqlite-vec) and llm in with it.
// config_test.go asserts the two stay equal, so the coupling lives in the test
// rather than in the binary.
const defaultSearchMaxDistance = 1.25

// AuthMode selects how peeq signs users in.
type AuthMode string

// The supported authentication modes.
const (
	// AuthModeNone disables authentication entirely.
	AuthModeNone AuthMode = ""
	// AuthModeOIDC signs users in against an OIDC provider.
	AuthModeOIDC AuthMode = "oidc"
	// AuthModeDev is the local development mode.
	AuthModeDev AuthMode = "dev"
)

// Config holds all runtime settings. Secrets come from ENV only.
type Config struct {
	Addr      string // HTTP listen address
	PublicURL string // externally reachable base URL
	DBPath    string // path to the SQLite file
	MediaDir  string // root for downloaded media
	YtdlpDir  string // directory holding the yt-dlp binary
	AuthMode  AuthMode
	OIDC      OIDCConfig
	Dev       DevUserConfig

	// AI integration: model ids only, no endpoint. Each model's llmwire
	// profile carries its host, its capabilities and the key variable its
	// client reads (LLMWIRE_<PROVIDER>_API_KEY); llmwire reads the keys at
	// boot, so a missing one is a boot error there. No model has a default:
	// llm.NewClient and rag.NewEmbedClient refuse an unset, unknown or
	// unsuitable id, listing the valid choices.
	//
	// ChatModel (BACKEND_CHAT_MODEL) writes summaries, key points and Ask
	// answers. GateModel (BACKEND_GATE_MODEL) answers the short gates —
	// classification and query understanding; empty uses ChatModel.
	// EmbedModel (BACKEND_EMBED_MODEL) embeds for search; its vector width
	// must match the one vec_chunks was built at, or boot refuses.
	ChatModel  string
	GateModel  string
	EmbedModel string

	// SearchMaxDistance bounds the semantic lane: hits at or beyond this L2
	// distance are dropped rather than ranked. Vectors are unit length, so
	// L2 = sqrt(2-2*cos); see rag.DefaultMaxDistance for the calibration.
	//
	// Zero means "unset, use the default"; a NEGATIVE value disables the bound.
	SearchMaxDistance float64
	DefaultSubLang    string

	// SummarizeRequestDelay is the minimum gap between chat requests, and
	// SummarizeVideoDelay the gap between videos, so a rate-limited or slow LLM
	// endpoint gets room to breathe. Both are tunable; 0 disables the pacing.
	SummarizeRequestDelay time.Duration
	SummarizeVideoDelay   time.Duration
	// AskCallTimeout bounds one interactive answer. Much shorter than
	// ChatCallTimeout, which sizes a background summarize of a whole transcript:
	// here a person is watching an empty panel.
	AskCallTimeout time.Duration

	// SummaryChunkTokens is the coarse chunk budget for the prose summary (in
	// estimated tokens), sized so a whole transcript fits a single call for all
	// but multi-hour videos and only a marathon fans out into a few coarse
	// sections. It must fit the chat model's context window with room for the
	// prompt and answer; lower it for a smaller-window model. 0 uses the
	// summarizer's default.
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

	// MCPToken (BACKEND_MCP_TOKEN) is the bearer secret for POST /mcp, the MCP
	// server other tools search the library through. Empty leaves the route
	// unmounted. A separate secret from the extension's API token on purpose:
	// that one may only write a cookie, this one reads the library.
	MCPToken string

	// SessionSecret (BACKEND_SESSION_SECRET) signs the session cookie, which
	// carries the identity itself: there is no session table. Required with
	// OIDC, at least 32 characters. Rotating it signs everyone out, and is the
	// only way to end a session before SessionTTL. Dev mode may leave it
	// empty: a random key per process, so a restart signs out.
	SessionSecret string
	// SessionTTL (BACKEND_SESSION_TTL, default 30d) is how long a login lasts.
	// Nothing renews a session: it ends this long after sign-in.
	SessionTTL time.Duration
}

// minSessionSecret is the shortest BACKEND_SESSION_SECRET accepted: an HMAC
// key is no stronger than the secret it is derived from.
const minSessionSecret = 32

// OIDCConfig holds OpenID Connect settings.
type OIDCConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

// DevUserConfig holds the fixed local-only development identity.
type DevUserConfig struct {
	Subject     string
	Username    string
	Email       string
	DisplayName string
}

// Load reads configuration from the environment, applying defaults. Every
// fault is reported, not just the first. A variable set to the empty string
// counts as unset (kit's env.Reader).
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(lookup func(string) string) (Config, error) {
	r := env.New(lookup)
	cfg := Config{
		Addr:       r.String("BACKEND_ADDR", ":8080"),
		PublicURL:  r.String("BACKEND_PUBLIC_URL", ""),
		DBPath:     r.String("BACKEND_DB_PATH", "/data/peeq.db"),
		MediaDir:   r.String("BACKEND_MEDIA_DIR", "/data/media"),
		YtdlpDir:   r.String("BACKEND_YTDLP_DIR", "/data/bin"),
		AuthMode:   AuthMode(r.Enum("BACKEND_AUTH_MODE", "", string(AuthModeOIDC), string(AuthModeDev))),
		ChatModel:  r.String("BACKEND_CHAT_MODEL", ""),
		GateModel:  r.String("BACKEND_GATE_MODEL", ""),
		EmbedModel: r.String("BACKEND_EMBED_MODEL", ""),
		OIDC: OIDCConfig{
			Issuer:       r.String("BACKEND_OIDC_ISSUER", ""),
			ClientID:     r.String("BACKEND_OIDC_CLIENT_ID", ""),
			ClientSecret: r.String("BACKEND_OIDC_CLIENT_SECRET", ""),
			RedirectURL:  r.String("BACKEND_OIDC_REDIRECT_URL", ""),
		},
		Dev: DevUserConfig{
			Subject:     r.String("BACKEND_DEV_USER_SUBJECT", "dev-admin"),
			Username:    r.String("BACKEND_DEV_USER_USERNAME", "dev"),
			Email:       r.String("BACKEND_DEV_USER_EMAIL", "dev@example.local"),
			DisplayName: r.String("BACKEND_DEV_USER_NAME", "Dev Admin"),
		},
		SessionSecret:         r.String("BACKEND_SESSION_SECRET", ""),
		SessionTTL:            nonNegative(r, "BACKEND_SESSION_TTL", 30*24*time.Hour),
		DefaultSubLang:        r.String("BACKEND_DEFAULT_SUB_LANG", "en"),
		MCPToken:              r.String("BACKEND_MCP_TOKEN", ""),
		AllowAnonymousYoutube: r.Bool("BACKEND_ALLOW_ANONYMOUS_YOUTUBE", false),
		// NaN and Inf are refused by env.Float: NaN makes every distance
		// comparison false, which would silently drop EVERY semantic hit. A
		// NEGATIVE value disables the cutoff. Zero used to mean that, which put
		// the dangerous reading on the value a forgotten field takes — see the
		// resolution in httpapi.New.
		SearchMaxDistance:     r.Float("BACKEND_SEARCH_MAX_DISTANCE", defaultSearchMaxDistance),
		SummarizeRequestDelay: nonNegative(r, "BACKEND_SUMMARIZE_REQUEST_DELAY", 10*time.Second),
		SummarizeVideoDelay:   nonNegative(r, "BACKEND_SUMMARIZE_VIDEO_DELAY", 30*time.Second),
		AskCallTimeout:        nonNegative(r, "BACKEND_ASK_CALL_TIMEOUT", 90*time.Second),
		ChatStreamIdleTimeout: nonNegative(r, "BACKEND_CHAT_STREAM_IDLE_TIMEOUT", 90*time.Second),
		ChatCallTimeout:       nonNegative(r, "BACKEND_CHAT_CALL_TIMEOUT", 15*time.Minute),
	}
	// 0 is the summarizer's default, so only an unset variable may yield it.
	cfg.SummaryChunkTokens = r.Int("BACKEND_SUMMARIZE_SUMMARY_TOKENS", 0)
	if cfg.SummaryChunkTokens < 0 || (cfg.SummaryChunkTokens == 0 && strings.TrimSpace(lookup("BACKEND_SUMMARIZE_SUMMARY_TOKENS")) != "") {
		r.Fail("BACKEND_SUMMARIZE_SUMMARY_TOKENS must be a positive integer")
	}

	if strings.IndexFunc(cfg.MCPToken, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		// No client can present such a token (httpx.Bearer refuses whitespace,
		// a header refuses control characters): /mcp would 401 everything.
		r.Fail("BACKEND_MCP_TOKEN must not contain whitespace or control characters")
	}
	if cfg.MCPToken != "" && cfg.PublicURL == "" {
		// MCP results cite peeq links; relative ones mean nothing to the client.
		r.Fail("BACKEND_MCP_TOKEN is set but BACKEND_PUBLIC_URL is empty; MCP results link to peeq and need the public URL")
	}

	switch cfg.AuthMode {
	case AuthModeOIDC:
		for _, v := range []struct{ name, value string }{
			{"BACKEND_OIDC_ISSUER", cfg.OIDC.Issuer},
			{"BACKEND_OIDC_CLIENT_ID", cfg.OIDC.ClientID},
			{"BACKEND_OIDC_CLIENT_SECRET", cfg.OIDC.ClientSecret},
			{"BACKEND_OIDC_REDIRECT_URL", cfg.OIDC.RedirectURL},
		} {
			if v.value == "" {
				r.Fail("%s is required when BACKEND_AUTH_MODE=oidc", v.name)
			}
		}
		if cfg.SessionSecret == "" {
			r.Fail("BACKEND_SESSION_SECRET is required when BACKEND_AUTH_MODE=oidc, at least %d characters (openssl rand -base64 48)", minSessionSecret)
		}
	case AuthModeDev:
		if err := validateDevAuthLocalOnly(cfg); err != nil {
			r.Fail("%v", err)
		}
	}
	if cfg.SessionSecret != "" && len(cfg.SessionSecret) < minSessionSecret {
		r.Fail("BACKEND_SESSION_SECRET must be at least %d characters", minSessionSecret)
	}
	if cfg.SessionTTL == 0 && strings.TrimSpace(lookup("BACKEND_SESSION_TTL")) != "" {
		r.Fail("BACKEND_SESSION_TTL must be positive")
	}

	// BACKEND_ALLOW_ANONYMOUS_YOUTUBE relaxes the cookie invariant, so it must
	// only ever be reachable through dev auth (loopback-only, enforced above).
	// A stray true in a production environment (oidc or unset auth mode) is a
	// hard boot failure, never a silent behavior change.
	if cfg.AllowAnonymousYoutube && cfg.AuthMode != AuthModeDev {
		r.Fail("BACKEND_ALLOW_ANONYMOUS_YOUTUBE=true requires BACKEND_AUTH_MODE=dev")
	}

	if err := r.Err(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// nonNegative is r.Duration with a negative value a fault: every one of these
// is a delay or a bound, and kit's Duration takes "-5s" as Go does.
func nonNegative(r *env.Reader, name string, def time.Duration) time.Duration {
	d := r.Duration(name, def)
	if d < 0 {
		r.Fail("%s must not be negative", name)
		return def
	}
	return d
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
