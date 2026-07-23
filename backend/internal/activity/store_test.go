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

	page, err := s.Recent(0, 40)
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
	first, err := s.Recent(0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || !first.HasMore {
		t.Fatalf("first page: %d events, hasMore=%v", len(first.Events), first.HasMore)
	}
	// Next page keyed on the last id.
	second, err := s.Recent(first.Events[1].ID, 2)
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
