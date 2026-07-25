package playback

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/videos"
)

func newTestStore(t *testing.T) (*Store, *videos.Store, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db), videos.New(db), db
}

// seedDownloaded creates a video the pointer is allowed to point at. Get filters
// on status='downloaded', so an Upsert alone (status 'new') is not enough.
func seedDownloaded(t *testing.T, vs *videos.Store, id string) {
	t.Helper()
	if err := vs.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id}); err != nil {
		t.Fatalf("upsert %s: %v", id, err)
	}
	if err := vs.SetStatus(id, "downloaded", ""); err != nil {
		t.Fatalf("set status %s: %v", id, err)
	}
}

func TestGet_emptyBeforeAnythingIsSet(t *testing.T) {
	s, _, _ := newTestStore(t)
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	// Migration 0011 seeds the singleton with a NULL video_id, so this is a
	// plain empty pointer and not a missing-row error.
	if got.VideoID != "" {
		t.Fatalf("video_id = %q, want empty", got.VideoID)
	}
}

func TestSetThenGet_roundTrips(t *testing.T) {
	s, vs, _ := newTestStore(t)
	seedDownloaded(t, vs, "v1")
	ctx := context.Background()
	if err := s.Set(ctx, "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.VideoID != "v1" {
		t.Fatalf("video_id = %q, want v1", got.VideoID)
	}
	if got.UpdatedAt == "" {
		t.Fatalf("updated_at not stamped")
	}

	// Set is a plain UPDATE on the seeded singleton, so pointing somewhere else
	// replaces rather than accumulating.
	seedDownloaded(t, vs, "v2")
	if err := s.Set(ctx, "v2"); err != nil {
		t.Fatalf("set again: %v", err)
	}
	got, err = s.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.VideoID != "v2" {
		t.Fatalf("video_id = %q, want v2", got.VideoID)
	}
}

// TestGet_ignoresTombstonedTarget is the reason Get joins videos at all. The
// column's ON DELETE SET NULL never fires on the normal delete path: Tombstone
// keeps the row and flips status, so a raw read would hand back an id whose media
// is gone and the rail would open a player that cannot play.
//
// Tombstoning directly here, rather than through the HTTP delete handler that
// also clears the pointer, is deliberate: the filter itself is what's under test.
func TestGet_ignoresTombstonedTarget(t *testing.T) {
	s, vs, _ := newTestStore(t)
	seedDownloaded(t, vs, "v1")
	ctx := context.Background()
	if err := s.Set(ctx, "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := vs.Tombstone("v1"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	got, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.VideoID != "" {
		t.Fatalf("video_id = %q, want empty for a tombstoned target", got.VideoID)
	}
}

// TestGet_ignoresNotYetDownloadedTarget covers the other half of the status
// filter: a queued or failed video is equally unplayable.
func TestGet_ignoresNotYetDownloadedTarget(t *testing.T) {
	s, vs, _ := newTestStore(t)
	if err := vs.Upsert(videos.Video{ID: "v1", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	ctx := context.Background()
	if err := s.Set(ctx, "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.VideoID != "" {
		t.Fatalf("video_id = %q, want empty for a video that isn't downloaded", got.VideoID)
	}
}

func TestClear_dropsThePointer(t *testing.T) {
	s, vs, _ := newTestStore(t)
	seedDownloaded(t, vs, "v1")
	ctx := context.Background()
	if err := s.Set(ctx, "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.Clear(ctx); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.VideoID != "" {
		t.Fatalf("video_id = %q, want empty after clear", got.VideoID)
	}
}

// TestClearIfVideo_onlyClearsTheMatchingVideo is why ClearIfVideo exists rather
// than a bare Clear at the call sites: by the time a mark-watched or a delete
// lands, the user may already be watching something else, and an unconditional
// clear would wipe a pointer that legitimately moved on.
func TestClearIfVideo_onlyClearsTheMatchingVideo(t *testing.T) {
	s, vs, _ := newTestStore(t)
	seedDownloaded(t, vs, "v1")
	seedDownloaded(t, vs, "v2")
	ctx := context.Background()
	if err := s.Set(ctx, "v2"); err != nil {
		t.Fatalf("set: %v", err)
	}

	if err := s.ClearIfVideo(ctx, "v1"); err != nil {
		t.Fatalf("clear if video: %v", err)
	}
	got, err := s.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.VideoID != "v2" {
		t.Fatalf("video_id = %q, want v2 left alone", got.VideoID)
	}

	if err := s.ClearIfVideo(ctx, "v2"); err != nil {
		t.Fatalf("clear if video: %v", err)
	}
	got, err = s.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.VideoID != "" {
		t.Fatalf("video_id = %q, want empty", got.VideoID)
	}
}

// TestStoreErrors_wrapRatherThanPanic drops the table out from under every
// method. Each one has to surface a wrapped error naming what it was doing:
// these are the branches the httpapi layer turns into a 500, and the one place
// that distinguishes a BROKEN pointer from the MISSING one it deliberately
// treats as a no-op.
func TestStoreErrors_wrapRatherThanPanic(t *testing.T) {
	s, vs, db := newTestStore(t)
	seedDownloaded(t, vs, "v1")
	ctx := context.Background()
	if err := s.Set(ctx, "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE playback_state`); err != nil {
		t.Fatalf("drop table: %v", err)
	}

	if _, err := s.Get(ctx); err == nil {
		t.Fatal("Get on a broken table returned nil — a read failure must not look like an empty pointer")
	}
	for _, tc := range []struct {
		name string
		call func() error
	}{
		{"Set", func() error { return s.Set(ctx, "v1") }},
		{"Clear", func() error { return s.Clear(ctx) }},
		{"ClearIfVideo", func() error { return s.ClearIfVideo(ctx, "v1") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatalf("%s on a broken table returned nil", tc.name)
			}
		})
	}
}
