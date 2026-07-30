package store

import "testing"

// The backfill's entire premise is that existing rows land at embed_rev 0 and
// are therefore stale against the current recipe. If the column defaulted to
// anything else, the sweep would find nothing and the library would silently
// keep its old index.
func TestMigrate0018_existingRowsAreStale(t *testing.T) {
	db := openMigratedThrough(t, "0017_direct_stream.sql")

	// A video indexed under the pre-chapter recipe.
	if _, err := db.Exec(`
		INSERT INTO videos (id, url, status, subtitle_path, embed_model, embed_dim)
		VALUES ('v1','u','downloaded','c/v/x.vtt','text-embedding-3-small',1536)`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var rev int
	if err := db.QueryRow(`SELECT embed_rev FROM videos WHERE id='v1'`).Scan(&rev); err != nil {
		t.Fatalf("embed_rev missing after 0018: %v", err)
	}
	if rev != 0 {
		t.Errorf("embed_rev = %d for a pre-existing row, want 0 (stale)", rev)
	}
	// The model columns are untouched: the recipe changed, not the model.
	var model string
	if err := db.QueryRow(`SELECT embed_model FROM videos WHERE id='v1'`).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != "text-embedding-3-small" {
		t.Errorf("embed_model = %q, want it preserved", model)
	}
}

func TestMigrate0018_embedJobsQueueExists(t *testing.T) {
	db := openMigratedThrough(t, "0017_direct_stream.sql")
	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO videos (id, url) VALUES ('v1','u')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO embed_jobs (video_id) VALUES ('v1')`); err != nil {
		t.Fatalf("insert embed job: %v", err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM embed_jobs WHERE video_id='v1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Errorf("default state = %q, want pending", state)
	}
	// The CHECK constraint is the authority for embedjobs.States.
	if _, err := db.Exec(`INSERT INTO embed_jobs (video_id, state) VALUES ('v1','bogus')`); err == nil {
		t.Error("an invalid state should be rejected by the CHECK constraint")
	}
	// Deleting the video takes its queue rows with it.
	if _, err := db.Exec(`DELETE FROM videos WHERE id='v1'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM embed_jobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("embed_jobs rows = %d after deleting the video, want 0 (FK cascade)", n)
	}
}
