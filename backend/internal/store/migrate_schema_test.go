package store

import (
	"database/sql"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"testing"
)

// applyThrough stands db up part-way through the migration history: it applies
// every migration up to and including stopAt and records it, so a following
// Migrate() sees exactly the pending set a deployed peeq would. Tests that only
// care about the final schema call Migrate directly instead.
func applyThrough(t *testing.T, db *sql.DB, stopAt string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatal(err)
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			t.Fatal(err)
		}
		if name == stopAt {
			return
		}
	}
	t.Fatalf("no migration named %q", stopAt)
}

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
	applyThrough(t, db, "0001_init.sql")
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

// TestMigrate_0004ResetsWhatHasASummary guards the one migration that touches
// DATA rather than shape. A fresh-DB test cannot see it: the reset runs against
// zero rows there and passes no matter what it says. So stand the DB up at
// 0003 — what a deployed peeq looked like before this release — seed the row
// shapes that matter, and let Migrate apply 0004.
//
// The rule under test: a category may only be cleared when the summarize
// worker's idle sweep can hand it back, which means the row has a summary to
// classify from. A no-transcript video has none, and for it a hand pick on the
// Player is the only way a category could ever have been set, so clearing it
// would not reclassify it — it would erase it. The paired
// videos.TestResetSetMatchesTheSweep asserts the other half, that the sweep
// really does reach everything cleared here.
func TestMigrate_0004ResetsWhatHasASummary(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "cat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Given: a DB at 0003, with the enum-era categories already assigned.
	applyThrough(t, db, "0003_channel_metadata.sql")
	if _, err := db.Exec(`
INSERT INTO videos (id, url, status, summary, summary_status, category) VALUES
	('downloaded',   'https://youtu.be/a', 'downloaded', 'a summary', 'done',          'entertainment'),
	('tombstoned',   'https://youtu.be/b', 'tombstoned', 'a summary', 'done',          'history'),
	('notranscript', 'https://youtu.be/c', 'downloaded', '',          'no_transcript', 'gaming')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// When: peeq starts and migrates. Twice — a recorded migration must never
	// run again, which is the property that makes this reset a one-shot.
	for i := 0; i < 2; i++ {
		if err := Migrate(db); err != nil {
			t.Fatalf("Migrate #%d: %v", i+1, err)
		}
	}

	// Then: everything with a summary was cleared, including the tombstoned
	// row the sweep still reaches; the no-transcript hand pick survives.
	for _, tc := range []struct{ id, want string }{
		{"downloaded", "uncategorized"},
		{"tombstoned", "uncategorized"},
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

	// And: 0004 is recorded exactly once, so a later edit to that file is
	// silently skipped rather than replayed (see AGENTS.md).
	var recorded int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM schema_migrations WHERE version = '0004_category_manual.sql'`,
	).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("0004 recorded %d times, want 1", recorded)
	}
}

