package playbackgrant

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/store"
)

// openTestStore opens a migrated temp DB, seeds the video rows the
// playback_grants foreign key needs, and returns a Store over it.
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

func TestMintAndResolve(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	token, expiresAt, err := s.Mint(ctx, "vid1", DefaultTTL)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if token == "" {
		t.Fatal("Mint returned an empty token")
	}
	if expiresAt == "" {
		t.Fatal("Mint must always set an expiry — the column is NOT NULL")
	}

	got, ok, err := s.Resolve(ctx, token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !ok || got != "vid1" {
		t.Fatalf("Resolve = (%q, %v), want (vid1, true)", got, ok)
	}
}

// The whole design rests on the token being unguessable, so pin the encoded
// length: 32 bytes of entropy base64url-encode to 43 characters.
func TestMint_tokenIsHighEntropy(t *testing.T) {
	s, _ := openTestStore(t)
	token, _, err := s.Mint(context.Background(), "vid1", DefaultTTL)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if len(token) != 43 {
		t.Fatalf("token length = %d, want 43 (32 raw bytes, base64url)", len(token))
	}
}

// Unlike sharelink.Upsert, a grant is never reused: two players opening the
// same video must get independent, independently-expiring URLs.
func TestMint_neverReusesAToken(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	first, _, err := s.Mint(ctx, "vid1", DefaultTTL)
	if err != nil {
		t.Fatalf("Mint first: %v", err)
	}
	second, _, err := s.Mint(ctx, "vid1", DefaultTTL)
	if err != nil {
		t.Fatalf("Mint second: %v", err)
	}
	if first == second {
		t.Fatal("minting twice for one video must produce two distinct tokens")
	}
	// Both stay live — the second must not have invalidated the first, or a
	// reload in one tab would kill playback in another.
	for _, tok := range []string{first, second} {
		if _, ok, err := s.Resolve(ctx, tok); err != nil || !ok {
			t.Fatalf("Resolve(%q) = (_, %v, %v), want live", tok, ok, err)
		}
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

// An expired grant is indistinguishable from one that never existed: ok=false,
// no error. The handler turns both into the same bare 404.
func TestResolve_expiredToken(t *testing.T) {
	s, db := openTestStore(t)
	ctx := context.Background()

	token, _, err := s.Mint(ctx, "vid1", DefaultTTL)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour).Format(sqliteTime)
	if _, err := db.Exec(`UPDATE playback_grants SET expires_at = ? WHERE token = ?`, past, token); err != nil {
		t.Fatalf("age the grant: %v", err)
	}

	_, ok, err := s.Resolve(ctx, token)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ok {
		t.Fatal("an expired grant must not resolve")
	}
}

// A ttl of zero would otherwise persist a grant that is dead the instant it is
// created, so it falls back to DefaultTTL rather than being taken literally.
func TestMint_zeroTTLFallsBackToDefault(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	token, _, err := s.Mint(ctx, "vid1", 0)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, ok, err := s.Resolve(ctx, token); err != nil || !ok {
		t.Fatalf("Resolve = (_, %v, %v), want a live grant", ok, err)
	}
}

func TestPruneExpired_removesOnlyDeadGrants(t *testing.T) {
	s, db := openTestStore(t)
	ctx := context.Background()

	live, _, err := s.Mint(ctx, "vid1", DefaultTTL)
	if err != nil {
		t.Fatalf("Mint live: %v", err)
	}
	dead, _, err := s.Mint(ctx, "vid2", DefaultTTL)
	if err != nil {
		t.Fatalf("Mint dead: %v", err)
	}
	past := time.Now().UTC().Add(-time.Hour).Format(sqliteTime)
	if _, err := db.Exec(`UPDATE playback_grants SET expires_at = ? WHERE token = ?`, past, dead); err != nil {
		t.Fatalf("age the grant: %v", err)
	}

	if err := s.PruneExpired(ctx); err != nil {
		t.Fatalf("PruneExpired: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM playback_grants`).Scan(&n); err != nil {
		t.Fatalf("count grants: %v", err)
	}
	if n != 1 {
		t.Fatalf("grant rows after prune = %d, want 1", n)
	}
	if _, ok, err := s.Resolve(ctx, live); err != nil || !ok {
		t.Fatal("PruneExpired must not touch a live grant")
	}
}

// Deleting a video takes its grants with it (ON DELETE CASCADE), so a grant can
// never outlive the media it names.
func TestDeletingVideoCascadesToGrants(t *testing.T) {
	s, db := openTestStore(t)
	ctx := context.Background()

	token, _, err := s.Mint(ctx, "vid1", DefaultTTL)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM videos WHERE id = ?`, "vid1"); err != nil {
		t.Fatalf("delete video: %v", err)
	}
	if _, ok, err := s.Resolve(ctx, token); err != nil || ok {
		t.Fatalf("Resolve after video delete = (_, %v, %v), want gone", ok, err)
	}
}
