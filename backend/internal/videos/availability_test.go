package videos

import (
	"strconv"
	"testing"
)

func TestNormalizeAvailability(t *testing.T) {
	cases := map[string]string{
		"public":          "available",
		"unlisted":        "available",
		"private":         "private",
		"premium_only":    "unknown",
		"subscriber_only": "unknown",
		"needs_auth":      "unknown",
		"":                "unknown",
		"deleted":         "unknown", // not reachable from yt-dlp metadata; falls back
		"nonsense":        "unknown",
		"  PUBLIC  ":      "available",
		"Private":         "private",
		"UNLISTED":        "available",
	}
	for in, want := range cases {
		if got := NormalizeAvailability(in); got != want {
			t.Fatalf("NormalizeAvailability(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNormalizeAvailability_everyResultIsDBValid locks the Go enum to the
// SQL CHECK constraint: every value NormalizeAvailability can return must be
// insertable into videos.availability, and every value it returns must also
// be a member of the exported Availabilities enum. This is exactly the
// coupling that broke in production (yt-dlp's "public" flowed straight into
// a column value the CHECK constraint rejected).
func TestNormalizeAvailability_everyResultIsDBValid(t *testing.T) {
	s := New(openTestDB(t))
	inputs := []string{"public", "unlisted", "private", "premium_only", "subscriber_only", "needs_auth", "", "garbage"}
	for i, in := range inputs {
		got := NormalizeAvailability(in)
		if !ValidAvailability(got) {
			t.Fatalf("NormalizeAvailability(%q) = %q, not a member of Availabilities", in, got)
		}
		v := Video{ID: "norm-check-" + strconv.Itoa(i), URL: "https://youtu.be/norm-check", Availability: got}
		if err := s.Upsert(v); err != nil {
			t.Fatalf("Upsert(NormalizeAvailability(%q)=%q) rejected by DB CHECK: %v", in, got, err)
		}
	}
}

// TestAvailabilities_allAcceptedByDBCheck inserts one row per enum value
// against a real SQLite store and confirms none violate the CHECK
// constraint on videos.availability. This is the direct lock between the Go
// enum in this file and the SQL enum in 0001_init.sql.
func TestAvailabilities_allAcceptedByDBCheck(t *testing.T) {
	s := New(openTestDB(t))
	for _, a := range Availabilities {
		v := Video{
			ID:           "avail-check-" + a,
			URL:          "https://youtu.be/avail-check",
			Availability: a,
		}
		if err := s.Upsert(v); err != nil {
			t.Fatalf("Upsert with availability %q rejected by DB: %v", a, err)
		}
		got, err := s.Get(v.ID)
		if err != nil {
			t.Fatalf("Get(%q): %v", v.ID, err)
		}
		if got == nil {
			t.Fatalf("Get(%q): nil row", v.ID)
		}
		if got.Availability != a {
			t.Fatalf("stored availability = %q, want %q", got.Availability, a)
		}
	}
}
