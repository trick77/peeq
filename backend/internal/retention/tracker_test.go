package retention

import (
	"testing"
	"time"
)

// TestStreamAccessTracker_activeWithinWindowThenExpires drives the tracker
// with an injectable clock: freshly recorded access is active, and once the
// clock advances past the window it is not.
func TestStreamAccessTracker_activeWithinWindowThenExpires(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	tr := newStreamAccessTracker(5*time.Minute, clock)

	if tr.IsActive("v1") {
		t.Fatal("IsActive before any RecordAccess = true, want false")
	}

	tr.RecordAccess("v1")
	if !tr.IsActive("v1") {
		t.Fatal("IsActive immediately after RecordAccess = false, want true")
	}
	if tr.IsActive("v2") {
		t.Fatal("IsActive for a never-recorded id = true, want false")
	}

	now = now.Add(4 * time.Minute)
	if !tr.IsActive("v1") {
		t.Fatal("IsActive within window = false, want true")
	}

	now = now.Add(2 * time.Minute) // total 6 minutes, past the 5-minute window
	if tr.IsActive("v1") {
		t.Fatal("IsActive past window = true, want false")
	}
}
