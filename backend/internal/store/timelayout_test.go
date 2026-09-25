package store

import (
	"testing"
	"time"
)

func TestFormatTime_isUTCAtSecondPrecision(t *testing.T) {
	zone := time.FixedZone("CEST", 2*3600)
	local := time.Date(2026, 9, 25, 14, 30, 45, 999, zone)
	if got := FormatTime(local); got != "2026-09-25 12:30:45" {
		t.Fatalf("FormatTime = %q, want the UTC stamp", got)
	}
}

func TestParseTime_roundTripsAsUTC(t *testing.T) {
	got, err := ParseTime("2026-09-25 12:30:45")
	if err != nil {
		t.Fatal(err)
	}
	if got.Location() != time.UTC || got.Hour() != 12 {
		t.Fatalf("ParseTime = %v, want 12:30:45 UTC", got)
	}
	if _, err := ParseTime("2026-09-25T12:30:45Z"); err == nil {
		t.Fatal("an ISO stamp is not the column form")
	}
}
