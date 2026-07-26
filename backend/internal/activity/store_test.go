package activity

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRecordAndRecent(t *testing.T) {
	db := openDB(t)
	s := New(db)

	s.Record(Event{Kind: KindScan, Outcome: OutcomeOK, Subject: "Veritasium", Summary: "3 new"})
	s.Record(Event{Kind: KindDownload, Outcome: OutcomeOK, Subject: "A clip"})

	page, err := s.Recent(0, 40, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(page.Events))
	}
	// Newest first.
	if page.Events[0].Kind != KindDownload || page.Events[1].Kind != KindScan {
		t.Fatalf("wrong order: %+v", page.Events)
	}
	// at is stamped by the column default.
	if page.Events[0].At == "" {
		t.Fatal("event has no timestamp")
	}
	if page.HasMore {
		t.Fatal("HasMore should be false with 2 rows and limit 40")
	}
}

func TestOnRecordFiresWithPopulatedEvent(t *testing.T) {
	db := openDB(t)
	s := New(db)
	var got Event
	var calls int
	s.OnRecord = func(e Event) { got = e; calls++ }

	s.Record(Event{Kind: KindSummary, Outcome: OutcomeOK, Subject: "A talk"})

	if calls != 1 {
		t.Fatalf("OnRecord called %d times, want 1", calls)
	}
	if got.ID == 0 || got.At == "" {
		t.Fatalf("OnRecord event not populated: %+v", got)
	}
	if got.Subject != "A talk" {
		t.Fatalf("OnRecord subject = %q", got.Subject)
	}
}

func TestRecordNeverPanicsOnBadDB(t *testing.T) {
	db := openDB(t)
	s := New(db)
	// Drop the table so the insert fails; Record must swallow the error and not
	// panic or signal failure to the caller.
	if _, err := db.Exec(`DROP TABLE activity_events`); err != nil {
		t.Fatal(err)
	}
	s.Record(Event{Kind: KindScan, Outcome: OutcomeOK}) // must not panic
}

func TestRecentKeysetPagination(t *testing.T) {
	db := openDB(t)
	s := New(db)
	for i := 0; i < 5; i++ {
		s.Record(Event{Kind: KindScan, Outcome: OutcomeOK})
	}
	// First page of 2 → HasMore, newest ids first.
	first, err := s.Recent(0, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || !first.HasMore {
		t.Fatalf("first page: %d events, hasMore=%v", len(first.Events), first.HasMore)
	}
	// Next page keyed on the last id.
	second, err := s.Recent(first.Events[1].ID, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 2 {
		t.Fatalf("second page: %d events", len(second.Events))
	}
	if second.Events[0].ID >= first.Events[1].ID {
		t.Fatalf("second page not older than first: %d >= %d", second.Events[0].ID, first.Events[1].ID)
	}
}

func TestTrimKeepsExactlyMaxRows(t *testing.T) {
	db := openDB(t)
	s := New(db)
	// Insert maxRows+50 directly (bypassing Record's per-insert trim) then one
	// Record to trigger the trim, and confirm exactly maxRows survive with the
	// oldest gone.
	for i := 0; i < maxRows+49; i++ {
		if _, err := db.Exec(`INSERT INTO activity_events (kind, outcome) VALUES ('scan','ok')`); err != nil {
			t.Fatal(err)
		}
	}
	s.Record(Event{Kind: KindScan, Outcome: OutcomeOK})

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM activity_events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != maxRows {
		t.Fatalf("row count = %d, want %d", count, maxRows)
	}
	// The very first inserted row must be gone.
	var min int64
	db.QueryRow(`SELECT MIN(id) FROM activity_events`).Scan(&min)
	if min <= 1 {
		t.Fatalf("oldest row id = %d, expected the earliest ids trimmed away", min)
	}
}

func TestRecentSearch(t *testing.T) {
	db := openDB(t)
	s := New(db)

	s.Record(Event{Kind: KindScan, Outcome: OutcomeOK, Subject: "Veritasium", Summary: "3 new"})
	s.Record(Event{Kind: KindDownload, Outcome: OutcomeOK, Subject: "A clip", Detail: "412 MiB"})
	s.Record(Event{Kind: KindSummary, Outcome: OutcomeFail, Subject: "Another clip", Summary: "summary failed"})

	t.Run("matches the subject, case-insensitively", func(t *testing.T) {
		page, err := s.Recent(0, 40, "veritasium")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) != 1 || page.Events[0].Subject != "Veritasium" {
			t.Fatalf("got %+v, want the one Veritasium row", page.Events)
		}
	})

	t.Run("matches the summary and the detail too", func(t *testing.T) {
		page, err := s.Recent(0, 40, "failed")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) != 1 || page.Events[0].Kind != KindSummary {
			t.Fatalf("summary text not searched: %+v", page.Events)
		}
		page, err = s.Recent(0, 40, "MiB")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) != 1 || page.Events[0].Kind != KindDownload {
			t.Fatalf("detail text not searched: %+v", page.Events)
		}
	})

	t.Run("matches a substring across both clips", func(t *testing.T) {
		page, err := s.Recent(0, 40, "clip")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) != 2 {
			t.Fatalf("got %d events, want both clips", len(page.Events))
		}
	})

	// A LIKE wildcard typed into the box is a literal character, or "%" would
	// match the whole log and read as a broken filter.
	t.Run("treats LIKE metacharacters literally", func(t *testing.T) {
		page, err := s.Recent(0, 40, "%")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) != 0 {
			t.Fatalf("percent matched %d rows, want 0", len(page.Events))
		}
		s.Record(Event{Kind: KindScan, Outcome: OutcomeOK, Subject: "100% real", Summary: "x"})
		page, err = s.Recent(0, 40, "100%")
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Events) != 1 {
			t.Fatalf("got %d events, want the literal 100%% row", len(page.Events))
		}
	})

	// Search and keyset paging have to compose, or paging back through a
	// filtered log would drag the unfiltered rows in behind it.
	t.Run("pages back within the filtered set", func(t *testing.T) {
		first, err := s.Recent(0, 1, "clip")
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Events) != 1 || !first.HasMore {
			t.Fatalf("got %+v, want one row and more to come", first)
		}
		second, err := s.Recent(first.Events[0].ID, 1, "clip")
		if err != nil {
			t.Fatal(err)
		}
		if len(second.Events) != 1 || second.Events[0].Subject != "A clip" {
			t.Fatalf("got %+v, want the older clip", second.Events)
		}
		if second.HasMore {
			t.Fatal("HasMore should be false past the last matching row")
		}
	})
}
