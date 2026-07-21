package channelvideos

import (
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

// newTestStore opens a migrated temp DB and returns a channelvideos Store
// backed by it, mirroring internal/channels/store_test.go's helper.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

// seedChannel inserts a channels row directly so channel_videos' FK is
// satisfied.
func seedChannel(t *testing.T, st *Store, id string) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO channels (id,name) VALUES (?, 'x')`, id); err != nil {
		t.Fatalf("seed channel %s: %v", id, err)
	}
}

// seedChannelNamed inserts a channels row with an explicit name, so a test can
// assert ListPending joins that name into each entry.
func seedChannelNamed(t *testing.T, st *Store, id, name string) {
	t.Helper()
	if _, err := st.db.Exec(`INSERT INTO channels (id,name) VALUES (?, ?)`, id, name); err != nil {
		t.Fatalf("seed channel %s: %v", id, err)
	}
}

// TestLedger_listPendingJoinsChannelName proves FIX 6: ListPending LEFT JOINs
// channels so each pending entry carries the human-readable channel name (the
// Pending UI shows the name, not the raw UCID).
func TestLedger_listPendingJoinsChannelName(t *testing.T) {
	st := newTestStore(t)
	seedChannelNamed(t, st, "UC1", "Cool Channel")

	if err := st.Insert(Entry{VideoID: "v1", ChannelID: "UC1", Title: "A", State: "pending"}); err != nil {
		t.Fatal(err)
	}
	pend, err := st.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 1 {
		t.Fatalf("pending = %d, want 1", len(pend))
	}
	if pend[0].ChannelName != "Cool Channel" {
		t.Fatalf("ChannelName = %q, want %q", pend[0].ChannelName, "Cool Channel")
	}
}

func TestLedger_insertPendingAndDecide(t *testing.T) {
	st := newTestStore(t) // migrated temp DB; a channels row UC1 must exist first (FK)
	seedChannel(t, st, "UC1")

	if err := st.Insert(Entry{VideoID: "v1", ChannelID: "UC1", Title: "A", DurationSeconds: 600, State: "pending", ThumbnailURL: "https://t/a.jpg"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Insert(Entry{VideoID: "v2", ChannelID: "UC1", Title: "B", State: "seen"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Exists("v1"); !ok {
		t.Fatal("v1 must exist")
	}
	if ok, _ := st.Exists("nope"); ok {
		t.Fatal("nope must not exist")
	}
	pend, err := st.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 1 || pend[0].VideoID != "v1" || pend[0].ThumbnailURL != "https://t/a.jpg" {
		t.Fatalf("pending = %+v", pend)
	}
	if err := st.SetState("v1", "ignored"); err != nil {
		t.Fatal(err)
	}
	pend, _ = st.ListPending()
	if len(pend) != 0 {
		t.Fatalf("after ignore, pending = %d, want 0", len(pend))
	}
}

// TestListPendingForChannel_scopesToChannel asserts the channel page's
// pending list only shows that channel's entries, not every channel's.
func TestListPendingForChannel_scopesToChannel(t *testing.T) {
	st := newTestStore(t)
	seedChannel(t, st, "UC1")
	seedChannel(t, st, "UC2")

	if err := st.Insert(Entry{VideoID: "v1", ChannelID: "UC1", Title: "A", State: "pending"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Insert(Entry{VideoID: "v2", ChannelID: "UC2", Title: "B", State: "pending"}); err != nil {
		t.Fatal(err)
	}

	pend, err := st.ListPendingForChannel("UC1")
	if err != nil {
		t.Fatal(err)
	}
	if len(pend) != 1 || pend[0].VideoID != "v1" {
		t.Fatalf("pending = %+v, want only v1", pend)
	}
}

// TestListPendingForChannel_errorsOnClosedDB asserts a query failure is
// reported to the caller rather than being mistaken for "no pending videos".
func TestListPendingForChannel_errorsOnClosedDB(t *testing.T) {
	st := newTestStore(t)
	if err := st.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if _, err := st.ListPendingForChannel("UC1"); err == nil {
		t.Fatal("expected an error listing pending for channel against a closed db")
	}
}
