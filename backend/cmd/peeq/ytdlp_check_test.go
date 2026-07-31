package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/ytdlp"
)

// openTestDB opens a migrated scratch database, so the Activity assertions run
// against the real CHECK constraints rather than a double.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

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

	before := cache.Get()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runYtdlpVersionCheckTicker(ctx, dir, time.Hour, fetch, cache, nil)
	}()

	// Wait for the CACHE to change, not for fetch to return.
	//
	// This used to signal from a defer as fetch returned and then cancel
	// immediately, on the reasoning that the goroutine only exits past the
	// write. It does not: on a fetch error the ticker checks ctx.Err() first and
	// returns WITHOUT recording, because a cancelled fetch is a shutdown rather
	// than a check failure (see runYtdlpVersionCheckTicker). So the cancel could
	// land in the window between fetch returning and the write, the result went
	// missing, and TestYtdlpVersionCheck_fetchFails_keepsLastKnownRelease failed
	// on CI while passing locally every time.
	//
	// Waiting on the observable effect closes that window: by the time the cache
	// differs, the write this test is about has already happened.
	waitUntil(t, "the release check to record a result", func() bool {
		return cache.Get() != before
	})
	cancel()
	<-done
}

// waitUntil polls ok until it holds, failing the test if it never does. Tests
// here wait on EFFECTS rather than on the calls that cause them, since the
// ticker decides whether to record after its collaborator has returned.
func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
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

// runCheckTicks runs the ticker with a tiny interval so ticks (not just the
// boot check) fire, and returns once wantCalls checks have COMPLETED.
//
// Completed, not started: the ticker records its result after fetch returns and
// skips recording entirely if ctx is already cancelled, so returning as soon as
// the wantCalls-th fetch returned raced that write — the same bug as in
// runCheckOnceInto. The ticker runs its checks sequentially, so the START of
// one more call is proof the previous one finished writing. That costs one
// extra fetch, which every caller here tolerates: their fetches return a
// constant after the first call.
func runCheckTicks(t *testing.T, dir string, cache *ytdlp.StatusCache, rec *activity.Store, wantCalls int, fetch func(context.Context) (string, error)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		runYtdlpVersionCheckTicker(ctx, dir, time.Millisecond, func(c context.Context) (string, error) {
			calls.Add(1)
			return fetch(c)
		}, cache, rec)
	}()

	waitUntil(t, fmt.Sprintf("%d release checks to complete", wantCalls), func() bool {
		return int(calls.Load()) > wantCalls
	})
	cancel()
	<-done
}

// TestYtdlpVersionCheck_recordsActivityOnce is the only test that exercises the
// Activity write with a REAL store. activity.Record swallows every error at
// ERROR level by design, and the kind/outcome pair is enforced by a CHECK in
// 0007_activity.sql, so a wrong pair would fail silently and forever — a fake
// recorder would never notice.
//
// It also pins the silence rule twice over: the row lands once no matter how
// many checks find the same release, and a release already pending at boot is
// seeded rather than announced, so restarting daily with an update outstanding
// does not deposit one identical row per boot.
func TestYtdlpVersionCheck_recordsActivityOnce(t *testing.T) {
	db := openTestDB(t)
	rec := activity.New(db)
	dir := fakeYtdlpBin(t, "2026.07.01")
	cache := ytdlp.NewStatusCache()

	// Several checks, all finding the same newer release: boot seeds it, and
	// every tick after that sees nothing new.
	runCheckTicks(t, dir, cache, rec, 4, func(context.Context) (string, error) {
		return "2026.08.15", nil
	})

	page, err := rec.Recent(0, 40, "")
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("a release pending since boot logged %d rows, want 0: %+v", len(page.Events), page.Events)
	}

	// A second ticker over the same still-pending release is a RESTART. This is
	// the case that used to deposit a fresh row every boot.
	runCheckTicks(t, dir, cache, rec, 4, func(context.Context) (string, error) {
		return "2026.08.15", nil
	})
	page, err = rec.Recent(0, 40, "")
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("restarting with the same update pending logged %d rows, want 0: %+v", len(page.Events), page.Events)
	}

	// A release that appears WHILE the process runs is news. One ticker, whose
	// answer changes after the boot check — spawning a second ticker instead
	// would just be another restart, and would seed rather than announce.
	var calls atomic.Int32
	runCheckTicks(t, dir, cache, rec, 5, func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			return "2026.08.15", nil
		}
		return "2026.09.20", nil
	})

	page, err = rec.Recent(0, 40, "")
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	// One row, not zero: a zero here would mean the CHECK rejected the
	// kind/outcome pair and Record swallowed it.
	if len(page.Events) != 1 {
		t.Fatalf("newly discovered release logged %d rows, want exactly 1: %+v", len(page.Events), page.Events)
	}
	got := page.Events[0]
	if got.Kind != activity.KindYtdlp || got.Outcome != activity.OutcomeWarn {
		t.Fatalf("event = %s/%s, want %s/%s", got.Kind, got.Outcome, activity.KindYtdlp, activity.OutcomeWarn)
	}
	if !strings.Contains(got.Summary, "2026.09.20") {
		t.Fatalf("summary = %q, want it to name the new release", got.Summary)
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
