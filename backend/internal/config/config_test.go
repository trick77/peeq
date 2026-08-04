package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/rag"
)

func TestLoad_devAuthRejectsNonLoopback(t *testing.T) {
	t.Setenv("BACKEND_SESSION_SECRET", "x")
	t.Setenv("BACKEND_AUTH_MODE", "dev")
	t.Setenv("BACKEND_ADDR", "0.0.0.0:8080")
	t.Setenv("BACKEND_PUBLIC_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("dev auth on non-loopback must fail")
	}
}

func TestLoad_devAuthLoopbackOK(t *testing.T) {
	t.Setenv("BACKEND_SESSION_SECRET", "x")
	t.Setenv("BACKEND_AUTH_MODE", "dev")
	t.Setenv("BACKEND_ADDR", "127.0.0.1:8080")
	t.Setenv("BACKEND_PUBLIC_URL", "")
	t.Setenv("BACKEND_CHAT_BASE_URL", "http://chat")
	t.Setenv("BACKEND_EMBED_BASE_URL", "http://emb")
	t.Setenv("BACKEND_EMBED_MODEL", "e5")
	if _, err := Load(); err != nil {
		t.Fatalf("loopback dev auth must pass: %v", err)
	}
}

func TestLoad_missingSecretFails(t *testing.T) {
	t.Setenv("BACKEND_SESSION_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("missing secret must fail")
	}
}

func TestLoad_allowAnonymousYoutube_requiresDevAuth(t *testing.T) {
	// This test drives os.Setenv/Clearenv directly (like
	// TestLoadRequiresAIEndpoints below) rather than t.Setenv, since it needs
	// to fully replace the env between subcases; restore a clean env after so
	// later tests in this file aren't polluted by leftover vars.
	t.Cleanup(os.Clearenv)
	base := map[string]string{
		"BACKEND_SESSION_SECRET":          "s",
		"BACKEND_CHAT_BASE_URL":           "http://chat",
		"BACKEND_EMBED_BASE_URL":          "http://emb",
		"BACKEND_EMBED_MODEL":             "e5",
		"BACKEND_ALLOW_ANONYMOUS_YOUTUBE": "true",
	}
	setEnv := func(m map[string]string) {
		os.Clearenv()
		for k, v := range m {
			os.Setenv(k, v)
		}
	}

	// true + AUTH_MODE=dev (loopback) → OK.
	devOK := map[string]string{}
	for k, v := range base {
		devOK[k] = v
	}
	devOK["BACKEND_AUTH_MODE"] = "dev"
	devOK["BACKEND_ADDR"] = "127.0.0.1:8080"
	setEnv(devOK)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("anonymous youtube + dev auth should boot, got err: %v", err)
	}
	if !cfg.AllowAnonymousYoutube {
		t.Fatal("AllowAnonymousYoutube should be true")
	}

	// true + AUTH_MODE=oidc (fully configured) → hard startup error naming
	// the anon-guard, not an incidental OIDC-field error.
	oidcBad := map[string]string{}
	for k, v := range base {
		oidcBad[k] = v
	}
	oidcBad["BACKEND_AUTH_MODE"] = "oidc"
	oidcBad["BACKEND_ADDR"] = ":8080"
	oidcBad["BACKEND_OIDC_ISSUER"] = "https://issuer.example"
	oidcBad["BACKEND_OIDC_CLIENT_ID"] = "client"
	oidcBad["BACKEND_OIDC_CLIENT_SECRET"] = "secret"
	oidcBad["BACKEND_OIDC_REDIRECT_URL"] = "https://issuer.example/callback"
	setEnv(oidcBad)
	_, err = Load()
	if err == nil {
		t.Fatal("BACKEND_ALLOW_ANONYMOUS_YOUTUBE=true with BACKEND_AUTH_MODE=oidc must fail to start")
	}
	if !strings.Contains(err.Error(), "BACKEND_ALLOW_ANONYMOUS_YOUTUBE") {
		t.Fatalf("error should name the anon-youtube guard, got: %v", err)
	}
}

