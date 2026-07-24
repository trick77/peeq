package channels

import "testing"

func TestScanDueSoon_ordersSoonestFirstWithName(t *testing.T) {
	s := newTestStore(t)
	seedSubscribed(t, s, "UCa", "2026-07-29 12:00:00")
	seedSubscribed(t, s, "UCb", "2026-07-29 12:00:00")
	setScan(t, s, "UCa", "", "2026-07-24 09:00:00")
	setScan(t, s, "UCb", "", "2026-07-23 09:00:00") // sooner

	got, err := s.ScanDueSoon(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if got[0].ChannelID != "UCb" || got[1].ChannelID != "UCa" {
		t.Fatalf("order = %v, want UCb then UCa", []string{got[0].ChannelID, got[1].ChannelID})
	}
	if got[0].Name != "UCb" || got[0].At != "2026-07-23 09:00:00" {
		t.Fatalf("first item = %+v", got[0])
	}
}

func TestScanDueSoon_respectsLimit(t *testing.T) {
	s := newTestStore(t)
	seedSubscribed(t, s, "UCa", "2026-07-29 12:00:00")
	seedSubscribed(t, s, "UCb", "2026-07-29 12:00:00")
	seedSubscribed(t, s, "UCc", "2026-07-29 12:00:00")
	got, err := s.ScanDueSoon(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("limit not respected: got %d", len(got))
	}
}

func TestMetaDueSoon_excludesNullAndOrders(t *testing.T) {
	s := newTestStore(t)
	seedSubscribed(t, s, "UCsoon", "2026-07-23 12:00:00")
	seedSubscribed(t, s, "UClate", "2026-07-25 12:00:00")
	seedSubscribed(t, s, "UCnull", "2026-07-24 12:00:00")
	// A subscription with no scheduled meta refresh must be excluded entirely,
	// not sorted as the earliest (NULL compares falsy, but the query filters it).
	if _, err := s.DB().Exec(
		`UPDATE subscriptions SET next_meta_refresh_at = NULL WHERE channel_id = 'UCnull'`); err != nil {
		t.Fatal(err)
	}

	got, err := s.MetaDueSoon(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2 (UCnull excluded)", len(got))
	}
	if got[0].ChannelID != "UCsoon" || got[1].ChannelID != "UClate" {
		t.Fatalf("order = %v", []string{got[0].ChannelID, got[1].ChannelID})
	}
}
