package sharelink

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/store"
)

// openTestStore opens a migrated temp DB, seeds a couple of video rows the
// share_links foreign key needs, and returns a Store over it.
func openTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, id := range []string{"vid1", "vid2"} {
		if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES (?, ?)`, id, "https://youtu.be/"+id); err != nil {
			t.Fatalf("seed video %s: %v", id, err)
		}
	}
	return New(db), db
}

func TestCreateAndResolve(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	link, err := s.Upsert(ctx, "vid1", 7*24*time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if link.Token == "" {
		t.Fatal("Create returned an empty token")
	}
	if link.ExpiresAt == "" {
		t.Fatal("Create with a ttl should set an expiry")
	}

	got, ok, err := s.Resolve(ctx, link.Token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok || got != "vid1" {
		t.Fatalf("Resolve = (%q, %v), want (vid1, true)", got, ok)
	}
}

func TestResolve_unknownToken(t *testing.T) {
	s, _ := openTestStore(t)
	_, ok, err := s.Resolve(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Fatal("an unknown token must not resolve")
	}
}

func TestResolve_emptyToken(t *testing.T) {
	s, _ := openTestStore(t)
	_, ok, err := s.Resolve(context.Background(), "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Fatal("an empty token must not resolve")
	}
}

func TestResolve_expiredToken(t *testing.T) {
	s, db := openTestStore(t)
	ctx := context.Background()

	link, err := s.Upsert(ctx, "vid1", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Force the expiry into the past — cheaper and deterministic vs sleeping.
	if _, err := db.Exec(`UPDATE share_links SET expires_at = ? WHERE token = ?`,
		time.Now().UTC().Add(-time.Minute).Format(sqliteTime), link.Token); err != nil {
		t.Fatalf("expire link: %v", err)
	}
	_, ok, err := s.Resolve(ctx, link.Token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Fatal("an expired token must not resolve")
	}
}

func TestCreate_neverExpires(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	link, err := s.Upsert(ctx, "vid1", 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if link.ExpiresAt != "" {
		t.Fatalf("ttl<=0 should never expire, got expires_at=%q", link.ExpiresAt)
	}
	_, ok, err := s.Resolve(ctx, link.Token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok {
		t.Fatal("a never-expiring token must resolve")
	}
}

func TestUpsert_keepsTokenWhileLive(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	first, err := s.Upsert(ctx, "vid1", 24*time.Hour)
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}
	// Changing the expiry of an already-live link must NOT rotate the token —
	// the owner may have already handed it out.
	second, err := s.Upsert(ctx, "vid1", 7*24*time.Hour)
	if err != nil {
		t.Fatalf("Upsert second: %v", err)
	}
	if first.Token != second.Token {
		t.Fatalf("re-stamping a live link changed the token: %q -> %q", first.Token, second.Token)
	}
	if second.ExpiresAt == first.ExpiresAt {
		t.Fatal("the new expiry should have been applied")
	}
	if _, ok, _ := s.Resolve(ctx, first.Token); !ok {
		t.Fatal("the same token must still resolve after an expiry change")
	}
}

func TestUpsert_newTokenAfterExpiry(t *testing.T) {
	s, db := openTestStore(t)
	ctx := context.Background()

	first, err := s.Upsert(ctx, "vid1", time.Hour)
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}
	// Force the first link past its expiry, then re-share.
	if _, err := db.Exec(`UPDATE share_links SET expires_at = ? WHERE token = ?`,
		time.Now().UTC().Add(-time.Minute).Format(sqliteTime), first.Token); err != nil {
		t.Fatalf("expire link: %v", err)
	}
	second, err := s.Upsert(ctx, "vid1", time.Hour)
	if err != nil {
		t.Fatalf("Upsert second: %v", err)
	}
	if first.Token == second.Token {
		t.Fatal("re-sharing after expiry should mint a new token")
	}
	if _, ok, _ := s.Resolve(ctx, first.Token); ok {
		t.Fatal("the expired token must stay dead")
	}
	if _, ok, _ := s.Resolve(ctx, second.Token); !ok {
		t.Fatal("the fresh token must resolve")
	}
}

func TestGetByVideo(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	if l, err := s.GetByVideo(ctx, "vid1"); err != nil || l != nil {
		t.Fatalf("GetByVideo on unshared video = (%v, %v), want (nil, nil)", l, err)
	}
	created, err := s.Upsert(ctx, "vid1", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.GetByVideo(ctx, "vid1")
	if err != nil {
		t.Fatalf("GetByVideo: %v", err)
	}
	if got == nil || got.Token != created.Token {
		t.Fatalf("GetByVideo returned %+v, want token %q", got, created.Token)
	}
}

func TestDeleteByVideo(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	link, err := s.Upsert(ctx, "vid1", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.DeleteByVideo(ctx, "vid1"); err != nil {
		t.Fatalf("DeleteByVideo: %v", err)
	}
	if _, ok, _ := s.Resolve(ctx, link.Token); ok {
		t.Fatal("token must not resolve after stop-sharing")
	}
	// Idempotent: deleting again is not an error.
	if err := s.DeleteByVideo(ctx, "vid1"); err != nil {
		t.Fatalf("DeleteByVideo (idempotent): %v", err)
	}
}

func TestDeleteVideo_cascadesShareLink(t *testing.T) {
	s, db := openTestStore(t)
	ctx := context.Background()

	link, err := s.Upsert(ctx, "vid1", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM videos WHERE id = ?`, "vid1"); err != nil {
		t.Fatalf("delete video: %v", err)
	}
	if _, ok, _ := s.Resolve(ctx, link.Token); ok {
		t.Fatal("deleting the video must cascade-delete its share link")
	}
}

func TestStore_surfacesDBErrors(t *testing.T) {
	s, db := openTestStore(t)
	ctx := context.Background()
	// Dropping the table makes every query fail, exercising the error-wrapping
	// branches without a real fault-injection harness.
	if _, err := db.Exec(`DROP TABLE share_links`); err != nil {
		t.Fatalf("drop table: %v", err)
	}
	if _, err := s.Upsert(ctx, "vid1", time.Hour); err == nil {
		t.Fatal("Upsert should error when the table is gone")
	}
	if _, _, err := s.Resolve(ctx, "sometoken"); err == nil {
		t.Fatal("Resolve should error when the table is gone")
	}
	if _, err := s.GetByVideo(ctx, "vid1"); err == nil {
		t.Fatal("GetByVideo should error when the table is gone")
	}
	if err := s.DeleteByVideo(ctx, "vid1"); err == nil {
		t.Fatal("DeleteByVideo should error when the table is gone")
	}
}

func TestGetByVideo_ignoresExpiredLink(t *testing.T) {
	s, db := openTestStore(t)
	ctx := context.Background()
	link, err := s.Upsert(ctx, "vid1", time.Hour)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if _, err := db.Exec(`UPDATE share_links SET expires_at = ? WHERE token = ?`,
		time.Now().UTC().Add(-time.Minute).Format(sqliteTime), link.Token); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if l, err := s.GetByVideo(ctx, "vid1"); err != nil || l != nil {
		t.Fatalf("GetByVideo on expired link = (%v, %v), want (nil, nil)", l, err)
	}
}
