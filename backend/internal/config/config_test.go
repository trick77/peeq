package config

import (
	"os"
	"strings"
	"testing"
	"time"
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
	if cfg.SummarizeRequestDelay != 2*time.Second || cfg.SummarizeVideoDelay != 5*time.Second {
		t.Errorf("defaults = %v/%v, want 2s/5s", cfg.SummarizeRequestDelay, cfg.SummarizeVideoDelay)
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