func TestLoad_allowAnonymousYoutube_defaultFalse(t *testing.T) {
	t.Setenv("BACKEND_SESSION_SECRET", "x")
	t.Setenv("BACKEND_AUTH_MODE", "dev")
	t.Setenv("BACKEND_ADDR", "127.0.0.1:8080")
	t.Setenv("BACKEND_CHAT_BASE_URL", "http://chat")
	t.Setenv("BACKEND_EMBED_BASE_URL", "http://emb")
	t.Setenv("BACKEND_EMBED_MODEL", "e5")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AllowAnonymousYoutube {
		t.Fatal("AllowAnonymousYoutube must default to false")
	}
}

func TestLoadRequiresAIEndpoints(t *testing.T) {
	base := map[string]string{
		"BACKEND_SESSION_SECRET": "s", "BACKEND_AUTH_MODE": "dev", "BACKEND_ADDR": "127.0.0.1:8080",
		"BACKEND_CHAT_BASE_URL": "http://chat", "BACKEND_EMBED_BASE_URL": "http://emb", "BACKEND_EMBED_MODEL": "e5",
	}
	setEnv := func(m map[string]string) {
		os.Clearenv()
		for k, v := range m {
			os.Setenv(k, v)
		}
	}
	setEnv(base)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if cfg.ChatBaseURL != "http://chat" || cfg.EmbedModel != "e5" || cfg.EmbedDim != 1536 || cfg.DefaultSubLang != "en" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	for _, drop := range []string{"BACKEND_CHAT_BASE_URL", "BACKEND_EMBED_BASE_URL", "BACKEND_EMBED_MODEL"} {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		delete(m, drop)
		setEnv(m)
		if _, err := Load(); err == nil {
			t.Fatalf("expected error when %s missing", drop)
		}
	}
}

func TestLoad_summarizeDelays(t *testing.T) {
	setRequired := func() {
		t.Setenv("BACKEND_SESSION_SECRET", "x")
		t.Setenv("BACKEND_AUTH_MODE", "dev")
		t.Setenv("BACKEND_ADDR", "127.0.0.1:8080")
		t.Setenv("BACKEND_PUBLIC_URL", "")
		t.Setenv("BACKEND_CHAT_BASE_URL", "http://chat")
		t.Setenv("BACKEND_EMBED_BASE_URL", "http://emb")
		t.Setenv("BACKEND_EMBED_MODEL", "e5")
	}

	setRequired()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load (defaults): %v", err)
	}
	if cfg.SummarizeRequestDelay != 10*time.Second || cfg.SummarizeVideoDelay != 30*time.Second {
		t.Errorf("defaults = %v/%v, want 10s/30s", cfg.SummarizeRequestDelay, cfg.SummarizeVideoDelay)
	}

	setRequired()
	t.Setenv("BACKEND_SUMMARIZE_REQUEST_DELAY", "250ms")
	t.Setenv("BACKEND_SUMMARIZE_VIDEO_DELAY", "0s")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load (custom): %v", err)
	}
	if cfg.SummarizeRequestDelay != 250*time.Millisecond || cfg.SummarizeVideoDelay != 0 {
		t.Errorf("custom = %v/%v, want 250ms/0", cfg.SummarizeRequestDelay, cfg.SummarizeVideoDelay)
	}

	setRequired()
	t.Setenv("BACKEND_SUMMARIZE_REQUEST_DELAY", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Error("want an error for an unparseable duration")
	}

	setRequired()
	t.Setenv("BACKEND_SUMMARIZE_REQUEST_DELAY", "-1s")
	if _, err := Load(); err == nil {
		t.Error("want an error for a negative duration")
	}

	setRequired()
	t.Setenv("BACKEND_SUMMARIZE_REQUEST_DELAY", "1s") // valid, so the video-delay parse is reached
	t.Setenv("BACKEND_SUMMARIZE_VIDEO_DELAY", "bogus")
	if _, err := Load(); err == nil {
		t.Error("want an error for an unparseable video delay")
	}
}

