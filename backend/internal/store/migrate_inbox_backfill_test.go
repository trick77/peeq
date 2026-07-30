package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// TestBackfillInboxSummaries pins what migration 0021 selects, which is the
// only interesting thing about it.
//
// caption_attempts = 99 means two different things — "retired by 0020, never
// looked at" and "the fetcher tried every rung and found nothing" — and the
// counter cannot tell them apart. Getting this wrong is not a crash: it is a
// quiet re-run of five yt-dlp calls against every video already known to have
// no captions, ending in exactly the state it started from.
func TestBackfillInboxSummaries(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO channels (id, name) VALUES ('UC1','A channel')`); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	// The migrations have already run, so this test seeds the states 0021 has
	// to tell apart and re-runs its statement against them. That is the honest
	// shape for a data migration whose subject is rows written afterwards: the
	// SQL is what is under test, not the ordering.
	seed := func(id, state string, attempts int, withVideoRow bool) {
		t.Helper()
		if _, err := db.Exec(
			`INSERT INTO channel_videos (video_id, channel_id, title, url, state, caption_attempts)
			 VALUES (?, 'UC1', 'A video', 'https://youtu.be/'||?, ?, ?)`,
			id, id, state, attempts); err != nil {
			t.Fatalf("seed ledger %s: %v", id, err)
		}
		if withVideoRow {
			if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES (?, 'https://youtu.be/'||?)`, id, id); err != nil {
				t.Fatalf("seed video %s: %v", id, err)
			}
		}
	}

	// Retired by 0020 and never looked at — the whole point of the backfill.
	seed("neverRead01", "pending", 99, false)
	// Tried every rung, no captions, settled. Must NOT be sent round again.
	seed("noCaptions1", "pending", 99, true)
	// Part-way through its ladder. Must keep its own count and schedule.
	seed("midLadder01", "pending", 2, true)
	// Already decided about: neither is in the inbox any more.
	seed("ignoredVid1", "ignored", 99, false)
	seed("queuedVid01", "queued", 99, false)

	if _, err := db.Exec(`
UPDATE channel_videos
   SET caption_attempts = 0
 WHERE state = 'pending'
   AND NOT EXISTS (SELECT 1 FROM videos v WHERE v.id = channel_videos.video_id)`); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	attempts := func(id string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT caption_attempts FROM channel_videos WHERE video_id = ?`, id).Scan(&n); err != nil {
			t.Fatalf("read %s: %v", id, err)
		}
		return n
	}

	if got := attempts("neverRead01"); got != 0 {
		t.Errorf("neverRead01 caption_attempts = %d, want 0 — this is the row the migration exists for", got)
	}
	if got := attempts("noCaptions1"); got != 99 {
		t.Errorf("noCaptions1 caption_attempts = %d, want 99 — a settled no-caption video must not be re-read", got)
	}
	if got := attempts("midLadder01"); got != 2 {
		t.Errorf("midLadder01 caption_attempts = %d, want 2 — a ladder in progress must not be extended", got)
	}
	if got := attempts("ignoredVid1"); got != 99 {
		t.Errorf("ignoredVid1 caption_attempts = %d, want 99 — it left the inbox", got)
	}
	if got := attempts("queuedVid01"); got != 99 {
		t.Errorf("queuedVid01 caption_attempts = %d, want 99 — it left the inbox", got)
	}

	// And the schedule is untouched, so a reset row is due immediately rather
	// than inheriting a stamp from somewhere.
	var next sql.NullString
	if err := db.QueryRow(
		`SELECT next_caption_attempt_at FROM channel_videos WHERE video_id = 'neverRead01'`).Scan(&next); err != nil {
		t.Fatalf("read schedule: %v", err)
	}
	if next.Valid {
		t.Errorf("next_caption_attempt_at = %q, want NULL (due now)", next.String)
	}
}
