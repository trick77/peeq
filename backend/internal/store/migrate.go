package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"testing"

	"github.com/trick77/ship/kit/migrate"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrations is the embedded directory as kit's runner reads it: the files at
// the root of the FS. fs.Sub fails only on an invalid path, and this one is a
// constant.
func migrations() fs.FS {
	sub, _ := fs.Sub(migrationsFS, "migrations")
	return sub
}

// migrateOptions logs each applied file in production. A test binary, where
// every test database applies them all, discards the lines.
var migrateOptions = migrate.Options{Dialect: migrate.SQLite, Log: migrateLog()}

func migrateLog() *slog.Logger {
	if testing.Testing() {
		return slog.New(slog.DiscardHandler)
	}
	return nil
}

// Migrate applies any embedded migrations not yet recorded in
// schema_migrations, each in its own transaction, in version order. The
// table keeps the old runner's version column, so the previous release still
// boots as a no-op after a rollback.
func Migrate(ctx context.Context, db *sql.DB) error {
	if err := prepareLegacy(ctx, db); err != nil {
		return err
	}
	_, err := migrate.Up(ctx, db, migrations(), migrateOptions)
	return err
}

// legacyNames maps every file name the old runner may have recorded to the
// file's name today. The old runner keyed schema_migrations.version by file
// name in lexicographic order, which let five numbers repeat; kit orders by
// the number and refuses a repeat, so every file from the first duplicate on
// was renumbered, keeping the order. The map is frozen: a file added later
// is recorded under its own name in both columns.
var legacyNames = map[string]string{
	"0001_init.sql":                       "0001_init.sql",
	"0002_subtitles_default.sql":          "0002_subtitles_default.sql",
	"0003_channel_metadata.sql":           "0003_channel_metadata.sql",
	"0004_category_manual.sql":            "0004_category_manual.sql",
	"0005_channel_meta_refresh.sql":       "0005_channel_meta_refresh.sql",
	"0005_videos_category_index.sql":      "0006_videos_category_index.sql",
	"0006_sponsorblock_refresh.sql":       "0007_sponsorblock_refresh.sql",
	"0007_activity.sql":                   "0008_activity.sql",
	"0008_channel_videos_published.sql":   "0009_channel_videos_published.sql",
	"0008_share_links.sql":                "0010_share_links.sql",
	"0009_scan_requested.sql":             "0011_scan_requested.sql",
	"0009_video_metadata.sql":             "0012_video_metadata.sql",
	"0010_media_probe.sql":                "0013_media_probe.sql",
	"0010_video_state_version.sql":        "0014_video_state_version.sql",
	"0011_playback_state.sql":             "0015_playback_state.sql",
	"0012_channel_added_rename.sql":       "0016_channel_added_rename.sql",
	"0013_tombstone_thumbnail.sql":        "0017_tombstone_thumbnail.sql",
	"0014_channel_videos_unavailable.sql": "0018_channel_videos_unavailable.sql",
	"0015_reclassify_hints.sql":           "0019_reclassify_hints.sql",
	"0016_scan_slots.sql":                 "0020_scan_slots.sql",
	"0017_direct_stream.sql":              "0021_direct_stream.sql",
	"0018_embed_rev.sql":                  "0022_embed_rev.sql",
	"0019_drop_embed_jobs.sql":            "0023_drop_embed_jobs.sql",
	"0020_inbox_summaries.sql":            "0024_inbox_summaries.sql",
	"0021_retire_apple_720p.sql":          "0025_retire_apple_720p.sql",
	"0022_video_thumbnails.sql":           "0026_video_thumbnails.sql",
	"0023_assets_to_database.sql":         "0027_assets_to_database.sql",
	"0024_drop_asset_paths.sql":           "0028_drop_asset_paths.sql",
	"0025_unprobed_index.sql":             "0029_unprobed_index.sql",
	"0026_keep_reads.sql":                 "0030_keep_reads.sql",
	"0027_summary_job_backoff.sql":        "0031_summary_job_backoff.sql",
	"0028_video_chat_cost.sql":            "0032_video_chat_cost.sql",
	"0029_drop_video_chat_cost.sql":       "0033_drop_video_chat_cost.sql",
	"0030_download_job_backoff.sql":       "0034_download_job_backoff.sql",
	"0031_vec_model.sql":                  "0035_vec_model.sql",
	"0032_library_load_indexes.sql":       "0036_library_load_indexes.sql",
	"0033_streams_missing.sql":            "0037_streams_missing.sql",
	"0034_caption_last_error.sql":         "0038_caption_last_error.sql",
	"0035_video_in_depth.sql":             "0039_video_in_depth.sql",
	"0036_channel_category_stats.sql":     "0040_channel_category_stats.sql",
	"0036_videos_channel_search.sql":      "0041_videos_channel_search.sql",
	"0037_keep_in_search_default_on.sql":  "0042_keep_in_search_default_on.sql",
	"0038_drop_sessions_users.sql":        "0043_drop_sessions_users.sql",
}

