package rag

import (
	"strings"
	"testing"
)

// The unfiltered count sizes the library on list_youtube_channels. It pins no
// index: idx_videos_cards is the one index holding every column it reads, and
// the planner must keep picking it over a table scan (a video row keeps its id
// behind the big text columns). Each video probes its chunks by key.
func TestCountVideos_unfilteredStaysOnTheIndexes(t *testing.T) {
	db, _, _ := filterFixture(t)
	q, args := countVideosQuery(Filter{})
	rows, err := db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	allowed := []string{
		"SCAN v USING COVERING INDEX idx_videos_cards",
		"USE TEMP B-TREE FOR count(DISTINCT)",
		"SEARCH tc USING COVERING INDEX idx_transcript_chunks_video (video_id=?)",
	}
	joined := strings.Join(plan, " | ")
	if !strings.Contains(joined, allowed[0]) {
		t.Errorf("plan lacks %q; plan = %s", allowed[0], joined)
	}
	for _, l := range plan {
		ok := strings.HasPrefix(l, "CORRELATED SCALAR SUBQUERY ")
		for _, a := range allowed {
			ok = ok || l == a
		}
		if !ok {
			t.Errorf("unexpected plan line %q; plan = %s", l, joined)
		}
	}
}
