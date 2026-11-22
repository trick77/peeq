package videos

import "testing"

// Every row counts, whatever its status or channel. The span is release dates
// only: a row without published_at (a scan-seeded one) is left out of it, never
// dated by the day peeq recorded it.
func TestLibrarySpan(t *testing.T) {
	db := openTestDB(t)
	s := New(db)
	for _, row := range []struct{ id, ch, status, published, created string }{
		{"a", "UCa", StatusDownloaded, "2024-03-03", "2026-01-01 10:00:00"},
		{"b", "UCa", StatusNew, "2026-06-01", "2026-06-02 10:00:00"},
		{"c", "", StatusTombstoned, "2025-01-10", "2025-02-01 10:00:00"},
		{"d", "UCb", StatusQueued, "", "2019-05-04 08:00:00"},
		{"e", "UCb", StatusNew, "", "2026-11-22 08:00:00"},
	} {
		mustExecBrowse(t, db, `INSERT INTO videos (id, url, channel_id, status, published_at, created_at) VALUES (?, 'u', ?, ?, NULLIF(?, ''), ?)`,
			row.id, row.ch, row.status, row.published, row.created)
	}
	got, err := s.LibrarySpan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := (LibrarySpan{Videos: 5, Oldest: "2024-03-03", Newest: "2026-06-01"}); got != want {
		t.Errorf("span = %+v, want %+v", got, want)
	}
}

// Rows but no release date at all: no span.
func TestLibrarySpan_noReleaseDates(t *testing.T) {
	db := openTestDB(t)
	mustExecBrowse(t, db, `INSERT INTO videos (id, url, channel_id) VALUES ('a', 'u', 'UCa')`)
	got, err := New(db).LibrarySpan(t.Context())
	if err != nil || got != (LibrarySpan{Videos: 1}) {
		t.Errorf("span = %+v, %v; want 1 video, no dates", got, err)
	}
}

func TestLibrarySpan_emptyLibrary(t *testing.T) {
	got, err := New(openTestDB(t)).LibrarySpan(t.Context())
	if err != nil || got != (LibrarySpan{}) {
		t.Errorf("empty library: %+v, %v", got, err)
	}
}

// A full pass over idx_videos_cards, never a video row.
func TestLibrarySpan_staysOnTheIndex(t *testing.T) {
	db := openTestDB(t)
	assertPlanOnly(t, "library span", queryPlan(t, db, librarySpanSQL),
		"SCAN v USING COVERING INDEX idx_videos_cards")
}
