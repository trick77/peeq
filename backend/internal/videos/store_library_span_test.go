package videos

import "testing"

// Every row counts, whatever its status or channel. The span is search's and
// list_videos' release date: a dateless row counts by the day it was recorded.
func TestLibrarySpan(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	for _, row := range []struct{ id, ch, status, published, created string }{
		{"a", "UCa", StatusDownloaded, "2024-03-03", "2026-01-01 10:00:00"},
		{"b", "UCa", StatusNew, "2026-06-01", "2026-06-02 10:00:00"},
		{"c", "", StatusTombstoned, "2025-01-10", "2025-02-01 10:00:00"},
		{"d", "UCb", StatusQueued, "", "2019-05-04 08:00:00"},
	} {
		mustExecBrowse(t, db, `INSERT INTO videos (id, url, channel_id, status, published_at, created_at) VALUES (?, 'u', ?, ?, NULLIF(?, ''), ?)`,
			row.id, row.ch, row.status, row.published, row.created)
	}
	got, err := s.LibrarySpan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := (LibrarySpan{Videos: 4, Oldest: "2019-05-04", Newest: "2026-06-01"}); got != want {
		t.Errorf("span = %+v, want %+v", got, want)
	}
}

func TestLibrarySpan_emptyLibrary(t *testing.T) {
	got, err := New(openTestDB(t)).LibrarySpan(t.Context())
	if err != nil || got != (LibrarySpan{}) {
		t.Errorf("empty library: %+v, %v", got, err)
	}
}

// A full pass over idx_videos_cards, never a video row.
func TestLibrarySpan_staysOnTheIndex(t *testing.T) {
	db := openTestDB(t)
	assertPlanOnly(t, "library span", queryPlan(t, db, librarySpanSQL),
		"SCAN v USING COVERING INDEX idx_videos_cards")
}

// The other half of list_youtube_channels' totals line is rag.CountVideos
// over the whole library (rag.Filter{}), verbatim here. It pins no index;
// idx_videos_cards is the one index holding every column it reads, and the
// planner must keep picking it.
func TestLibraryCount_unfilteredStaysOnTheIndexes(t *testing.T) {
	db := openTestDB(t)
	const countVideosUnfiltered = `SELECT COUNT(*), COALESCE(SUM(COALESCE(v.duration_seconds, 0)), 0),
	             COUNT(DISTINCT COALESCE(NULLIF(v.channel_id, ''), v.channel_name))
	      FROM videos v WHERE EXISTS(SELECT 1 FROM transcript_chunks tc WHERE tc.video_id = v.id)`
	assertPlanOnly(t, "library count", queryPlan(t, db, countVideosUnfiltered),
		"SCAN v USING COVERING INDEX idx_videos_cards",
		"USE TEMP B-TREE FOR count(DISTINCT)",
		"CORRELATED SCALAR SUBQUERY *",
		"SEARCH tc USING COVERING INDEX idx_transcript_chunks_video (video_id=?)")
}
