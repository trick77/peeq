package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/ytdlp"
)

// fakeYtdlpBin installs an executable in dir that reports version when run
// with --version, so the check ticker's "what is installed?" half has a real
// binary to shell out to without any network or a real yt-dlp.
func fakeYtdlpBin(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho " + version + "\n"
	if err := os.WriteFile(filepath.Join(dir, "yt-dlp"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake yt-dlp: %v", err)
	}
	return dir
}

// runCheckOnce runs the ticker into cache with an interval long enough that
// only the boot check fires, waits for that check to finish, then cancels and
// waits for the goroutine to exit — so the cache is settled and race-free to
// assert on.
//
// The wait is on the fetch actually being called rather than on cancelling
// straight away: the boot check shells out to yt-dlp with the SAME context, so
// an immediate cancel would abort the version read and record an empty
// installed version — a test artefact, not the behaviour under test.
func runCheckOnce(t *testing.T, dir string, fetch func(context.Context) (string, error)) *ytdlp.StatusCache {
	t.Helper()
	cache := ytdlp.NewStatusCache()
	runCheckOnceInto(t, dir, fetch, cache)
	return cache
}

func runCheckOnceInto(t *testing.T, dir string, fetch func(context.Context) (string, error), cache *ytdlp.StatusCache) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetched := make(chan struct{})
	var once sync.Once
	done := make(chan struct{})
	go func() {
		defer close(done)
		runYtdlpVersionCheckTicker(ctx, dir, time.Hour, func(c context.Context) (string, error) {
			defer once.Do(func() { close(fetched) })
			return fetch(c)
		}, cache, nil)
	}()

	select {
	case <-fetched:
	case <-time.After(10 * time.Second):
		t.Fatal("boot release check never ran")
	}
	// The cache write happens just after fetch returns; cancelling the ticker
	// is what guarantees it has, since the goroutine only exits past that point.
	cancel()
	<-done
}

// TestYtdlpVersionCheck_recordsBothVersions covers the boot check populating
// the cache the version endpoint reads.
func TestYtdlpVersionCheck_recordsBothVersions(t *testing.T) {
	dir := fakeYtdlpBin(t, "2026.07.01")

	cache := runCheckOnce(t, dir, func(context.Context) (string, error) {
		return "2026.08.15", nil
	})

	got := cache.Get()
	if got.Installed != "2026.07.01" || got.Latest != "2026.08.15" {
		t.Fatalf("status = %+v, want installed 2026.07.01 / latest 2026.08.15", got)
	}
	if !got.UpdateAvailable() {
		t.Fatal("an older installed version did not report an available update")
	}
	if got.CheckedAt.IsZero() {
		t.Fatal("a successful check left CheckedAt unstamped")
	}
}

// TestYtdlpVersionCheck_neverInstalls is the load-bearing guarantee of this
// ticker: it reports, and nothing else. peeq used to blind-download the latest
// release here on a timer, swapping the extractor every download depends on
// with no announcement. Installing is the Settings Update button's job now, so
// a check that finds a newer release must leave the binary on disk untouched.
func TestYtdlpVersionCheck_neverInstalls(t *testing.T) {
	dir := fakeYtdlpBin(t, "2026.07.01")
	binPath := filepath.Join(dir, "yt-dlp")
	before, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read fake binary: %v", err)
	}

	cache := runCheckOnce(t, dir, func(context.Context) (string, error) {
		return "2026.08.15", nil
	})
	if !cache.Get().UpdateAvailable() {
		t.Fatal("test is not exercising the interesting case: no update was found")
	}

	after, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("read fake binary after check: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("the version check replaced the yt-dlp binary; it must only report")
	}
}

// TestYtdlpVersionCheck_fetchFails_keepsLastKnownRelease covers a GitHub
// outage on a later tick. Blanking the release there would silently downgrade
// "an update is waiting" to "you look current" — the update indicator would
// vanish because the CHECK broke, not because anything was installed.
func TestYtdlpVersionCheck_fetchFails_keepsLastKnownRelease(t *testing.T) {
	dir := fakeYtdlpBin(t, "2026.07.01")
	cache := ytdlp.NewStatusCache()
	checked := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	cache.SetChecked("2026.07.01", "2026.08.15", checked)

	runCheckOnceInto(t, dir, func(context.Context) (string, error) {
		return "", errors.New("no such host")
	}, cache)

	got := cache.Get()
	if got.Latest != "2026.08.15" || !got.UpdateAvailable() {
		t.Fatalf("a failed check discarded the pending update: %+v", got)
	}
	if got.CheckErr == "" {
		t.Fatal("a failed check recorded no error, so the failure would be invisible")
	}
	if !got.CheckedAt.Equal(checked) {
		t.Fatalf("a failed check restamped CheckedAt to %v, want the last SUCCESSFUL check %v", got.CheckedAt, checked)
	}
}

// TestYtdlpVersionCheck_missingBinary_stillChecks covers a deployment whose
// yt-dlp is absent or unrunnable. The release check must still run and record
// its answer — the binary being broken is exactly when knowing what to install
// matters — and must not be mistaken for an available update.
func TestYtdlpVersionCheck_missingBinary_stillChecks(t *testing.T) {
	var calls atomic.Int32
	// An empty dir resolves to the bare PATH name; whether a real yt-dlp is on
	// this machine's PATH is not this test's concern, only that the check ran.
	cache := runCheckOnce(t, t.TempDir(), func(context.Context) (string, error) {
		calls.Add(1)
		return "2026.08.15", nil
	})

	if calls.Load() != 1 {
		t.Fatalf("release check ran %d times, want exactly 1", calls.Load())
	}
	if got := cache.Get(); got.Latest != "2026.08.15" {
		t.Fatalf("latest = %q, want it recorded regardless of the local binary", got.Latest)
	}
}
