package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// openMigratedThrough stands a DB up at the state just BEFORE 0014, so the
// rescue clauses in that migration act on rows shaped the way the bug left
// them in production.
func openMigratedThrough(t *testing.T, stopAt string) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	applyThrough(t, db, stopAt)
	return db
}

func TestMigrate0014_rebuildAcceptsUnavailableAndKeepsRows(t *testing.T) {
	db := openMigratedThrough(t, "0013_tombstone_thumbnail.sql")
	if _, err := db.Exec(`INSERT INTO channels (id,name) VALUES ('UC1','c')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO channel_videos (video_id, channel_id, title, state, published_at)
		 VALUES ('keep','UC1','Keep me','pending','2026-07-01')`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The rebuild must carry every column across, not just the state.
	var title, state, published string
	if err := db.QueryRow(
		`SELECT title, state, published_at FROM channel_videos WHERE video_id='keep'`,
	).Scan(&title, &state, &published); err != nil {
		t.Fatal(err)
	}
	if title != "Keep me" || state != "pending" || published != "2026-07-01" {
		t.Fatalf("row mangled by rebuild: %q/%q/%q", title, state, published)
	}
	// The widened CHECK accepts the new state...
	if _, err := db.Exec(
		`UPDATE channel_videos SET state='unavailable' WHERE video_id='keep'`); err != nil {
		t.Fatalf("unavailable rejected by CHECK: %v", err)
	}
	// ...and still rejects nonsense.
	if _, err := db.Exec(
		`UPDATE channel_videos SET state='banana' WHERE video_id='keep'`); err == nil {
		t.Fatal("CHECK must still reject an unknown state")
	}
	// Both indexes survive the drop/rename under their own names.
	for _, idx := range []string{"idx_channel_videos_channel", "idx_channel_videos_state"} {
		var n int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, idx,
		).Scan(&n); err != nil || n != 1 {
			t.Fatalf("index %s missing (n=%d err=%v)", idx, n, err)
		}
	}
}

// The rescue clauses: a video stranded by the old behaviour — ledger row stuck
// at 'queued', videos row a permanent 'error' — is adopted into the new state
// and its dead Library card removed.
func TestMigrate0014_rescuesStrandedTerminalFailures(t *testing.T) {
	db := openMigratedThrough(t, "0013_tombstone_thumbnail.sql")
	if _, err := db.Exec(`INSERT INTO channels (id,name) VALUES ('UC1','c')`); err != nil {
		t.Fatal(err)
	}
	seed := func(videoID, errMsg string) {
		t.Helper()
		if _, err := db.Exec(
			`INSERT INTO channel_videos (video_id, channel_id, title, state)
			 VALUES (?, 'UC1', ?, 'queued')`, videoID, videoID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			`INSERT INTO videos (id, url, title, status, error_message)
			 VALUES (?, 'https://y/'||?, ?, 'error', ?)`,
			videoID, videoID, videoID, errMsg); err != nil {
			t.Fatal(err)
		}
	}
	seed("gated", "ytdlp: terminal (members)")
	seed("flaky", "network unreachable after 3 attempts")

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var state, reason, at string
	if err := db.QueryRow(
		`SELECT state, unavailable_reason, COALESCE(unavailable_at,'')
		 FROM channel_videos WHERE video_id='gated'`,
	).Scan(&state, &reason, &at); err != nil {
		t.Fatal(err)
	}
	if state != "unavailable" || reason != "members" {
		t.Fatalf("gated row = %q/%q, want unavailable/members", state, reason)
	}
	if at == "" {
		t.Fatal("rescued row must get a re-offer clock")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM videos WHERE id='gated'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("the dead Library row must be removed")
	}

	// A retryable failure is NOT a gate: it keeps its row and its re-download
	// button, and its ledger state is left alone.
	if err := db.QueryRow(
		`SELECT state FROM channel_videos WHERE video_id='flaky'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("flaky ledger state = %q, want it untouched", state)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM videos WHERE id='flaky'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatal("a retryable failure must keep its video row")
	}
}
