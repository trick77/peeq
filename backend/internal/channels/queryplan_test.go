package channels

import (
	"strings"
	"testing"
)

// TestIndexedCount_staysOnTheCoveringIndex pins the "searchable" count to
// idx_videos_channel_search. It walks every video a channel ever had, and a
// videos row keeps id behind its big text columns, so a plan that touches the
// table pays overflow pages per row on every channel page load. It runs as a
// subquery of statsQuery, so the plan checked is that statement's.
func TestIndexedCount_staysOnTheCoveringIndex(t *testing.T) {
	s := newTestStore(t)
	for name, tc := range map[string]struct {
		where string
		args  []any
	}{
		"channel id":         {"channel_id = ?", []any{"UCa"}},
		"legacy name branch": {"(channel_id = ? OR (channel_id = '' AND channel_name = ?))", []any{"UCa", "A"}},
	} {
		rows, err := s.DB().Query("EXPLAIN QUERY PLAN "+statsQuery(tc.where), append(append([]any{}, tc.args...), tc.args...)...)
		if err != nil {
			t.Fatalf("%s: explain: %v", name, err)
		}
		var details []string
		for rows.Next() {
			var id, parent, notUsed int
			var detail string
			if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
				t.Fatal(err)
			}
			details = append(details, detail)
		}
		_ = rows.Close()
		plan := strings.Join(details, " | ")

		if !strings.Contains(plan, "USING COVERING INDEX idx_videos_channel_search") {
			t.Errorf("%s: not covered by idx_videos_channel_search; plan = %s", name, plan)
		}
		if strings.Contains(plan, "SCAN v") && !strings.Contains(plan, "SCAN v USING COVERING INDEX") {
			t.Errorf("%s: scans the videos table; plan = %s", name, plan)
		}
	}
}
