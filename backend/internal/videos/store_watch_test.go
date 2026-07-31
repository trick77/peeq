package videos

import (
	"errors"
	"testing"
)

// Tests for store_watch.go: resume position, the watched toggle and its
// state_version concurrency rules, favorites, and the retention sweep.

func TestSetResume_autoMarksWatchedAtNinetyPercent_noResetOnRewatch(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// 95/100 = 95% >= 90% threshold: auto-marks watched.
	if _, _, err := s.SetResume("v", 95, nil); err != nil {
		t.Fatalf("set resume: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Watched {
		t.Fatalf("watched = false, want true after resume >= 90%%")
	}
	if got.WatchedAt == "" {
		t.Fatalf("watched_at not set")
	}
	if got.ResumePositionSeconds != 95 {
		t.Fatalf("resume_position_seconds = %v, want 95", got.ResumePositionSeconds)
	}
	firstWatchedAt := got.WatchedAt

	// Re-watching (another SetResume above threshold) must NOT reset
	// watched_at — no "life extension".
	if _, _, err := s.SetResume("v", 98, nil); err != nil {
		t.Fatalf("set resume again: %v", err)
	}
	got, err = s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.WatchedAt != firstWatchedAt {
		t.Fatalf("watched_at changed on re-watch: got %q, want unchanged %q", got.WatchedAt, firstWatchedAt)
	}
	if got.ResumePositionSeconds != 98 {
		t.Fatalf("resume_position_seconds = %v, want 98", got.ResumePositionSeconds)
	}

	// Manual un-watch clears both watched and watched_at (rescues from the
	// auto-delete sweep), and ALSO resets resume_position_seconds to 0 so
	// the rescue is sticky: a subsequent player resume ping can't
	// immediately re-cross the 90% threshold and undo the un-watch.
	if _, err := s.SetWatched("v", false); err != nil {
		t.Fatalf("set watched false: %v", err)
	}
	got, err = s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Watched {
		t.Fatalf("watched = true, want false after manual un-watch")
	}
	if got.WatchedAt != "" {
		t.Fatalf("watched_at = %q, want cleared after manual un-watch", got.WatchedAt)
	}
	if got.ResumePositionSeconds != 0 {
		t.Fatalf("resume_position_seconds = %v, want reset to 0 after manual un-watch", got.ResumePositionSeconds)
	}
}

// TestStateVersion_watchedTogglesBump covers the counter migration 0010 added:
// either direction of the manual toggle is a watched-state transition, so both
// must invalidate whatever version other clients are holding.
func TestStateVersion_watchedTogglesBump(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// DEFAULT 1, so a never-touched row already has an echoable version.
	if got.StateVersion != 1 {
		t.Fatalf("state_version = %d, want 1 on a fresh row", got.StateVersion)
	}

	for _, watched := range []bool{true, false} {
		before := got.StateVersion
		returned, err := s.SetWatched("v", watched)
		if err != nil {
			t.Fatalf("set watched %v: %v", watched, err)
		}
		if returned != before+1 {
			t.Fatalf("SetWatched(%v) returned %d, want %d", watched, returned, before+1)
		}
		got, err = s.Get("v")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		// The returned value has to be the stored one, not a guess: it is what
		// the toggling client will echo on its very next resume ping.
		if got.StateVersion != returned {
			t.Fatalf("stored state_version = %d, want the returned %d", got.StateVersion, returned)
		}
	}
}

// TestSetResume_staleVersionRefusesWrite is the issue #97 regression test.
// Asserting only the error would not prove the fix: the whole point is that
// nothing was written, so the position is checked too.
func TestSetResume_staleVersionRefusesWrite(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 1000}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// The version a second client read when it opened the video.
	stale, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Somewhere else, the user marks it watched — zeroing the position.
	if _, err := s.SetWatched("v", true); err != nil {
		t.Fatalf("set watched: %v", err)
	}

	staleVersion := stale.StateVersion
	if _, _, err := s.SetResume("v", 300, &staleVersion); !errors.Is(err, ErrStaleVersion) {
		t.Fatalf("set resume with stale version: err = %v, want ErrStaleVersion", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ResumePositionSeconds != 0 {
		t.Fatalf("resume_position_seconds = %v, want 0 — the stale write must not land", got.ResumePositionSeconds)
	}
	if !got.Watched {
		t.Fatalf("watched = false, want true — the stale write must not undo the toggle")
	}
}

// TestSetResume_versionEchoes covers the three accepting paths: the current
// version, no version at all (the back-compat escape hatch every non-Player
// caller uses), and the re-watch flow the #97 issue text calls out as the reason
// a plain "refuse writes to a watched row" guard would have been wrong.
func TestSetResume_versionEchoes(t *testing.T) {
	t.Run("currentVersionAccepted", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 1000}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		got, err := s.Get("v")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		version, watched, err := s.SetResume("v", 300, &got.StateVersion)
		if err != nil {
			t.Fatalf("set resume: %v", err)
		}
		if watched {
			t.Fatalf("watched = true, want false at 30%%")
		}
		if version != got.StateVersion {
			t.Fatalf("state_version = %d, want it unchanged at %d", version, got.StateVersion)
		}
		after, err := s.Get("v")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if after.ResumePositionSeconds != 300 {
			t.Fatalf("resume_position_seconds = %v, want 300", after.ResumePositionSeconds)
		}
	})

	t.Run("nilVersionSkipsCheck", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 1000}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		// Bump twice, so no caller could plausibly be holding the current
		// version — a nil echo must still be honoured.
		if _, err := s.SetWatched("v", true); err != nil {
			t.Fatalf("set watched: %v", err)
		}
		if _, err := s.SetWatched("v", false); err != nil {
			t.Fatalf("un-watch: %v", err)
		}
		if _, _, err := s.SetResume("v", 42, nil); err != nil {
			t.Fatalf("set resume with nil version: %v", err)
		}
		got, err := s.Get("v")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.ResumePositionSeconds != 42 {
			t.Fatalf("resume_position_seconds = %v, want 42", got.ResumePositionSeconds)
		}
	})

	t.Run("rewatchOfWatchedVideoStillSaves", func(t *testing.T) {
		s := newTestStore(t)
		if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 1000}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if _, err := s.SetWatched("v", true); err != nil {
			t.Fatalf("set watched: %v", err)
		}
		// A client that HAS seen the toggle (it echoes the post-toggle version)
		// is starting the video again. The guard must not touch it — this is
		// exactly the flow that ruled out "ignore writes to a watched row".
		got, err := s.Get("v")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if _, _, err := s.SetResume("v", 120, &got.StateVersion); err != nil {
			t.Fatalf("set resume on re-watch: %v", err)
		}
		after, err := s.Get("v")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if after.ResumePositionSeconds != 120 {
			t.Fatalf("resume_position_seconds = %v, want 120 — a re-watch must still save progress", after.ResumePositionSeconds)
		}
	})
}

