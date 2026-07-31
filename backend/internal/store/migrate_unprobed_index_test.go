package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// queryPlan returns the EXPLAIN QUERY PLAN detail lines for sql, joined.
func queryPlan(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN " + query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, " | ")
}

// unprobedClaim is videos.UnprobedDownloaded's statement. Kept verbatim: the
// index only helps the query it was shaped for, so a drift between the two is
// exactly what this test exists to catch.
const unprobedClaim = `
SELECT id, media_path FROM videos
WHERE status = 'downloaded' AND media_path != '' AND probed_at IS NULL
ORDER BY downloaded_at ASC
LIMIT 25`

// The media-probe worker runs this claim every 30 seconds for the life of the
// process and, in the steady state, finds nothing. Before 0025 it had no index
// to use: status = 'downloaded' matches nearly the whole table, so each pass
// walked the library and built a temp b-tree to sort it.
//
// Two assertions, and the second matters as much as the first. peeq never runs
// ANALYZE, and without statistics the planner will take an equality seek on
// idx_videos_status over an ordered walk — an earlier version of 0025 indexed
// downloaded_at alone and was quietly never used. Leading with status is what
// gets both the seek and the ordering from one index.
func TestMigrate0025_unprobedClaimUsesItsIndex(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	plan := queryPlan(t, db, unprobedClaim)
	if !strings.Contains(plan, "idx_videos_unprobed") {
		t.Fatalf("claim does not use idx_videos_unprobed; plan = %s", plan)
	}
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("claim still sorts; the index must supply downloaded_at order. plan = %s", plan)
	}
}

// The index is partial on probed_at IS NULL, so it holds only rows still
// awaiting a probe and empties out as the library is probed — that is what
// makes the steady-state pass cheap. Rows are still found in oldest-first
// order, and a probed row leaves the index entirely.
func TestMigrate0025_partialIndexHoldsOnlyUnprobedRows(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(`
		INSERT INTO videos (id, url, status, media_path, downloaded_at, probed_at) VALUES
			('newer','u','downloaded','/m/newer.mp4','2026-02-01',NULL),
			('older','u','downloaded','/m/older.mp4','2026-01-01',NULL),
			('probed','u','downloaded','/m/probed.mp4','2026-01-02','2026-01-03'),
			('notdown','u','new','/m/nd.mp4','2026-01-04',NULL)`); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(unprobedClaim)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var id, path string
		if err := rows.Scan(&id, &path); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	want := "older,newer"
	if strings.Join(got, ",") != want {
		t.Fatalf("claim returned %v, want [%s] — oldest first, probed and non-downloaded excluded", got, want)
	}
}
