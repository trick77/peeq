package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"time"

	"gopkg.in/yaml.v3"

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
	// This test drives os.Setenv/Clearenv directly rather than t.Setenv, since
	// it needs to fully replace the env between subcases; restore a clean env after so
	// later tests in this file aren't polluted by leftover vars.
	t.Cleanup(os.Clearenv)
	base := map[string]string{
		"BACKEND_SESSION_SECRET":          "s",
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
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AllowAnonymousYoutube {
		t.Fatal("AllowAnonymousYoutube must default to false")
	}
}

func TestLoad_defaults(t *testing.T) {
	t.Setenv("BACKEND_SESSION_SECRET", "s")
	t.Setenv("BACKEND_AUTH_MODE", "dev")
	t.Setenv("BACKEND_ADDR", "127.0.0.1:8080")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	// No endpoint or width here: the endpoints are read by llmwire from the env
	// vars each model's profile names, and the width is the embedding model's
	// profile's. No model default either: llm and rag refuse an unset model
	// with the valid choices, so a model id is never picked in code.
	if cfg.DefaultSubLang != "en" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	if cfg.ChatModel != "" || cfg.GateModel != "" || cfg.EmbedModel != "" {
		t.Fatalf("a model was defaulted: %q %q %q", cfg.ChatModel, cfg.GateModel, cfg.EmbedModel)
	}
}

func TestLoad_models(t *testing.T) {
	t.Setenv("BACKEND_SESSION_SECRET", "s")
	t.Setenv("BACKEND_AUTH_MODE", "dev")
	t.Setenv("BACKEND_ADDR", "127.0.0.1:8080")
	t.Setenv("BACKEND_CHAT_MODEL", "chat-m")
	t.Setenv("BACKEND_GATE_MODEL", "gate-m")
	t.Setenv("BACKEND_EMBED_MODEL", "embed-m")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ChatModel != "chat-m" || cfg.GateModel != "gate-m" || cfg.EmbedModel != "embed-m" {
		t.Fatalf("models = %q %q %q", cfg.ChatModel, cfg.GateModel, cfg.EmbedModel)
	}
}

func TestLoad_summarizeDelays(t *testing.T) {
	setRequired := func() {
		t.Setenv("BACKEND_SESSION_SECRET", "x")
		t.Setenv("BACKEND_AUTH_MODE", "dev")
		t.Setenv("BACKEND_ADDR", "127.0.0.1:8080")
		t.Setenv("BACKEND_PUBLIC_URL", "")
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

// composeFiles are every compose file in the repo root.
var composeFiles = []string{"compose.yaml", "compose.dev.yaml"}

// Compose reads .env for ${} interpolation only; a variable reaches the
// container only through env_file or an environment entry. Forwarding them one
// by one cost a diagnosis cycle (BACKEND_SEARCH_MAX_DISTANCE set in .env, read
// by nobody) and would make every new provider key a compose edit. So every
// service loads .env whole, optional so a stack without one still parses, and
// any BACKEND_* or LLMWIRE_* variable in it reaches peeq unlisted.
func TestComposeLoadsDotEnvIntoPeeq(t *testing.T) {
	for _, file := range composeFiles {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "..", file))
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			var doc struct {
				Services map[string]struct {
					EnvFile []struct {
						Path     string `yaml:"path"`
						Required *bool  `yaml:"required"`
					} `yaml:"env_file"`
				} `yaml:"services"`
			}
			if err := yaml.Unmarshal(raw, &doc); err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			peeq, ok := doc.Services["peeq"]
			if !ok {
				t.Fatalf("%s has no peeq service", file)
			}
			for _, ef := range peeq.EnvFile {
				if ef.Path == ".env" && ef.Required != nil && !*ef.Required {
					return
				}
			}
			t.Fatalf("%s: peeq does not load .env via env_file {path: .env, required: false}", file)
		})
	}
}

// Compose warns "variable is not set" for every bare "${VAR}" it cannot
// resolve, on every command. With .env loaded whole, the environment block
// keeps only values compose sets or defaults itself, so every interpolation
// left in it carries a default.
func TestComposeInterpolationsCarryADefault(t *testing.T) {
	bare := regexp.MustCompile(`\$\{[A-Z0-9_]+\}`)
	for _, file := range composeFiles {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, m := range bare.FindAllString(string(raw), -1) {
			t.Errorf("%s interpolates %s with no default; drop it (env_file carries .env) or write ${NAME:-default}", file, m)
		}
	}
}