func TestLoad_chatStreamIdleTimeout(t *testing.T) {
	setRequired := func() {
		t.Setenv("BACKEND_SESSION_SECRET", "x")
		t.Setenv("BACKEND_AUTH_MODE", "dev")
		t.Setenv("BACKEND_ADDR", "127.0.0.1:8080")
		t.Setenv("BACKEND_PUBLIC_URL", "")
		t.Setenv("BACKEND_CHAT_BASE_URL", "http://chat")
		t.Setenv("BACKEND_EMBED_BASE_URL", "http://emb")
		t.Setenv("BACKEND_EMBED_MODEL", "e5")
	}

	setRequired()
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load (default): %v", err)
	}
	if cfg.ChatStreamIdleTimeout != 90*time.Second {
		t.Errorf("default = %v, want 90s", cfg.ChatStreamIdleTimeout)
	}

	setRequired()
	t.Setenv("BACKEND_CHAT_STREAM_IDLE_TIMEOUT", "45s")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load (custom): %v", err)
	}
	if cfg.ChatStreamIdleTimeout != 45*time.Second {
		t.Errorf("custom = %v, want 45s", cfg.ChatStreamIdleTimeout)
	}

	// A typo here would otherwise mean the bound silently reverts to its
	// default, which is the one knob an operator reaches for when the endpoint
	// pauses longer than we assumed.
	setRequired()
	t.Setenv("BACKEND_CHAT_STREAM_IDLE_TIMEOUT", "ninety")
	if _, err := Load(); err == nil {
		t.Error("want an error for an unparseable idle timeout")
	}
}

// baseEnv sets the minimal environment for a successful Load, so a test can add
// only the variable it is exercising.
func baseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("BACKEND_SESSION_SECRET", "x")
	t.Setenv("BACKEND_AUTH_MODE", "dev")
	t.Setenv("BACKEND_ADDR", "127.0.0.1:8080")
	t.Setenv("BACKEND_PUBLIC_URL", "")
	t.Setenv("BACKEND_CHAT_BASE_URL", "http://chat")
	t.Setenv("BACKEND_EMBED_BASE_URL", "http://emb")
	t.Setenv("BACKEND_EMBED_MODEL", "e5")
}

func TestLoad_summaryTokensAndCallTimeout(t *testing.T) {
	baseEnv(t)
	t.Setenv("BACKEND_SUMMARIZE_SUMMARY_TOKENS", "12000")
	t.Setenv("BACKEND_CHAT_CALL_TIMEOUT", "20m")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SummaryChunkTokens != 12000 {
		t.Errorf("SummaryChunkTokens = %d, want 12000", cfg.SummaryChunkTokens)
	}
	if cfg.ChatCallTimeout != 20*time.Minute {
		t.Errorf("ChatCallTimeout = %v, want 20m", cfg.ChatCallTimeout)
	}
}

// Unset, the summary-token budget stays 0 (the summarizer applies its own
// default) and the call timeout falls to its 15m default.
func TestLoad_summaryTokensAndCallTimeoutDefaults(t *testing.T) {
	baseEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SummaryChunkTokens != 0 {
		t.Errorf("SummaryChunkTokens = %d, want 0 when unset", cfg.SummaryChunkTokens)
	}
	if cfg.ChatCallTimeout != 15*time.Minute {
		t.Errorf("ChatCallTimeout = %v, want the 15m default", cfg.ChatCallTimeout)
	}
}

func TestLoad_invalidSummaryTokensFails(t *testing.T) {
	baseEnv(t)
	t.Setenv("BACKEND_SUMMARIZE_SUMMARY_TOKENS", "notanumber")
	if _, err := Load(); err == nil {
		t.Fatal("want an error for a non-integer summary-token budget")
	}
}

