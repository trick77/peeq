package config

import (
	"os"
	"testing"
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
	t.Setenv("BACKEND_MIMO_BASE_URL", "http://mimo")
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

func TestLoadRequiresAIEndpoints(t *testing.T) {
	base := map[string]string{
		"BACKEND_SESSION_SECRET": "s", "BACKEND_AUTH_MODE": "dev", "BACKEND_ADDR": "127.0.0.1:8080",
		"BACKEND_MIMO_BASE_URL": "http://mimo", "BACKEND_EMBED_BASE_URL": "http://emb", "BACKEND_EMBED_MODEL": "e5",
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
	if cfg.MimoBaseURL != "http://mimo" || cfg.EmbedModel != "e5" || cfg.EmbedDim != 1536 || cfg.DefaultSubLang != "en" {
		t.Fatalf("defaults wrong: %+v", cfg)
	}
	for _, drop := range []string{"BACKEND_MIMO_BASE_URL", "BACKEND_EMBED_BASE_URL", "BACKEND_EMBED_MODEL"} {
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
