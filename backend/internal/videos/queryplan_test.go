package videos

import (
	"database/sql"
	"slices"
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

// A switch-off reads every tombstoned and inbox video of the channel; its videos
// side must stay on idx_videos_channel_kept, never on the table (id sits behind
// the big text columns).
func TestKeptVideos_staysOnTheCoveringIndex(t *testing.T) {
	db := openTestDB(t)
	plan := queryPlan(t, db, keptVideosQuery, "UCa")
	if !strings.Contains(plan, "USING COVERING INDEX idx_videos_channel_kept") {
		t.Fatalf("kept videos not covered by idx_videos_channel_kept; plan = %s", plan)
	}
}

// The image tables store the bytes ahead of updated_at, so reading the version
// stamp from the table walks the whole image's overflow chain, once per listed
// row. 0036's indexes carry the stamp, and these statements must stay on them.
// Each is the subquery its reader runs, verbatim.
func TestMigrate0036_imageVersionReadsNeverTouchTheBlob(t *testing.T) {
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
func TestMigrate0036_countsAreAnsweredFromTheIndex(t *testing.T) {
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
func TestMigrate0036_channelCountsSeekOnTheChannel(t *testing.T) {
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

// The grid must be answered from idx_videos_cards alone, whatever the filter
// and the sort: a plan that opens the videos table follows each row's text
// into its overflow pages, which is what made the Library slow on a cold cache.
func TestMigrate0036_listNeverOpensTheVideosTable(t *testing.T) {
	db := openTestDB(t)
	cases := []ListOptions{
		{},
		{Sort: "added_newest"},
		{Filter: "unwatched", Category: "ai", Query: "needle"},
		{Filter: "watched"}, {Filter: "favorites"}, {Filter: "in_progress"},
		{ChannelID: "UCx", Sort: "added_oldest"},
		{ChannelID: "UCx", ChannelName: "Name"},
	}
	for sort := range sortClauses {
		cases = append(cases, ListOptions{Sort: sort})
	}
	for _, opts := range cases {
		query, args := listQuery(opts)
		plan := queryPlan(t, db, query, args...)
		if !strings.Contains(plan, "v USING COVERING INDEX idx_videos_cards") {
			t.Errorf("%+v: list is not covered by idx_videos_cards; plan = %s", opts, plan)
		}
	}
}

// list_videos' page and a filtered total are answered from idx_videos_cards
// alone, for every sort, with and without the channel (legacy name arm
// included), title and date filters. The channels side is a primary-key
// lookup. The page sorts in a temp b-tree (the index leads with status):
// over index entries, never table rows.
func TestBrowse_pageAndCountNeverOpenTheVideosTable(t *testing.T) {
	db := openTestDB(t)
	filters := []BrowseOptions{
		{},
		{ChannelIDs: []string{"UCx"}},
		{ChannelIDs: []string{"UCx", "UCy"}, ChannelNames: []string{"Old Name"}},
		{Title: "needle"},
		{ChannelIDs: []string{"UCx"}, Title: "needle", After: "2025-01-01", Before: "2026-01-01"},
	}
	for _, sort := range BrowseSorts {
		for _, f := range filters {
			f.Sort, f.Limit, f.Offset = sort, 25, 50
			q := browseQueries(f)
			// Every plan line must be one of these: a table read of either
			// table (SEARCH v USING INDEX …, SCAN videos) fails.
			assertPlanOnly(t, browseCase(f, "page"), queryPlan(t, db, q.page, q.pageArgs...),
				"SCAN v USING COVERING INDEX idx_videos_cards",
				"SEARCH ch USING INDEX sqlite_autoindex_channels_1 (id=?) LEFT-JOIN",
				"USE TEMP B-TREE FOR ORDER BY")
			// An unfiltered COUNT(*) takes SQLite's count shortcut onto the
			// narrowest covering index, INDEXED BY or not: still no table read.
			want := "SCAN v USING COVERING INDEX idx_videos_cards"
			if len(q.countArgs) == 0 {
				want = "SCAN videos USING COVERING INDEX idx_videos_status"
			}
			assertPlanOnly(t, browseCase(f, "count"), queryPlan(t, db, q.count, q.countArgs...), want)
		}
	}
}

// Step two never opens the videos table: the page's ids drive two key
// lookups on covering indexes.
func TestBrowse_flagsSeekByKeyOnly(t *testing.T) {
	db := openTestDB(t)
	plan := queryPlan(t, db, browseFlagsQuery(3), "a", "b", "c")
	if !strings.Contains(plan, "SEARCH t USING COVERING INDEX sqlite_autoindex_video_transcripts_1") {
		t.Errorf("transcript check not by key; plan = %s", plan)
	}
	assertPlanOnly(t, "flags", plan,
		"SEARCH c USING COVERING INDEX idx_transcript_chunks_video (video_id=?)",
		"SEARCH t USING COVERING INDEX sqlite_autoindex_video_transcripts_1 (video_id=?)",
		"CO-ROUTINE ids", "SCAN 3 CONSTANT ROWS", "SCAN ids", "CORRELATED SCALAR SUBQUERY *")
}

// assertPlanOnly fails unless every line of plan is one of allowed (an entry
// ending in "*" matches by prefix), and the first allowed line (the main
// read) is there.
func assertPlanOnly(t *testing.T, what, plan string, allowed ...string) {
	t.Helper()
	lines := strings.Split(plan, " | ")
	if !slices.Contains(lines, allowed[0]) {
		t.Errorf("%s: plan lacks %q; plan = %s", what, allowed[0], plan)
	}
	for _, l := range lines {
		if !slices.ContainsFunc(allowed, func(a string) bool {
			p, wild := strings.CutSuffix(a, "*")
			return l == a || wild && strings.HasPrefix(l, p)
		}) {
			t.Errorf("%s: unexpected plan line %q; plan = %s", what, l, plan)
		}
	}
}

// browseCase names one plan-test case in a failure.
func browseCase(f BrowseOptions, step string) string {
	return step + " sort=" + f.Sort + " channel=" + strings.Join(f.ChannelIDs, ",") + "/" +
		strings.Join(f.ChannelNames, ",") + " title=" + f.Title + " dates=" + f.After + ".." + f.Before
}
