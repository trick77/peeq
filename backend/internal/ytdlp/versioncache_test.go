package ytdlp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// countingVersionBin writes a script that prints version and appends a line
// to runs every time it is executed.
func countingVersionBin(t *testing.T, dir, version, runs string) string {
	t.Helper()
	bin := filepath.Join(dir, "yt-dlp")
	content := "#!/bin/sh\necho run >> '" + runs + "'\necho " + version + "\n"
	if err := os.WriteFile(bin, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	return bin
}

func runCount(t *testing.T, runs string) int {
	t.Helper()
	data, err := os.ReadFile(runs)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(data), "run")
}

// The shell asks for the installed version on every page load, and answering
// it meant starting a Python interpreter each time. The binary is read once
// and again only when the file itself changes.
func TestVersionCache_runsTheBinaryOncePerFile(t *testing.T) {
	dir := t.TempDir()
	runs := filepath.Join(dir, "runs")
	bin := countingVersionBin(t, dir, "2026.01.01", runs)

	var c VersionCache
	for range 3 {
		got, err := c.Version(context.Background(), bin)
		if err != nil || got != "2026.01.01" {
			t.Fatalf("Version = %q, %v", got, err)
		}
	}
	if n := runCount(t, runs); n != 1 {
		t.Fatalf("binary ran %d times for three reads, want 1", n)
	}

	// An update replaces the file: the next read must see the new version
	// without anyone telling the cache.
	countingVersionBin(t, dir, "2026.02.02", runs)
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(bin, later, later); err != nil {
		t.Fatal(err)
	}
	got, err := c.Version(context.Background(), bin)
	if err != nil || got != "2026.02.02" {
		t.Fatalf("Version after replace = %q, %v, want 2026.02.02", got, err)
	}
	if n := runCount(t, runs); n != 2 {
		t.Fatalf("binary ran %d times, want 2 (once per file)", n)
	}
}

// A failure is never remembered: a broken binary that gets fixed in place
// must be read again, and the error must reach the caller each time.
func TestVersionCache_doesNotCacheAFailure(t *testing.T) {
	var c VersionCache
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	for range 2 {
		if _, err := c.Version(context.Background(), missing); err == nil {
			t.Fatal("expected an error for a missing binary")
		}
	}
}
