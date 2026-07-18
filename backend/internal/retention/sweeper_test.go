package retention

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/vark/internal/settings"
	"github.com/trick77/vark/internal/store"
	"github.com/trick77/vark/internal/videos"
)

// fakeGuard is a NowPlayingGuard whose active set is controlled directly by
// the test, so the "currently playing" exclusion can be exercised without
// any real HTTP stream or wall-clock timing.
type fakeGuard struct {
	active map[string]bool
}

func (g *fakeGuard) IsActive(id string) bool {
	return g.active[id]
}

// harness bundles a Sweeper with the raw db handle (needed to backdate
// watched_at directly — the store's own SetWatched always stamps "now") and
// a fake guard the test controls.
type harness struct {
	sw       *Sweeper
	vs       *videos.Store
	db       *sql.DB
	guard    *fakeGuard
	mediaDir string
}

func newHarness(t *testing.T, retentionDays int) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	vs := videos.New(db)
	ss := settings.New(db)
	days := retentionDays
	if err := ss.Update(t.Context(), settings.Patch{RetentionDays: &days}); err != nil {
		t.Fatalf("set retention_days: %v", err)
	}

	guard := &fakeGuard{active: map[string]bool{}}
	fixedNow := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	mediaDir := t.TempDir()

	sw := New(Deps{
		Videos:   vs,
		Settings: ss,
		MediaDir: mediaDir,
		Guard:    guard,
		Now:      func() time.Time { return fixedNow },
	})
	return &harness{sw: sw, vs: vs, db: db, guard: guard, mediaDir: mediaDir}
}

// backdateWatchedAt directly overwrites watched_at, bypassing the store's
// own SetWatched (which always stamps the real current time) so the test
// can place a video's watched_at at an arbitrary point relative to the
// sweeper's fixed clock.
func (h *harness) backdateWatchedAt(t *testing.T, id, watchedAt string) {
	t.Helper()
	var arg any
	if watchedAt == "" {
		arg = nil
	} else {
		arg = watchedAt
	}
	if _, err := h.db.Exec(`UPDATE videos SET watched_at = ? WHERE id = ?`, arg, id); err != nil {
		t.Fatalf("backdate watched_at for %s: %v", id, err)
	}
}

func TestSweepOnce_deletesOnlyWatchedNonFavoriteAgedAndNotPlaying(t *testing.T) {
	h := newHarness(t, 30) // retention_days = 30; fixed now is 2026-07-18

	seed := func(id string, watched, favorite bool, watchedAt string, playing bool) {
		t.Helper()
		if err := h.vs.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
		// Write a real file under MediaDir and point media_path at it, so
		// the test also proves the sweep actually unlinks the file on disk,
		// not just flips the DB row.
		mediaPath := filepath.Join(h.mediaDir, id+".mp4")
		if err := os.WriteFile(mediaPath, []byte("fake video bytes"), 0o644); err != nil {
			t.Fatalf("write media file %s: %v", id, err)
		}
		if err := h.vs.SetDownloaded(id, videos.DownloadedResult{MediaPath: mediaPath}); err != nil {
			t.Fatalf("set downloaded %s: %v", id, err)
		}
		if watched {
			if err := h.vs.SetWatched(id, true); err != nil {
				t.Fatalf("set watched %s: %v", id, err)
			}
		}
		if favorite {
			if err := h.vs.SetFavorite(id, true); err != nil {
				t.Fatalf("set favorite %s: %v", id, err)
			}
		}
		h.backdateWatchedAt(t, id, watchedAt)
		if playing {
			h.guard.active[id] = true
		}
	}

	// retention_days=30, fixed now=2026-07-18 -> cutoff is 2026-06-18.
	seed("delete-me", true, false, "2026-01-01 00:00:00", false)   // watched, not fav, old, not playing -> delete
	seed("keep-fav", true, true, "2026-01-01 00:00:00", false)     // favorite -> keep
	seed("keep-unwatched", false, false, "", false)                // never watched -> keep
	seed("keep-playing", true, false, "2026-01-01 00:00:00", true) // currently playing -> keep

	if err := h.sw.SweepOnce(); err != nil {
		t.Fatalf("sweep once: %v", err)
	}

	assertStatus := func(id, want string) {
		t.Helper()
		v, err := h.vs.Get(id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if v.Status != want {
			t.Fatalf("%s status = %q, want %q", id, v.Status, want)
		}
	}
	assertStatus("delete-me", "tombstoned")
	assertStatus("keep-fav", "downloaded")
	assertStatus("keep-unwatched", "downloaded")
	assertStatus("keep-playing", "downloaded")

	deleted, err := h.vs.Get("delete-me")
	if err != nil {
		t.Fatalf("get delete-me: %v", err)
	}
	if deleted.MediaPath != "" {
		t.Fatalf("delete-me media_path = %q, want cleared", deleted.MediaPath)
	}

	// The on-disk file must actually be gone, not just the DB row updated.
	if _, err := os.Stat(filepath.Join(h.mediaDir, "delete-me.mp4")); !os.IsNotExist(err) {
		t.Fatalf("delete-me.mp4 still exists on disk (stat err = %v), want removed", err)
	}
	// The kept videos' files must survive untouched.
	for _, id := range []string{"keep-fav", "keep-unwatched", "keep-playing"} {
		if _, err := os.Stat(filepath.Join(h.mediaDir, id+".mp4")); err != nil {
			t.Fatalf("%s.mp4 missing on disk after sweep, want kept: %v", id, err)
		}
	}
}

// TestSweepOnce_recentlyWatchedIsKept is the age-boundary companion: a
// video watched well within the retention window must survive a sweep even
// though it otherwise matches every other deletion criterion.
func TestSweepOnce_recentlyWatchedIsKept(t *testing.T) {
	h := newHarness(t, 30)
	if err := h.vs.Upsert(videos.Video{ID: "fresh", URL: "https://youtu.be/fresh"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := h.vs.SetDownloaded("fresh", videos.DownloadedResult{MediaPath: "fresh.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := h.vs.SetWatched("fresh", true); err != nil {
		t.Fatalf("set watched: %v", err)
	}
	h.backdateWatchedAt(t, "fresh", "2026-07-10 00:00:00") // 8 days ago, well inside 30-day retention

	if err := h.sw.SweepOnce(); err != nil {
		t.Fatalf("sweep once: %v", err)
	}
	v, err := h.vs.Get("fresh")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.Status != "downloaded" {
		t.Fatalf("status = %q, want downloaded (kept)", v.Status)
	}
}
