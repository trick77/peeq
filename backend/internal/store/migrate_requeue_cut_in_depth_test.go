package store

import "testing"

// 0045 re-queues in-depth texts stored cut off. It deletes DATA, so it runs
// over a database shaped the way 0044 left it: a cut text, a whole one, a cut
// one already queued with a history, and a video with no text at all.
func TestMigrate0045_requeuesCutInDepthTexts(t *testing.T) {
	db := openMigratedThrough(t, "0044_in_depth_retry.sql")
	for _, q := range []string{
		`INSERT INTO videos (id, url) VALUES ('cut', 'u'), ('whole', 'u'), ('queued', 'u'), ('none', 'u')`,
		`INSERT INTO video_in_depth (video_id, body) VALUES ('cut', 'The SR-71 tour opens in the cockpit, where')`,
		`INSERT INTO video_in_depth (video_id, body) VALUES ('whole', 'Lead.' || char(10) || char(10) || '### A point [0:00]' || char(10) || char(10) || 'Body.')`,
		`INSERT INTO video_in_depth (video_id, body) VALUES ('queued', 'cut too')`,
		`INSERT INTO in_depth_retry (video_id, attempts, last_error) VALUES ('queued', 3, 'earlier')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	texts := map[string]bool{}
	rows, err := db.Query(`SELECT video_id FROM video_in_depth`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		texts[id] = true
	}
	_ = rows.Close()
	if len(texts) != 1 || !texts["whole"] {
		t.Fatalf("texts left = %v, want only the whole one", texts)
	}

	queued := map[string]int{}
	rows, err = db.Query(`SELECT video_id, attempts FROM in_depth_retry`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			t.Fatal(err)
		}
		queued[id] = n
	}
	_ = rows.Close()
	if len(queued) != 2 || queued["cut"] != 0 || queued["queued"] != 3 {
		t.Fatalf("queued = %v, want cut (fresh) and queued (history kept)", queued)
	}
}