// deletedNames are files the old runner applied that were deleted later
// (0021_backfill_inbox_summaries.sql ran on every database before #255). Their
// rows keep their name; kit ignores a recorded name with no file.
var deletedNames = []string{
	"0002_auth.sql",
	"0002_auto_unsubscribe.sql",
	"0021_backfill_inbox_summaries.sql",
}

// legacySchema makes schema_migrations readable by both runners: the old
// runner's table with kit's name column. version stays nullable: kit inserts
// the name alone, SQLite accepts a NULL in a TEXT PRIMARY KEY of a rowid
// table, and the trigger in legacyObjects fills it. Never NOT NULL, STRICT or
// WITHOUT ROWID: each one rejects kit's insert.
const legacySchema = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version    TEXT PRIMARY KEY,
	applied_at TEXT NOT NULL DEFAULT (datetime('now')),
	name       TEXT
)`

// legacyObjects are the rest, each as sqlite_master stores it: name unique,
// so kit's "recorded once" holds while the old runner's rows may still lack
// one; the lookup from today's name to the old one; and the trigger that gives
// every row kit records that version, inside kit's own transaction. An object
// whose stored SQL differs is dropped and created again, so a change here
// reaches every database.
var legacyObjects = []struct{ kind, name, sql string }{
	{"INDEX", "schema_migrations_name", `CREATE UNIQUE INDEX schema_migrations_name ON schema_migrations(name)`},
	{"TABLE", "schema_migrations_legacy", `CREATE TABLE schema_migrations_legacy (
	name    TEXT PRIMARY KEY,
	version TEXT NOT NULL
)`},
	{"TRIGGER", "schema_migrations_version", `CREATE TRIGGER schema_migrations_version
AFTER INSERT ON schema_migrations WHEN NEW.version IS NULL
BEGIN
	UPDATE schema_migrations
	SET version = coalesce((SELECT version FROM schema_migrations_legacy WHERE name = NEW.name), NEW.name)
	WHERE rowid = NEW.rowid;
