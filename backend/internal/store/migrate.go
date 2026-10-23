package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

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

var migrateOptions = migrate.Options{Dialect: migrate.SQLite}

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
// runner's table with kit's name column, unique so kit's "recorded once"
// holds while the old runner's rows may still lack one, and a trigger that
// gives every row kit records the version the old runner knows the file by,
// inside kit's own transaction. Each statement is a no-op when its object
// exists.
var legacySchema = []string{
	`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now')),
		name       TEXT
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS schema_migrations_name ON schema_migrations(name)`,
	`CREATE TABLE IF NOT EXISTS schema_migrations_legacy (
		name    TEXT PRIMARY KEY,
		version TEXT NOT NULL
	)`,
	`CREATE TRIGGER IF NOT EXISTS schema_migrations_version
	AFTER INSERT ON schema_migrations WHEN NEW.version IS NULL
	BEGIN
		UPDATE schema_migrations
		SET version = coalesce((SELECT version FROM schema_migrations_legacy WHERE name = NEW.name), NEW.name)
		WHERE rowid = NEW.rowid;
	END`,
}

// prepareLegacy brings schema_migrations to legacySchema and names every row
// the old runner recorded (a database it wrote, or rows it added after a
// rollback), in one transaction. kit reads name and cannot skip a NULL. A
// recorded file this build never had is an error and changes nothing: the
// history would be silently cut. The checks are plain reads, so a boot with
// nothing to do opens no write transaction.
func prepareLegacy(ctx context.Context, db *sql.DB) error {
	var hasVersion, hasName bool
	var objects int
	if err := db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) > 0 FROM pragma_table_info('schema_migrations') WHERE name = 'version'),
		(SELECT count(*) > 0 FROM pragma_table_info('schema_migrations') WHERE name = 'name'),
		(SELECT count(*) FROM sqlite_master WHERE name IN
			('schema_migrations_name', 'schema_migrations_legacy', 'schema_migrations_version'))`,
	).Scan(&hasVersion, &hasName, &objects); err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	var unnamed []string
	switch {
	case hasName && !hasVersion:
		return errors.New("schema_migrations has no version column: created by an unreleased build, recreate the database")
	case hasVersion:
		var err error
		if unnamed, err = unnamedVersions(ctx, db, hasName); err != nil {
			return err
		}
		if hasName && objects == 3 && len(unnamed) == 0 {
			var lookups int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations_legacy`).Scan(&lookups); err != nil {
				return fmt.Errorf("read schema_migrations_legacy: %w", err)
			}
			if lookups == len(legacyNames) {
				return nil
			}
		}
	}
	names, err := currentNames(unnamed)
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if hasVersion && !hasName {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE schema_migrations ADD COLUMN name TEXT`); err != nil {
			return fmt.Errorf("adopt schema_migrations: %w", err)
		}
	}
	for i, version := range unnamed {
		if _, err := tx.ExecContext(ctx,
			`UPDATE schema_migrations SET name = ? WHERE version = ?`, names[i], version,
		); err != nil {
			return fmt.Errorf("adopt schema_migrations: %w", err)
		}
	}
	for _, stmt := range legacySchema {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("prepare schema_migrations: %w", err)
		}
	}
	for old, name := range legacyNames {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO schema_migrations_legacy (name, version) VALUES (?, ?)`, name, old,
		); err != nil {
			return fmt.Errorf("prepare schema_migrations: %w", err)
		}
	}
	return tx.Commit()
}

// unnamedVersions are the rows the old runner recorded and no name was given
// yet: all of them before the name column exists.
func unnamedVersions(ctx context.Context, db *sql.DB, hasName bool) ([]string, error) {
	query := `SELECT version FROM schema_migrations ORDER BY version`
	if hasName {
		query = `SELECT version FROM schema_migrations WHERE name IS NULL ORDER BY version`
	}
	rows, err := db.QueryContext(ctx, query)
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
	files, err := migrate.Files(migrations())
	if err != nil {
		return nil, err
	}
	same := map[string]bool{}
	for _, n := range deletedNames {
		same[n] = true
	}
	for _, m := range files {
		same[m.Name] = true
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
