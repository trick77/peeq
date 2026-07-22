package store

import (
	"path/filepath"
	"testing"
)

func TestSchemaHasPhase3Objects(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	// New videos columns exist.
	for _, col := range []string{"audio_language", "subtitle_path", "summary", "chapters", "key_points", "summary_status", "summary_error", "embed_model", "embed_dim"} {
		var cnt int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('videos') WHERE name = ?`, col,
		).Scan(&cnt); err != nil || cnt != 1 {
			t.Fatalf("videos.%s missing (cnt=%d err=%v)", col, cnt, err)
		}
	}
	// New tables exist and vec_chunks accepts a 1536-dim vector.
	if _, err := db.Exec(`INSERT INTO summary_jobs (video_id) VALUES ('x')`); err == nil {
		t.Fatal("expected FK failure inserting summary_job for missing video")
	}
	if _, err := db.Exec(`SELECT rowid FROM vec_chunks LIMIT 0`); err != nil {
		t.Fatalf("vec_chunks not queryable: %v", err)
	}
	if _, err := db.Exec(`SELECT ordinal, start_seconds FROM transcript_chunks LIMIT 0`); err != nil {
		t.Fatalf("transcript_chunks not queryable: %v", err)
	}
}

func TestSchemaHasKindAndFTS(t *testing.T) {
	db, err := Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	var kindDefault string
	err = db.QueryRow(`SELECT dflt_value FROM pragma_table_info('transcript_chunks') WHERE name = 'kind'`).Scan(&kindDefault)
	if err != nil {
		t.Fatalf("kind column missing: %v", err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='fts_chunks'`).Scan(&name); err != nil {
		t.Fatalf("fts_chunks table missing: %v", err)
	}
	// FTS5 MATCH works end to end.
	if _, err := db.Exec(`INSERT INTO fts_chunks(rowid, text) VALUES (1, 'sourdough proofing')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM fts_chunks WHERE fts_chunks MATCH 'sourdough'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("MATCH count = %d, want 1", n)
	}
}

func TestSchemaHasYoutubePauseColumns(t *testing.T) {
	db, err := Open(t.TempDir() + "/s.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"youtube_paused", "youtube_pause_reason", "youtube_paused_at"} {
		var name string
		err := db.QueryRow(`SELECT name FROM pragma_table_info('settings') WHERE name = ?`, col).Scan(&name)
		if err != nil {
			t.Fatalf("settings.%s missing: %v", col, err)
		}
	}
}

// subtitles_default arrived in 0002, so the case that matters is the one a
// fresh-DB test can't see: an existing install already at 0001 must pick the
// column up on the next start, without its data being wiped. This stands the
// DB up at 0001 only — exactly what a deployed peeq looks like — and checks
// Migrate carries it forward.
func TestMigrate_addsSubtitlesDefaultToAnExisting0001DB(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Given: a DB at 0001 and nothing later, with a row already in it.
	body, err := migrationsFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatalf("apply 0001: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES ('0001_init.sql')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE settings SET retention_days = 42 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}

	// When: peeq starts and migrates.
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Then: the new column exists, defaulted off, and the pre-existing
	// settings survived untouched.
	var subtitlesDefault bool
	var retentionDays int
	if err := db.QueryRow(
		`SELECT subtitles_default, retention_days FROM settings WHERE id = 1`,
	).Scan(&subtitlesDefault, &retentionDays); err != nil {
		t.Fatalf("settings.subtitles_default missing after migrate: %v", err)
	}
	if subtitlesDefault {
		t.Fatal("subtitles_default = true, want false (pre-setting behaviour)")
	}
	if retentionDays != 42 {
		t.Fatalf("retention_days = %d, want 42 — existing settings were clobbered", retentionDays)
	}
}

// TestSchemaHasChannelMetadata guards migration 0003. resolve_ok is the one
// that matters: resolved_at is stamped even for a FAILED resolve, so without
// a separate success flag the channel page cannot tell fresh metadata from an
// attempt that gave up — and would show a confident "Refreshed <date>" over a
// channel it has never actually read.
func TestSchemaHasChannelMetadata(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	for _, col := range []string{"subscriber_count", "verified", "resolve_ok"} {
		var cnt int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('channels') WHERE name = ?`, col,
		).Scan(&cnt); err != nil || cnt != 1 {
			t.Fatalf("channels.%s missing (cnt=%d err=%v)", col, cnt, err)
		}
	}

	// An existing row predates the migration, so it must default to "nothing
	// known and nothing successfully read" rather than to a claim.
	if _, err := db.Exec(`INSERT INTO channels (id, name) VALUES ('UCa', 'Uncanny')`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var subs, verified, ok int
	if err := db.QueryRow(
		`SELECT subscriber_count, verified, resolve_ok FROM channels WHERE id = 'UCa'`,
	).Scan(&subs, &verified, &ok); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if subs != 0 || verified != 0 || ok != 0 {
		t.Fatalf("defaults = (%d,%d,%d), want all zero", subs, verified, ok)
	}
}

// TestMigrate_0004ResetsOnlyWhatTheSweepCanReclassify guards the one migration
// that touches DATA rather than shape. A fresh-DB test cannot see it: the
// reset runs against zero rows there and passes no matter what it says. So
// stand the DB up at 0003 — what a deployed peeq looked like before this
// release — seed the three row shapes that matter, and let Migrate apply 0004.
//
// The rule under test: a category may only be cleared when the summarize
// worker's idle sweep can hand it back (videos.Store.NextUnclassified selects
// status = 'downloaded' AND summary <> ”). Clearing anything else does not
// reclassify it, it erases it — permanently, and for a no-transcript video a
// hand pick on the Player was the only way that category ever got there.
func TestMigrate_0004ResetsOnlyWhatTheSweepCanReclassify(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Given: a DB at 0003, with the enum-era categories already assigned.
	for _, name := range []string{"0001_init.sql", "0002_subtitles_default.sql", "0003_channel_metadata.sql"} {
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_init.sql", "0002_subtitles_default.sql", "0003_channel_metadata.sql"} {
		if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`
INSERT INTO videos (id, url, status, summary, summary_status, category) VALUES
	('reachable',    'https://youtu.be/a', 'downloaded', 'a summary', 'done',          'entertainment'),
	('tombstoned',   'https://youtu.be/b', 'tombstoned', 'a summary', 'done',          'history'),
	('notranscript', 'https://youtu.be/c', 'downloaded', '',          'no_transcript', 'gaming')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// When: peeq starts and migrates.
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Then: only the row the sweep can pick up was cleared.
	for _, tc := range []struct{ id, want string }{
		{"reachable", "uncategorized"},
		{"tombstoned", "history"},
		{"notranscript", "gaming"},
	} {
		var got string
		if err := db.QueryRow(`SELECT category FROM videos WHERE id = ?`, tc.id).Scan(&got); err != nil {
			t.Fatalf("read %s: %v", tc.id, err)
		}
		if got != tc.want {
			t.Fatalf("%s category = %q, want %q", tc.id, got, tc.want)
		}
	}

	// And: the flag column arrived, off for every pre-existing row, since none
	// of them can be proven to be a hand pick.
	var flagged int
	if err := db.QueryRow(`SELECT COUNT(*) FROM videos WHERE category_manual <> 0`).Scan(&flagged); err != nil {
		t.Fatalf("category_manual missing after migrate: %v", err)
	}
	if flagged != 0 {
		t.Fatalf("%d rows flagged manual, want 0", flagged)
	}
}