END`},
}

// querier is what reading the legacy state needs from *sql.DB and *sql.Tx.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// legacyState is schema_migrations as prepareLegacy finds it.
type legacyState struct {
	hasVersion, hasName bool
	// unnamed are the rows the old runner recorded and no name was given yet.
	unnamed []string
	// stale are the legacyObjects missing or stored with other SQL.
	stale map[string]bool
	// lookupOK is whether schema_migrations_legacy holds exactly legacyNames.
	lookupOK bool
}

func (s legacyState) current() bool {
	return s.hasVersion && s.hasName && len(s.unnamed) == 0 && len(s.stale) == 0 && s.lookupOK
}

// prepareLegacy brings schema_migrations to legacySchema and legacyObjects
// and names every row the old runner recorded (a database it wrote, or rows
// it added after a rollback), in one transaction. kit reads name and cannot
// skip a NULL. A recorded file this build never had is an error and changes
// nothing: the history would be silently cut. The first check is plain reads,
// so a boot with nothing to do opens no write transaction.
func prepareLegacy(ctx context.Context, db *sql.DB) error {
	state, err := readLegacyState(ctx, db)
	if err != nil || state.current() {
		return err
	}
	return adoptLegacy(ctx, db)
}

// adoptLegacy does prepareLegacy's work in a write transaction. It reads the
// state again inside it: another process booting the same database may have
// done the work since the plain read.
func adoptLegacy(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := readLegacyState(ctx, tx)
	if err != nil || state.current() {
		return err
	}
	names, err := currentNames(state.unnamed)
	if err != nil {
		return err
	}
	if state.hasVersion && !state.hasName {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE schema_migrations ADD COLUMN name TEXT`); err != nil {
			return fmt.Errorf("adopt schema_migrations: %w", err)
		}
	}
	for i, version := range state.unnamed {
		if _, err := tx.ExecContext(ctx,
			`UPDATE schema_migrations SET name = ? WHERE version = ?`, names[i], version,
		); err != nil {
			return fmt.Errorf("adopt schema_migrations: %w", err)
		}
	}
	stmts := []string{legacySchema}
	for _, o := range legacyObjects {
		if !state.hasVersion || state.stale[o.name] {
			stmts = append(stmts, `DROP `+o.kind+` IF EXISTS `+o.name, o.sql)
		}
	}
	stmts = append(stmts, `DELETE FROM schema_migrations_legacy`)
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("prepare schema_migrations: %w", err)
		}
	}
	for old, name := range legacyNames {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations_legacy (name, version) VALUES (?, ?)`, name, old,
		); err != nil {
			return fmt.Errorf("prepare schema_migrations: %w", err)
		}
	}
	return tx.Commit()
}

// readLegacyState reads schema_migrations' shape, its unnamed rows, the
// legacyObjects' stored SQL and the lookup rows.
func readLegacyState(ctx context.Context, q querier) (legacyState, error) {
	var s legacyState
	if err := q.QueryRowContext(ctx, `SELECT
		(SELECT count(*) > 0 FROM pragma_table_info('schema_migrations') WHERE name = 'version'),
		(SELECT count(*) > 0 FROM pragma_table_info('schema_migrations') WHERE name = 'name')`,
	).Scan(&s.hasVersion, &s.hasName); err != nil {
		return s, fmt.Errorf("read schema_migrations: %w", err)
	}
	if s.hasName && !s.hasVersion {
		return s, errors.New("schema_migrations has no version column: created by an unreleased build, recreate the database")
	}
	if !s.hasVersion {
		return s, nil
	}
	var err error
	if s.unnamed, err = unnamedVersions(ctx, q, s.hasName); err != nil {
		return s, err
	}
	stored := map[string]string{}
	rows, err := q.QueryContext(ctx, `SELECT name, sql FROM sqlite_master WHERE name IN
		('schema_migrations_name', 'schema_migrations_legacy', 'schema_migrations_version')`)
	if err != nil {
		return s, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var name, stmt string
		if err := rows.Scan(&name, &stmt); err != nil {
			return s, err
		}
		stored[name] = stmt
	}
	if err := rows.Err(); err != nil {
		return s, err
	}
	s.stale = map[string]bool{}
	for _, o := range legacyObjects {
		if stored[o.name] != o.sql {
			s.stale[o.name] = true
		}
	}
	if s.stale["schema_migrations_legacy"] {
		return s, nil
	}
	lookups := map[string]string{}
	lrows, err := q.QueryContext(ctx, `SELECT name, version FROM schema_migrations_legacy`)
	if err != nil {
		return s, fmt.Errorf("read schema_migrations_legacy: %w", err)
	}
	defer func() { _ = lrows.Close() }()
	for lrows.Next() {
		var name, version string
		if err := lrows.Scan(&name, &version); err != nil {
			return s, err
		}
		lookups[name] = version
	}
	if err := lrows.Err(); err != nil {
		return s, err
	}
	s.lookupOK = len(lookups) == len(legacyNames)
	for old, name := range legacyNames {
		if lookups[name] != old {
			s.lookupOK = false
		}
	}
	return s, nil
}

// unnamedVersions are the rows the old runner recorded and no name was given
// yet: all of them before the name column exists.
func unnamedVersions(ctx context.Context, q querier, hasName bool) ([]string, error) {
	query := `SELECT version FROM schema_migrations ORDER BY version`
	if hasName {
		query = `SELECT version FROM schema_migrations WHERE name IS NULL ORDER BY version`
	}
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// currentNames maps each version the old runner recorded to the file's name
// today: the renumbered name, else the name itself for a deleted file or one
// added after the renumbering.
func currentNames(versions []string) ([]string, error) {
	if len(versions) == 0 {
		return nil, nil
	}
	files, err := fs.ReadDir(migrations(), ".")
	if err != nil {
		return nil, err
	}
	same := map[string]bool{}
	for _, n := range deletedNames {
		same[n] = true
	}
	for _, f := range files {
		same[f.Name()] = true
	}
	out := make([]string, len(versions))
	for i, v := range versions {
		switch name, renamed := legacyNames[v]; {
		case renamed:
			out[i] = name
		case same[v]:
			out[i] = v
		default:
			return nil, fmt.Errorf("adopt schema_migrations: applied migration %s has no current file", v)
		}
	}
	return out, nil
}
