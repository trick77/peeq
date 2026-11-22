package store

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// cardsColumns is idx_videos_cards after 0048: 0036's columns in 0036's
// order, summary_status appended, so every INDEXED BY reader keeps its plan.
var cardsColumns = []string{"status", "id", "title", "channel_id", "channel_name", "duration_seconds",
	"published_at", "media_path", "watched", "watched_at", "resume_position_seconds", "favorite",
	"downloaded_at", "category", "created_at", "summary_status"}

// 0048 rebuilds idx_videos_cards on a populated library: the rows stay, the
// index gains summary_status, and list_videos reads it without the table.
func TestMigrate0048_cardsIndexCarriesSummaryStatus(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	applyThrough(t, db, "0047_ytdlp_ip_family.sql")
	if _, err := db.Exec(`INSERT INTO videos (id, url, title, status, summary_status) VALUES
		('a', 'u', 'Alpha', 'new', 'done'), ('b', 'u', 'Beta', 'downloaded', 'error')`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(`SELECT name FROM pragma_index_info('idx_videos_cards') ORDER BY seqno`)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	_ = rows.Close()
	if !slices.Equal(cols, cardsColumns) {
		t.Fatalf("idx_videos_cards = %v, want %v", cols, cardsColumns)
	}

	const q = `SELECT id, summary_status FROM videos INDEXED BY idx_videos_cards ORDER BY id`
	if plan := queryPlan(t, db, q); !strings.Contains(plan, "USING COVERING INDEX idx_videos_cards") {
		t.Fatalf("summary_status not read from the index; plan = %s", plan)
	}
	var got []string
	rows, err = db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			t.Fatal(err)
		}
		got = append(got, id+"="+st)
	}
	if strings.Join(got, ",") != "a=done,b=error" {
		t.Fatalf("rows after the rebuild = %v", got)
	}
}
