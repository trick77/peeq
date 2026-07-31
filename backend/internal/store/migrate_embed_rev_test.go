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
		INSERT INTO videos (id, url, status, embed_model, embed_dim)
		VALUES ('v1','u','downloaded','text-embedding-3-small',1536)`); err != nil {
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

// 0019 removes the one-shot backfill's queue. embed_rev must survive it: the
// column records which content recipe each video's index follows, which is
// knowable now and impossible to reconstruct later.
func TestMigrate0019_dropsQueueButKeepsEmbedRev(t *testing.T) {
	db := openMigratedThrough(t, "0018_embed_rev.sql")
	if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE videos SET embed_rev=2 WHERE id='v1'`); err != nil {
		t.Fatal(err)
	}
	// The queue exists at 0018 and takes rows.
	if _, err := db.Exec(`INSERT INTO embed_jobs (video_id) VALUES ('v1')`); err != nil {
		t.Fatalf("embed_jobs should exist before 0019: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO embed_jobs (video_id) VALUES ('v1')`); err == nil {
		t.Error("embed_jobs still exists after 0019")
	}
	var rev int
	if err := db.QueryRow(`SELECT embed_rev FROM videos WHERE id='v1'`).Scan(&rev); err != nil {
		t.Fatalf("embed_rev was dropped along with the queue: %v", err)
	}
	if rev != 2 {
		t.Errorf("embed_rev = %d, want the recorded 2 to survive", rev)
	}
}
