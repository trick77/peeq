package ytdlp

import "testing"

func TestDisplayVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026.10.26", "26.10.2026"},
		{"2026.01.05", "05.01.2026"},
		// A same-day patch or nightly build keeps its suffix, so two
		// builds of one day stay apart.
		{"2026.10.26.1", "26.10.2026-1"},
		{"2026.10.26.232813", "26.10.2026-232813"},
		// Anything that is not a dotted date passes through unchanged:
		// never blank, never a date it is not.
		{"", ""},
		{"unknown", "unknown"},
		{"2026.10", "2026.10"},
		{"2026.13.40", "2026.13.40"},
		{"2026.02.30", "2026.02.30"},
		{"2026.10.26.", "2026.10.26."},
		{"26.10.2026", "26.10.2026"},
	}
	for _, c := range cases {
		if got := DisplayVersion(c.in); got != c.want {
			t.Errorf("DisplayVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
