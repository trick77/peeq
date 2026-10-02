package videos

import (
	"database/sql"
	"strings"
	"testing"
)

// queryPlan returns the EXPLAIN QUERY PLAN detail lines for query, joined.
func queryPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

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

// The image tables store the bytes ahead of updated_at, so reading the version
// stamp from the table walks the whole image's overflow chain, once per listed
// row. 0032's indexes carry the stamp, and these statements must stay on them.
// Each is the subquery its reader runs, verbatim.
func TestMigrate0032_imageVersionReadsNeverTouchTheBlob(t *testing.T) {
	db := openTestDB(t)
	for name, tc := range map[string]struct{ query, index string }{
		"video poster (videoColumns)": {
			`SELECT (SELECT COALESCE(strftime('%s', t.updated_at), '0') FROM video_thumbnails t INDEXED BY idx_video_thumbnails_version WHERE t.video_id = v.id) FROM videos v`,
			"idx_video_thumbnails_version",
		},
		"pending poster (channelvideos.ListPending)": {
			`SELECT (SELECT COALESCE(strftime('%s', pt.updated_at), '0') FROM pending_thumbnails pt INDEXED BY idx_pending_thumbnails_version WHERE pt.video_id = cv.video_id) FROM channel_videos cv`,
			"idx_pending_thumbnails_version",
		},
		"channel artwork (channels.List)": {
			`SELECT (SELECT COALESCE(strftime('%s', i.updated_at), '0') FROM channel_images i INDEXED BY idx_channel_images_version WHERE i.channel_id = c.id AND i.kind = 'banner') FROM channels c`,
			"idx_channel_images_version",
		},
	} {
		plan := queryPlan(t, db, tc.query)
		if !strings.Contains(plan, "USING COVERING INDEX "+tc.index) {
			t.Errorf("%s: version read is not covered by %s; plan = %s", name, tc.index, plan)
		}
	}
}

// The chip row is asked for on every Library load. Its statement must be
// answered from idx_videos_counts alone, in category order.
func TestMigrate0032_countsAreAnsweredFromTheIndex(t *testing.T) {
	db := openTestDB(t)
	query, args := countsQuery("")
	plan := queryPlan(t, db, query, args...)
	if !strings.Contains(plan, "USING COVERING INDEX idx_videos_counts") {
		t.Fatalf("counts do not use idx_videos_counts as a covering index; plan = %s", plan)
	}
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Fatalf("counts still sort; the index must supply category order. plan = %s", plan)
	}
}

// The per-channel numbers on the channel list and the channel page.
func TestMigrate0032_channelCountsSeekOnTheChannel(t *testing.T) {
	db := openTestDB(t)
	for name, tc := range map[string]struct{ query, index string }{
		"pending per channel": {
			`SELECT count(*) FROM channel_videos cv WHERE cv.channel_id = 'c' AND cv.state = 'pending'`,
			"idx_channel_videos_channel_state",
		},
		"newest ledger row per channel": {
			`SELECT channel_id, MAX(discovered_at) FROM channel_videos GROUP BY channel_id`,
			"idx_channel_videos_channel_discovered",
		},
		"downloaded per channel": {
			`SELECT count(*) FROM videos v WHERE v.channel_id = 'c' AND v.status = 'downloaded'`,
			"idx_videos_channel_status",
		},
	} {
		plan := queryPlan(t, db, tc.query)
		if !strings.Contains(plan, "USING COVERING INDEX "+tc.index) {
			t.Errorf("%s: not covered by %s; plan = %s", name, tc.index, plan)
		}
	}
}
