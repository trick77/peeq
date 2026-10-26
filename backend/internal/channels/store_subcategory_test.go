package channels

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func seedChannelVideos(t *testing.T, s *Store, channelID string, n int, status string) {
	t.Helper()
	for i := range n {
		id := channelID + "-" + string(rune('a'+i))
		if _, err := s.db.Exec(`INSERT INTO videos (id, url, title, channel_id, status) VALUES (?, ?, ?, ?, ?)`,
			id, "u-"+id, "title "+id, channelID, status); err != nil {
			t.Fatal(err)
		}
	}
}

func addedChannel(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.Upsert(Channel{ID: id, Name: "name " + id, Description: "about " + id}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAdded(id, "2026-07-20 10:00:00"); err != nil {
		t.Fatal(err)
	}
}

func TestChannelSubcategories_classifierWriteAndManualOverride(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	addedChannel(t, s, "UCa")

	ok, err := s.SetSubcategoriesIfAuto(ctx, "UCa", []string{"sports/cycling", "tech/wearables"}, 4)
	if err != nil || !ok {
		t.Fatalf("auto write = %v, %v", ok, err)
	}
	keys, manual, err := s.Subcategories(ctx, "UCa")
	if err != nil || manual || !slices.Equal(keys, []string{"sports/cycling", "tech/wearables"}) {
		t.Fatalf("got %v manual=%v err=%v", keys, manual, err)
	}

	// The user's pick wins, and the classifier never overwrites it again.
	if err := s.SetSubcategoriesManual(ctx, "UCa", []string{"tech/wearables"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SetSubcategoriesIfAuto(ctx, "UCa", []string{"sports/cycling"}, 50); err != nil || ok {
		t.Fatalf("classifier overwrote a manual pick: %v, %v", ok, err)
	}
	keys, manual, _ = s.Subcategories(ctx, "UCa")
	if !manual || !slices.Equal(keys, []string{"tech/wearables"}) {
		t.Fatalf("got %v manual=%v", keys, manual)
	}

	// Clearing hands the channel back to the classifier.
	if err := s.ClearSubcategories(ctx, "UCa"); err != nil {
		t.Fatal(err)
	}
	keys, manual, _ = s.Subcategories(ctx, "UCa")
	if manual || len(keys) != 0 {
		t.Fatalf("after clear: %v manual=%v", keys, manual)
	}
	c, err := s.NextToSubcategorize(ctx, nil)
	if err != nil || c == nil || c.ID != "UCa" {
		t.Fatalf("cleared channel not offered: %+v, %v", c, err)
	}
}

func TestNextToSubcategorize(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	addedChannel(t, s, "UCsmall")
	seedChannelVideos(t, s, "UCsmall", 2, "new")
	addedChannel(t, s, "UCbig")
	seedChannelVideos(t, s, "UCbig", 5, "downloaded")
	// A cache-only row: never added, nothing downloaded — never offered.
	if err := s.Upsert(Channel{ID: "UCcache", Name: "cache"}); err != nil {
		t.Fatal(err)
	}

	c, err := s.NextToSubcategorize(ctx, nil)
	if err != nil || c == nil || c.ID != "UCbig" || c.Videos != 5 || c.Name != "name UCbig" || c.Description != "about UCbig" {
		t.Fatalf("got %+v, %v; want the biggest channel first", c, err)
	}
	if _, err := s.SetSubcategoriesIfAuto(ctx, "UCbig", []string{"sports/cycling"}, 5); err != nil {
		t.Fatal(err)
	}
	c, _ = s.NextToSubcategorize(ctx, []string{"UCsmall"})
	if c != nil {
		t.Fatalf("offered %+v; UCbig is answered and UCsmall parked", c)
	}

	// Answered from two titles; asked again once the channel has doubled.
	if _, err := s.SetSubcategoriesIfAuto(ctx, "UCsmall", nil, 2); err != nil {
		t.Fatal(err)
	}
	if c, _ = s.NextToSubcategorize(ctx, nil); c != nil {
		t.Fatalf("offered %+v before growth", c)
	}
	for _, id := range []string{"x1", "x2"} {
		if _, err := s.db.Exec(`INSERT INTO videos (id, url, channel_id) VALUES (?, ?, 'UCsmall')`, id, "u-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if c, _ = s.NextToSubcategorize(ctx, nil); c == nil || c.ID != "UCsmall" || c.Videos != 4 {
		t.Fatalf("grown channel not offered again: %+v", c)
	}
}

func TestAllSubcategories(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	addedChannel(t, s, "UCa")
	addedChannel(t, s, "UCb")
	_, _ = s.SetSubcategoriesIfAuto(ctx, "UCa", []string{"ai/building", "ai/infra"}, 1)
	got, err := s.AllSubcategories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got["UCa"], []string{"ai/building", "ai/infra"}) || len(got["UCb"]) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestChannelSubcategoryFilter_usesTheIndex(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN SELECT channel_id FROM channel_subcategories WHERE subcategory = ?`, "ai/building")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var a, b, c int
		var d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, d)
	}
	if !strings.Contains(strings.Join(plan, " | "), "idx_channel_subcategories_sub") {
		t.Fatalf("plan = %v", plan)
	}
}
