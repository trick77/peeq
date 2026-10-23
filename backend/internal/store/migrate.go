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
// schema_migrations, each in its own transaction, in version order. A
// database the old runner wrote is adopted first, and stays readable by it.
func Migrate(ctx context.Context, db *sql.DB) error {
	if err := adoptLegacy(ctx, db); err != nil {
		return err
	}
	_, err := migrate.Up(ctx, db, migrations(), migrateOptions)
	return errors.Join(err, mirrorLegacy(ctx, db))
}

// legacyNames maps every file name the old runner may have recorded to the
// file's name today. The old runner keyed schema_migrations.version by file
// name in lexicographic order, which let five numbers repeat; kit orders by
// the number and refuses a repeat, so every file from the first duplicate on
// was renumbered, keeping the order. Remove this, adoptLegacy and
// mirrorLegacy once no instance can roll back past the adopting build.
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

// adoptLegacy adds kit's name column next to the old runner's version
// column and fills it with each recorded file's current name, in one
// transaction. version keeps the old names, so the previous release still
// finds every file it applied and boots as a no-op after a rollback. A fresh
// database, or one already adopted with every name set, is left alone. A
// recorded name the map does not know is an error: the history would be
// silently cut.
//
// Rows the old runner adds after a rollback carry no name; the next boot
// fills them the same way.
func adoptLegacy(ctx context.Context, db *sql.DB) error {
	var hasVersion, hasName bool
	if err := db.QueryRowContext(ctx, `SELECT
		count(*) FILTER (WHERE name = 'version') > 0,
		count(*) FILTER (WHERE name = 'name') > 0
		FROM pragma_table_info('schema_migrations')`,
	).Scan(&hasVersion, &hasName); err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	if !hasVersion {
		return nil
	}
	if hasName {
		var unnamed bool
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) > 0 FROM schema_migrations WHERE name IS NULL`,
		).Scan(&unnamed); err != nil {
			return fmt.Errorf("read schema_migrations: %w", err)
		}
		if !unnamed {
			return nil
		}
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if !hasName {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE schema_migrations ADD COLUMN name TEXT`); err != nil {
			return fmt.Errorf("adopt schema_migrations: %w", err)
		}
	}
	known := make([]any, 0, len(legacyNames))
	for old := range legacyNames {
		known = append(known, old)
	}
	var unknown sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT min(version) FROM schema_migrations WHERE name IS NULL AND version NOT IN (`+Placeholders(len(known))+`)`, //nolint:gosec // placeholders only
		known...,
	).Scan(&unknown); err != nil {
		return fmt.Errorf("read schema_migrations: %w", err)
	}
	if unknown.Valid {
		return fmt.Errorf("adopt schema_migrations: applied migration %s has no current file", unknown.String)
	}
	for old, name := range legacyNames {
		if _, err := tx.ExecContext(ctx,
			`UPDATE schema_migrations SET name = ? WHERE version = ? AND name IS NULL`, name, old,
		); err != nil {
			return fmt.Errorf("adopt schema_migrations: %w", err)
		}
	}
	return tx.Commit()
}

// mirrorLegacy gives every row kit recorded on an adopted database a version:
// the file's old name where it has one, so the old runner does not run it a
// second time after a rollback, else its current name, which the old runner
// has no file for. It runs after every boot, so rows a crash left without a
// version are filled on the next one.
func mirrorLegacy(ctx context.Context, db *sql.DB) error {
	var hasVersion, unmirrored bool
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) > 0 FROM pragma_table_info('schema_migrations') WHERE name = 'version'`,
	).Scan(&hasVersion); err != nil || !hasVersion {
		return err
	}
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) > 0 FROM schema_migrations WHERE version IS NULL`,
	).Scan(&unmirrored); err != nil || !unmirrored {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for old, name := range legacyNames {
		if _, err := tx.ExecContext(ctx,
			`UPDATE schema_migrations SET version = ? WHERE name = ? AND version IS NULL`, old, name,
		); err != nil {
			return fmt.Errorf("mirror schema_migrations: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE schema_migrations SET version = name WHERE version IS NULL`,
	); err != nil {
		return fmt.Errorf("mirror schema_migrations: %w", err)
	}
	return tx.Commit()
}
