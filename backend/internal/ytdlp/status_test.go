package ytdlp

import (
	"testing"
	"time"
)

// TestStatus_updateAvailable pins the one comparison the whole update
// indicator rests on.
func TestStatus_updateAvailable(t *testing.T) {
	cases := []struct {
		name              string
		installed, latest string
		want              bool
	}{
		{"behind by a release", "2026.07.01", "2026.08.15", true},
		{"behind within a month", "2026.07.04", "2026.07.21", true},
		{"already current", "2026.08.15", "2026.08.15", false},
		// A nightly or self-built binary routinely sits ahead of the last
		// stable tag; telling that user to "update" would move them backwards.
		{"ahead of the last stable", "2026.09.02", "2026.08.15", false},
		// Nothing installed is a broken deployment, not an available update:
		// the Settings page reports the missing binary, and a rail nudge to
		// update would be the wrong story.
		{"binary unreadable", "", "2026.08.15", false},
		// Before the first successful check there is nothing to compare
		// against, and silence is the only honest answer.
		{"latest not yet known", "2026.07.01", "", false},
		{"nothing known at all", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Status{Installed: tc.installed, Latest: tc.latest}.UpdateAvailable()
			if got != tc.want {
				t.Fatalf("Status{%q, %q}.UpdateAvailable() = %v, want %v",
					tc.installed, tc.latest, got, tc.want)
			}
		})
	}
}

// TestStatusCache_setCheckErr_keepsLastKnownRelease is the failure mode the
// whole check exists to avoid: a GitHub outage must not quietly turn "an
// update is waiting" into "you look current".
func TestStatusCache_setCheckErr_keepsLastKnownRelease(t *testing.T) {
	c := NewStatusCache()
	checked := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	c.SetChecked("2026.07.01", "2026.08.15", checked)

	c.SetCheckErr("2026.07.01", "no such host")

	got := c.Get()
	if got.Latest != "2026.08.15" || !got.CheckedAt.Equal(checked) {
		t.Fatalf("failed check discarded the last known release: %+v", got)
	}
	if got.CheckErr != "no such host" {
		t.Fatalf("CheckErr = %q, want %q", got.CheckErr, "no such host")
	}
	if !got.UpdateAvailable() {
		t.Fatal("update availability lost across a failed check")
	}
}

// TestStatusCache_setChecked_clearsEarlierError covers recovery: once a check
// succeeds again the stale error must not linger on the Settings page.
func TestStatusCache_setChecked_clearsEarlierError(t *testing.T) {
	c := NewStatusCache()
	c.SetCheckErr("2026.07.01", "no such host")

	c.SetChecked("2026.07.01", "2026.08.15", time.Now())

	if got := c.Get(); got.CheckErr != "" {
		t.Fatalf("CheckErr = %q after a successful check, want empty", got.CheckErr)
	}
}

// TestStatusCache_setInstalled_clearsUpdateAvailable covers the manual Update
// button: the indicator it prompted must go away at once, not linger until
// the next scheduled check.
func TestStatusCache_setInstalled_clearsUpdateAvailable(t *testing.T) {
	c := NewStatusCache()
	checked := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	c.SetChecked("2026.07.01", "2026.08.15", checked)

	c.SetInstalled("2026.08.15")

	got := c.Get()
	if got.UpdateAvailable() {
		t.Fatalf("update still reported after installing the latest release: %+v", got)
	}
	if !got.CheckedAt.Equal(checked) {
		t.Fatalf("installing moved CheckedAt to %v, want the check's own time %v", got.CheckedAt, checked)
	}
}

// TestStatusCache_zero_reportsNoUpdate covers the cold cache every process
// starts with: nothing known must read as nothing to do.
func TestStatusCache_zero_reportsNoUpdate(t *testing.T) {
	if got := NewStatusCache().Get(); got.UpdateAvailable() {
		t.Fatalf("fresh cache reported an update: %+v", got)
	}
}