func TestLoad_invalidCallTimeoutFails(t *testing.T) {
	baseEnv(t)
	t.Setenv("BACKEND_CHAT_CALL_TIMEOUT", "notaduration")
	if _, err := Load(); err == nil {
		t.Fatal("want an error for an unparseable call timeout")
	}
}

// TestLoad_searchMaxDistance covers the parse and the values that would
// otherwise pass silently. NaN and Inf are the dangerous ones: they satisfy
// ParseFloat and survive an `f < 0` test, and a NaN bound makes every distance
// comparison false, so the semantic lane would return nothing forever with no
// error anywhere to explain it.
func TestLoad_searchMaxDistance(t *testing.T) {
	baseEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SearchMaxDistance != rag.DefaultMaxDistance {
		t.Errorf("default = %v, want %v", cfg.SearchMaxDistance, rag.DefaultMaxDistance)
	}

	// A NEGATIVE value disables the cutoff. Zero no longer does: it is what an
	// unset field holds, and that reading must not be the one that silently
	// restores "a KNN query can never fail" (see httpapi.New).
	t.Setenv("BACKEND_SEARCH_MAX_DISTANCE", "-1")
	if cfg, err := Load(); err != nil || cfg.SearchMaxDistance >= 0 {
		t.Errorf("-1 must disable the cutoff, got %v (%v)", cfg.SearchMaxDistance, err)
	}

	// The local default must not drift from the constant it mirrors. This test
	// is the ONLY thing importing rag from config's module graph — the binary
	// itself no longer does.
	if defaultSearchMaxDistance != rag.DefaultMaxDistance {
		t.Errorf("config default %v has drifted from rag.DefaultMaxDistance %v",
			defaultSearchMaxDistance, rag.DefaultMaxDistance)
	}

	for _, bad := range []string{"notanumber", "NaN", "Inf", "-Inf"} {
		t.Setenv("BACKEND_SEARCH_MAX_DISTANCE", bad)
		if _, err := Load(); err == nil {
			t.Errorf("want an error for BACKEND_SEARCH_MAX_DISTANCE=%q", bad)
		}
	}
}

// Compose forwards environment variables one by one, and reads .env only for
// ${} interpolation — so a setting this package honours but compose.yaml never
// names is unreachable in a deployed stack, silently. That cost a diagnosis
// cycle: BACKEND_SEARCH_MAX_DISTANCE was set in .env, read by nobody, and the
// backend kept its compiled default while the logs looked identical.
//
// Rather than duplicate the list, this reads both files: every BACKEND_ name
// mentioned in config.go has to appear in compose.yaml, except the dev-auth
// ones, which belong to `go run` and not to a container.
func TestComposeForwardsEverySettingConfigReads(t *testing.T) {
	devOnly := map[string]bool{
		"BACKEND_DEV_USER_SUBJECT":  true,
		"BACKEND_DEV_USER_USERNAME": true,
		"BACKEND_DEV_USER_EMAIL":    true,
		"BACKEND_DEV_USER_NAME":     true,
	}

	source, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	compose, err := os.ReadFile(filepath.Join("..", "..", "..", "compose.yaml"))
	if err != nil {
		t.Fatalf("read compose.yaml: %v", err)
	}

	name := regexp.MustCompile(`BACKEND_[A-Z0-9_]+`)
	forwarded := map[string]bool{}
	for _, m := range name.FindAllString(string(compose), -1) {
		forwarded[m] = true
	}

	missing := map[string]bool{}
	for _, m := range name.FindAllString(string(source), -1) {
		if !devOnly[m] && !forwarded[m] {
			missing[m] = true
		}
	}
	for m := range missing {
		t.Errorf("%s is read by config but compose.yaml never forwards it, so setting "+
			"it in .env does nothing — add it to the environment block", m)
	}
}
