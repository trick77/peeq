package store

import "testing"

// 0042 turns keep_reads on for every channel and makes on the default. It is a
// DATA migration, so it runs over channels shaped the way 0041 left them: one
// that had opted in, one that had not, and a ledger row hanging off each so the
// column swap is seen not to cascade anything away.
func TestMigrate0042_keepReadsOnForAllAndByDefault(t *testing.T) {
	db := openMigratedThrough(t, "0041_videos_channel_search.sql")
	if _, err := db.Exec(`INSERT INTO channels (id, name, keep_reads) VALUES ('UCon','on',1), ('UCoff','off',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO channel_videos (video_id, channel_id, title, state)
		VALUES ('v1','UCon','t','ignored'), ('v2','UCoff','t','pending')`); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	for _, id := range []string{"UCon", "UCoff"} {
		var keep int
		if err := db.QueryRow(`SELECT keep_reads FROM channels WHERE id = ?`, id).Scan(&keep); err != nil {
			t.Fatal(err)
		}
		if keep != 1 {
			t.Fatalf("%s keep_reads = %d after 0037, want 1", id, keep)
		}
	}
	var ledger int
	if err := db.QueryRow(`SELECT COUNT(*) FROM channel_videos`).Scan(&ledger); err != nil {
		t.Fatal(err)
	}
	if ledger != 2 {
		t.Fatalf("ledger rows = %d after 0037, want 2", ledger)
	}

	if _, err := db.Exec(`INSERT INTO channels (id, name) VALUES ('UCnew','new')`); err != nil {
		t.Fatal(err)
	}
	var keep int
	if err := db.QueryRow(`SELECT keep_reads FROM channels WHERE id = 'UCnew'`).Scan(&keep); err != nil {
		t.Fatal(err)
	}
	if keep != 1 {
		t.Fatalf("new channel keep_reads = %d, want 1 (the default)", keep)
	}
}
