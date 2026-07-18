package config

import "testing"

func TestLoad_devAuthRejectsNonLoopback(t *testing.T) {
	t.Setenv("VARK_SESSION_SECRET", "x")
	t.Setenv("VARK_AUTH_MODE", "dev")
	t.Setenv("VARK_ADDR", "0.0.0.0:8080")
	t.Setenv("VARK_PUBLIC_URL", "")
	if _, err := Load(); err == nil {
		t.Fatal("dev auth on non-loopback must fail")
	}
}

func TestLoad_devAuthLoopbackOK(t *testing.T) {
	t.Setenv("VARK_SESSION_SECRET", "x")
	t.Setenv("VARK_AUTH_MODE", "dev")
	t.Setenv("VARK_ADDR", "127.0.0.1:8080")
	t.Setenv("VARK_PUBLIC_URL", "")
	if _, err := Load(); err != nil {
		t.Fatalf("loopback dev auth must pass: %v", err)
	}
}

func TestLoad_missingSecretFails(t *testing.T) {
	t.Setenv("VARK_SESSION_SECRET", "")
	if _, err := Load(); err == nil {
		t.Fatal("missing secret must fail")
	}
}