// TestSetResume_onlyAutoWatchBumps pins the asymmetry the migration comment
// depends on. If a plain position write bumped, every 5s ping would invalidate
// every other client's echo and the guard would degrade into a 409 storm; if
// auto-watch did NOT bump, crossing 90% in one tab would leave another tab free
// to write its stale position back — #97 through a different door.
func TestSetResume_onlyAutoWatchBumps(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	start, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// 30% — a plain write.
	version, watched, err := s.SetResume("v", 30, &start.StateVersion)
	if err != nil {
		t.Fatalf("set resume below threshold: %v", err)
	}
	if watched {
		t.Fatalf("watched = true, want false at 30%%")
	}
	if version != start.StateVersion {
		t.Fatalf("state_version = %d, want unchanged %d on a plain position write", version, start.StateVersion)
	}

	// 95% — crosses the threshold, so this IS a watched-state transition.
	bumped, watched, err := s.SetResume("v", 95, &version)
	if err != nil {
		t.Fatalf("set resume above threshold: %v", err)
	}
	if !watched {
		t.Fatalf("watched = false, want true at 95%%")
	}
	if bumped != version+1 {
		t.Fatalf("state_version = %d, want %d after auto-watch", bumped, version+1)
	}

	// And the self-409 this is all designed to avoid: the same client's next
	// ping echoes the version the auto-watch handed back, and is accepted.
	again, watched, err := s.SetResume("v", 96, &bumped)
	if err != nil {
		t.Fatalf("next ping after auto-watch: %v, want accepted — a client must never 409 against its own threshold crossing", err)
	}
	if !watched {
		t.Fatalf("watched = false, want true — the row is already watched")
	}
	// And it must NOT bump again. Every ping in the last 10% satisfies the
	// >=90% ratio, so bumping on the ratio rather than on the unwatched->watched
	// transition would invalidate every other client's echo once per ping — the
	// 409 storm migration 0010 exists to avoid.
	if again != bumped {
		t.Fatalf("state_version = %d, want it unchanged at %d — only the transition bumps", again, bumped)
	}
}

