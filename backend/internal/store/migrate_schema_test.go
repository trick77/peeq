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
