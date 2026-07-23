package activity

import "testing"

func TestMergeOrdersOrderedBeforeTimedThenByTime(t *testing.T) {
	items := []UpcomingItem{
		{At: "2026-07-24 15:00:00", Kind: KindScan, Subject: "Later scan"},
		{At: "", Kind: KindDownload, Subject: "Queued A"}, // ordered, imminent
		{At: "2026-07-24 09:00:00", Kind: KindChannelMeta, Subject: "Sooner meta"},
		{At: "", Kind: KindDownload, Subject: "Queued B"}, // ordered, imminent
	}
	got, truncated := Merge(items, 20)
	if truncated != 0 {
		t.Fatalf("truncated = %d, want 0", truncated)
	}
	order := []string{got[0].Subject, got[1].Subject, got[2].Subject, got[3].Subject}
	want := []string{"Queued A", "Queued B", "Sooner meta", "Later scan"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestMergeCapsAndReportsTruncated(t *testing.T) {
	var items []UpcomingItem
	for i := 0; i < 25; i++ {
		items = append(items, UpcomingItem{At: "", Kind: KindDownload})
	}
	got, truncated := Merge(items, 20)
	if len(got) != 20 {
		t.Fatalf("len = %d, want 20", len(got))
	}
	if truncated != 5 {
		t.Fatalf("truncated = %d, want 5", truncated)
	}
}

func TestMergeStableAmongOrdered(t *testing.T) {
	// Two ordered items keep their claim order (stable sort), not a reshuffle.
	items := []UpcomingItem{
		{At: "", Kind: KindDownload, Subject: "first"},
		{At: "", Kind: KindSummary, Subject: "second"},
	}
	got, _ := Merge(items, 20)
	if got[0].Subject != "first" || got[1].Subject != "second" {
		t.Fatalf("claim order not preserved: %v", got)
	}
}