func TestSetResume_belowThreshold_doesNotMarkWatched(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := s.SetResume("v", 50, nil); err != nil {
		t.Fatalf("set resume: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Watched {
		t.Fatalf("watched = true, want false below 90%% threshold")
	}
	if got.WatchedAt != "" {
		t.Fatalf("watched_at = %q, want empty", got.WatchedAt)
	}
}

func TestSetWatched_manualTrue_setsWatchedAt(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := s.SetWatched("v", true); err != nil {
		t.Fatalf("set watched: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Watched || got.WatchedAt == "" {
		t.Fatalf("watched=%v watched_at=%q, want true/set", got.Watched, got.WatchedAt)
	}
}

// TestSetWatched_manualTrue_resetsResumePosition covers the manual
// mark-watched rule: pressing the button means "done", so any stored resume
// position is cleared and reopening the video starts at 0:00.
func TestSetWatched_manualTrue_resetsResumePosition(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := s.SetResume("v", 42, nil); err != nil {
		t.Fatalf("set resume: %v", err)
	}
	if _, err := s.SetWatched("v", true); err != nil {
		t.Fatalf("set watched: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Watched {
		t.Fatalf("watched = false, want true")
	}
	if got.ResumePositionSeconds != 0 {
		t.Fatalf("resume_position_seconds = %v, want 0", got.ResumePositionSeconds)
	}
}

// TestSetResume_autoWatched_keepsResumePosition guards the deliberate
// asymmetry with the test above: a video that crossed the 90% threshold by
// actually playing keeps its position, so the last few minutes stay
// resumable. Only the manual button means "done".
func TestSetResume_autoWatched_keepsResumePosition(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := s.SetResume("v", 95, nil); err != nil {
		t.Fatalf("set resume: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Watched {
		t.Fatalf("watched = false, want true (95 >= 90%% of 100)")
	}
	if got.ResumePositionSeconds != 95 {
		t.Fatalf("resume_position_seconds = %v, want untouched 95", got.ResumePositionSeconds)
	}
}

func TestSetFavorite_toggles(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetFavorite("v", true); err != nil {
		t.Fatalf("set favorite: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Favorite {
		t.Fatalf("favorite = false, want true")
	}
	if err := s.SetFavorite("v", false); err != nil {
		t.Fatalf("set favorite false: %v", err)
	}
	got, err = s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Favorite {
		t.Fatalf("favorite = true, want false")
	}
}

// TestSetResume_negativePositionClampedToZero is the store-level
// defense-in-depth: the HTTP handler already rejects a negative resume
// position with 400, but the store must never persist one either, in case
// some other caller (a future internal job, a bug) skips the handler.
func TestSetResume_negativePositionClampedToZero(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, _, err := s.SetResume("v", -42, nil); err != nil {
		t.Fatalf("set resume: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ResumePositionSeconds != 0 {
		t.Fatalf("resume_position_seconds = %v, want clamped to 0", got.ResumePositionSeconds)
	}
	if got.Watched {
		t.Fatalf("watched = true, want false for a clamped-to-0 position")
	}
}
