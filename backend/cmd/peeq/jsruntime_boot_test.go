package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureLogs(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

func TestLogJSRuntime_present_logsVersion(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deno")
	body := "deno 2.9.3 (stable, release, x86_64-unknown-linux-gnu)\nv8 14.9.207.2-rusty\ntypescript 6.0.3"
	if err := os.WriteFile(p, []byte("#!/bin/sh\ncat <<'EOF'\n"+body+"\nEOF\n"), 0o755); err != nil {
		t.Fatalf("write fake deno: %v", err)
	}
	out := captureLogs(t, func() { logJSRuntime(context.Background(), p) })
	if !strings.Contains(out, "deno-2.9.3") {
		t.Fatalf("expected the version in the log, got: %s", out)
	}
	if strings.Contains(out, "level=WARN") {
		t.Fatalf("a healthy runtime must not warn, got: %s", out)
	}
}

func TestLogJSRuntime_absent_warnsButDoesNotExit(t *testing.T) {
	out := captureLogs(t, func() {
		logJSRuntime(context.Background(), filepath.Join(t.TempDir(), "nope"))
	})
	if !strings.Contains(out, "level=WARN") {
		t.Fatalf("a missing runtime must warn, got: %s", out)
	}
	if !strings.Contains(out, "deprecated") {
		t.Fatalf("the warning must say why it matters, got: %s", out)
	}
	// Reaching this line at all proves it did not exit or panic.
}
