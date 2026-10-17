package videos

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestChannelCategoryStats(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	for _, row := range []struct{ id, ch, cat, yt string }{
		{"a1", "UCA", "science", `["Education","Science & Technology"]`},
		{"a2", "UCA", "science", `["Education"]`},
		{"a3", "UCA", "space", `["Education"]`},
		{"a4", "UCA", "uncategorized", `not json`},
		{"b1", "UCB", "music", `[]`},
		{"legacy", "", "science", `["Education"]`},
	} {
		if _, err := db.Exec(`INSERT INTO videos (id, url, channel_id, category, yt_categories) VALUES (?,?,?,?,?)`,
			row.id, "u", row.ch, row.cat, row.yt); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := s.ChannelCategoryStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := stats["UCA"]
	if a == nil || a.Videos != 4 {
		t.Fatalf("UCA = %+v, want 4 videos", a)
	}
	if got := countsString(a.Categories); got != "science=2 space=1" {
		t.Errorf("UCA categories = %s (uncategorized is not a topic)", got)
	}
	if got := countsString(a.YouTube); got != "Education=3 Science & Technology=1" {
		t.Errorf("UCA youtube = %s", got)
	}
	if b := stats["UCB"]; b == nil || b.Videos != 1 || len(b.YouTube) != 0 {
		t.Errorf("UCB = %+v", b)
	}
	if _, ok := stats[""]; ok {
		t.Error("rows without a channel id must not form a channel")
	}
}

func TestChannelCategoryStatsIsIndexOnly(t *testing.T) {
	db := openTestDB(t)
	plan := queryPlan(t, db, channelCategoryStatsSQL)
	if !strings.Contains(plan, "USING COVERING INDEX idx_videos_channel_categories") {
		t.Fatalf("channel category stats read the table; plan = %s", plan)
	}
}

func countsString(cs []CategoryCount) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = fmt.Sprintf("%s=%d", c.ID, c.Count)
	}
	return strings.Join(parts, " ")
}

