package store

import (
	"database/sql"
	"testing"
)

// ids runs a one-column query and returns its values as a set.
func ids(t *testing.T, db *sql.DB, q string) map[string]bool {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// 0045 re-queues in-depth texts stored cut off. It deletes DATA, so it runs
// over a database shaped the way 0044 left it: cut texts the retry can
// rewrite (one already queued with a history), whole texts with and without
// the space after "###", and cut texts on videos the retry cannot serve.
func TestMigrate0045_requeuesCutInDepthTexts(t *testing.T) {
	db := openMigratedThrough(t, "0044_in_depth_retry.sql")
	whole := `'Lead.' || char(10) || char(10) || '### A point [0:00]' || char(10) || char(10) || 'Body.'`
	nospace := `'Lead.' || char(10) || char(10) || '###A point [0:00]' || char(10) || char(10) || 'Body.'`
	for _, q := range []string{
		`INSERT INTO channels (id) VALUES ('UC1')`,
		`INSERT INTO videos (id, url, summary, summary_status) VALUES
			('cut', 'u', 's', 'done'), ('queued', 'u', 's', 'done'),
			('whole', 'u', 's', 'done'), ('nospace', 'u', 's', 'done'),
			('ignored', 'u', 's', 'done'), ('notranscript', 'u', 's', 'done'),
			('unsummarized', 'u', '', 'pending')`,
		`INSERT INTO video_transcripts (video_id, source, vtt) SELECT id, 'download', 'WEBVTT' FROM videos WHERE id <> 'notranscript'`,
		`INSERT INTO channel_videos (video_id, channel_id, state) VALUES ('ignored', 'UC1', 'ignored')`,
		`INSERT INTO video_in_depth (video_id, body) VALUES ('cut', 'The SR-71 tour opens in the cockpit, where')`,
		`INSERT INTO video_in_depth (video_id, body) VALUES ('queued', 'cut too')`,
		`INSERT INTO video_in_depth (video_id, body) VALUES ('whole', ` + whole + `)`,
		`INSERT INTO video_in_depth (video_id, body) VALUES ('nospace', ` + nospace + `)`,
		`INSERT INTO video_in_depth (video_id, body) VALUES ('ignored', 'a fragment')`,
		`INSERT INTO video_in_depth (video_id, body) VALUES ('notranscript', 'a fragment')`,
		`INSERT INTO video_in_depth (video_id, body) VALUES ('unsummarized', 'a fragment')`,
		`INSERT INTO in_depth_retry (video_id, attempts, last_error) VALUES ('queued', 3, 'earlier')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	texts := ids(t, db, `SELECT video_id FROM video_in_depth`)
	for _, id := range []string{"whole", "nospace", "ignored", "notranscript", "unsummarized"} {
		if !texts[id] {
			t.Errorf("%s's text was deleted, want it kept", id)
		}
	}
	for _, id := range []string{"cut", "queued"} {
		if texts[id] {
			t.Errorf("%s's cut text survived", id)
		}
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM in_depth_retry`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("queued rows = %d, %v; want cut and queued", n, err)
	}
	var cut, queued int
	if err := db.QueryRow(`SELECT
		(SELECT attempts FROM in_depth_retry WHERE video_id = 'cut'),
		(SELECT attempts FROM in_depth_retry WHERE video_id = 'queued')`).Scan(&cut, &queued); err != nil {
		t.Fatal(err)
	}
	if cut != 0 || queued != 3 {
		t.Fatalf("attempts cut=%d queued=%d; want a fresh row and the history kept", cut, queued)
	}
	if ids(t, db, `SELECT name FROM sqlite_temp_master WHERE name = 'cut_in_depth'`)["cut_in_depth"] {
		t.Error("the temp table outlived the migration")
	}
}
