package ytdlp

import "testing"

func TestParseChannelEntries_carriesAvailabilityWhenPresent(t *testing.T) {
	out := []byte(`{"entries":[
		{"id":"v1","title":"Gated","availability":"subscriber_only"},
		{"id":"v2","title":"Open","availability":"public"},
		{"id":"v3","title":"Silent"}
	]}`)

	got, err := parseChannelEntries(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("entries = %d, want 3", len(got))
	}
	if got[0].Availability != "subscriber_only" {
		t.Fatalf("v1 availability = %q", got[0].Availability)
	}
	if got[1].Availability != "public" {
		t.Fatalf("v2 availability = %q", got[1].Availability)
	}
	// A flat entry usually omits the field entirely; that must read as "no
	// opinion", never as a value.
	if got[2].Availability != "" {
		t.Fatalf("v3 availability = %q, want empty", got[2].Availability)
	}
}

func TestChannelEntry_gateReason(t *testing.T) {
	cases := map[string]string{
		"subscriber_only": "members",
		"needs_auth":      "private",
		"premium_only":    "premium",
		// Everything else is "proceed" — including silence, which is the
		// common case and must never be mistaken for a gate.
		"public":   "",
		"unlisted": "",
		"":         "",
		"nonsense": "",
	}
	for availability, want := range cases {
		if got := (ChannelEntry{Availability: availability}).GateReason(); got != want {
			t.Fatalf("GateReason(%q) = %q, want %q", availability, got, want)
		}
	}
}