// TestMigrate_spreadsMetadataRefreshAcrossTheWeek guards 0005's backfill, which
// is the entire anti-batch mechanism. A DB that already has subscriptions gets
// them all seeded at once, and if they were all seeded to the SAME time they
// would refresh together, come due together a week later, and stay a convoy
// forever — a weekly stampede of yt-dlp calls on whatever day the migration ran.
//
// The assertion is on the SPREAD, not merely on non-null: a plain
// datetime('now','+7 days') backfill would pass a null check and still be the
// bug this test exists to catch.
func TestMigrate_spreadsMetadataRefreshAcrossTheWeek(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "spread.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Given: a DB at 0004 (no next_meta_refresh_at yet) with many subscriptions.
	applied := []string{"0001_init.sql", "0002_subtitles_default.sql", "0003_channel_metadata.sql", "0004_category_manual.sql"}
	for _, name := range applied {
		body, rerr := migrationsFS.ReadFile("migrations/" + name)
		if rerr != nil {
			t.Fatal(rerr)
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
	for _, name := range applied {
		if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
			t.Fatal(err)
		}
	}
	const channelCount = 40
	for i := 0; i < channelCount; i++ {
		id := fmt.Sprintf("UC%03d", i)
		if _, err := db.Exec(`INSERT INTO channels (id, name, added_at) VALUES (?, ?, datetime('now'))`, id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO subscriptions (channel_id, next_scan_at) VALUES (?, datetime('now'))`, id); err != nil {
			t.Fatal(err)
		}
	}

	// When: peeq starts and migrates.
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Then: every subscription has a due time, all of them inside the coming
	// week, and they are genuinely scattered rather than stacked on one moment.
	var seeded, inWindow, distinctDays int
	if err := db.QueryRow(`
SELECT COUNT(next_meta_refresh_at),
       SUM(next_meta_refresh_at BETWEEN datetime('now', '-1 minute') AND datetime('now', '+7 days')),
       COUNT(DISTINCT date(next_meta_refresh_at))
FROM subscriptions`).Scan(&seeded, &inWindow, &distinctDays); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if seeded != channelCount || inWindow != channelCount {
		t.Fatalf("seeded = %d, inside the week = %d; want %d for both", seeded, inWindow, channelCount)
	}
	// 40 uniformly random points across 8 calendar days landing on fewer than 5
	// distinct days is not a spread; it is a convoy.
	if distinctDays < 5 {
		t.Fatalf("refreshes land on only %d distinct days; the backfill is not spreading them", distinctDays)
	}
}

// TestMigrate_seedsPlaybackStateOnAnExistingDB guards migration 0011. Unlike the
// ALTER TABLE migrations, this one INSERTs: the singleton row has to be there for
// playback.Store's plain UPDATE/QueryRow to work at all, so a DB that upgraded
// rather than being created fresh must come out of Migrate with the row present.
func TestMigrate_seedsPlaybackStateOnAnExistingDB(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Given: a DB standing at the migration right before this one.
	applyThrough(t, db, "0010_video_state_version.sql")

	// When: peeq starts and migrates.
	if err := Migrate(db); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Then: exactly one row, pointing at nothing.
	var count int
	var videoID sql.NullString
	if err := db.QueryRow(`SELECT COUNT(*) FROM playback_state`).Scan(&count); err != nil {
		t.Fatalf("playback_state missing after migrate: %v", err)
	}
	if count != 1 {
		t.Fatalf("playback_state row count = %d, want 1", count)
	}
	if err := db.QueryRow(`SELECT video_id FROM playback_state WHERE id = 1`).Scan(&videoID); err != nil {
		t.Fatalf("read playback_state: %v", err)
	}
	if videoID.Valid {
		t.Fatalf("video_id = %q, want NULL on a freshly migrated DB", videoID.String)
	}

	// And: re-running is a no-op rather than a duplicate-key failure — every
	// restart re-runs Migrate.
	if err := Migrate(db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM playback_state`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("playback_state row count = %d after a second migrate, want 1", count)
	}
}

// TestSchemaRenamesTrackedAt guards migration 0012's column swap. It is a
// two-step rename — added_at has to be vacated before tracked_at can take the
// name — so getting the order wrong would either fail outright or leave the
// two columns holding each other's meaning, which nothing else would catch.
func TestSchemaRenamesTrackedAt(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	for col, want := range map[string]int{"first_seen_at": 1, "added_at": 1, "tracked_at": 0} {
		var cnt int
		if err := db.QueryRow(
			`SELECT COUNT(*) FROM pragma_table_info('channels') WHERE name = ?`, col,
		).Scan(&cnt); err != nil || cnt != want {
			t.Fatalf("channels.%s count = %d, want %d (err=%v)", col, cnt, want, err)
		}
	}

	// added_at carries the OLD tracked_at's meaning: nullable, so a cache-only
	// row can say "never added". first_seen_at keeps the NOT NULL default.
	var notNull int
	if err := db.QueryRow(
		`SELECT "notnull" FROM pragma_table_info('channels') WHERE name = 'added_at'`,
	).Scan(&notNull); err != nil {
		t.Fatal(err)
	}
	if notNull != 0 {
		t.Fatal("channels.added_at is NOT NULL; a never-added channel needs it nullable")
	}
}

// TestMigrate_backfillsChannelsFromDownloadedVideos asserts 0012's backfill.
// Adding a video by URL never created a channels row, so channels the library
// already holds downloads from had nothing to list — this is what makes them
// appear under "From downloads" on an existing install rather than only for
// videos downloaded from here on.
func TestMigrate_backfillsChannelsFromDownloadedVideos(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Stop just before 0012 so the videos below look exactly like an existing
	// install's: channel_id set, no channels row anywhere.
	applyThrough(t, db, "0011_playback_state.sql")

	if _, err := db.Exec(`
INSERT INTO videos (id, url, channel_id, channel_name, status) VALUES
  ('v1','u','UCdl','Downloaded Channel','downloaded'),
  ('v2','u','UCdl','','downloaded'),
  ('v3','u','UCqueued','Queued Channel','queued'),
  ('v4','u','','Orphan','downloaded')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	var name string
	if err := db.QueryRow(`SELECT name FROM channels WHERE id = 'UCdl'`).Scan(&name); err != nil {
		t.Fatalf("backfilled row for UCdl missing: %v", err)
	}
	// The empty channel_name on v2 must not win over v1's real one.
	if name != "Downloaded Channel" {
		t.Fatalf("backfilled name = %q, want %q", name, "Downloaded Channel")
	}
	// It is visible, but NOT added: added_at stays NULL so nothing scans it.
	var addedAt sql.NullString
	if err := db.QueryRow(`SELECT added_at FROM channels WHERE id = 'UCdl'`).Scan(&addedAt); err != nil {
		t.Fatal(err)
	}
	if addedAt.Valid {
		t.Fatalf("backfill added the channel (added_at = %q)", addedAt.String)
	}

	// A channel with nothing downloaded, and the empty channel_id, get no row.
	var cnt int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM channels WHERE id IN ('UCqueued', '')`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatalf("backfill created %d row(s) it should not have", cnt)
	}
}
