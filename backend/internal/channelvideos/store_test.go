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

// TestLedger_setPublishedAtFillsOnlyWhileNull covers the heal path for rows
// that predate the published_at column: the scanner calls SetPublishedAt on
// every already-known video it re-lists, so the guard is what keeps that from
// rewriting a date on each pass.
func TestLedger_setPublishedAtFillsOnlyWhileNull(t *testing.T) {
	st := newTestStore(t)
	seedChannel(t, st, "UCa")
	// A row inserted with no date — exactly the shape of everything already
	// sitting in a user's inbox when this shipped.
	if err := st.Insert(Entry{VideoID: "v1", ChannelID: "UCa", State: "pending"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.PublishedAt != "" {
		t.Fatalf("fresh row published = %q, want empty", got.PublishedAt)
	}

	if err := st.SetPublishedAt("v1", "2026-07-20"); err != nil {
		t.Fatal(err)
	}
	if got, err = st.Get("v1"); err != nil {
		t.Fatal(err)
	} else if got.PublishedAt != "2026-07-20" {
		t.Fatalf("after heal = %q, want 2026-07-20", got.PublishedAt)
	}

	// Second pass: a later, coarser approximation must not overwrite the
	// date already stored.
	if err := st.SetPublishedAt("v1", "2026-07-01"); err != nil {
		t.Fatal(err)
	}
	if got, err = st.Get("v1"); err != nil {
		t.Fatal(err)
	} else if got.PublishedAt != "2026-07-20" {
		t.Fatalf("re-heal changed date to %q, want 2026-07-20 kept", got.PublishedAt)
	}

	// An empty date is a no-op, leaving the row eligible for a later pass.
	if err := st.SetPublishedAt("v1", ""); err != nil {
		t.Fatalf("empty date must not error: %v", err)
	}
}

// TestLedger_insertStoresPublishedAt proves a date supplied at insert time
// survives the round-trip through both read paths (Get and ListPending scan
// separate column lists, and only one of them carries channel_name).
func TestLedger_insertStoresPublishedAt(t *testing.T) {
	st := newTestStore(t)
	seedChannel(t, st, "UCa")
	if err := st.Insert(Entry{
		VideoID: "v1", ChannelID: "UCa", State: "pending", PublishedAt: "2026-07-19",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.PublishedAt != "2026-07-19" {
		t.Fatalf("Get published = %q", got.PublishedAt)
	}
	pending, err := st.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].PublishedAt != "2026-07-19" {
		t.Fatalf("ListPending = %+v", pending)
	}
}

// TestLedger_listPendingOrdersByPublishedDate proves the inbox's default
// order is publish date, not discovery: a channel's first scan backfills a
// batch of uploads with one shared discovered_at, so ordering on discovery
// alone would leave that batch in an arbitrary order.
func TestLedger_listPendingOrdersByPublishedDate(t *testing.T) {
	st := newTestStore(t)
	seedChannel(t, st, "UCa")
	for _, e := range []Entry{
		{VideoID: "vOld", ChannelID: "UCa", State: "pending", PublishedAt: "2026-07-01"},
		{VideoID: "vNew", ChannelID: "UCa", State: "pending", PublishedAt: "2026-07-23"},
		{VideoID: "vMid", ChannelID: "UCa", State: "pending", PublishedAt: "2026-07-10"},
	} {
		if err := st.Insert(e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"vNew", "vMid", "vOld"}
	for i, id := range want {
		if got[i].VideoID != id {
			t.Fatalf("position %d = %s, want %s", i, got[i].VideoID, id)
		}
	}
}

// SummaryGaveUp reads the LATEST summary job, not any of them. The Player's
// Reprocess inserts a new job row rather than reviving the old one, so a video
// repaired that way keeps its old 'failed' row forever — an EXISTS over all of
// them would mark it failed for the rest of its life.
func TestLedger_summaryGaveUpReadsTheLatestJobOnly(t *testing.T) {
	st := newTestStore(t)
	seedChannel(t, st, "UCa")
	for _, id := range []string{"vNone", "vRetrying", "vGaveUp", "vRepaired", "vDone"} {
		if err := st.Insert(Entry{VideoID: id, ChannelID: "UCa", State: "pending"}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`INSERT INTO videos (id, url) VALUES (?, 'u')`, id); err != nil {
			t.Fatal(err)
		}
	}
	// vNone has no job at all. The rest carry one or more, newest last.
	st.db.Exec(`INSERT INTO summary_jobs (video_id, state) VALUES
		('vRetrying','pending'),
		('vGaveUp','failed'),
		('vRepaired','failed'),
		('vRepaired','pending'),
		('vDone','done')`)

	got, err := st.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"vNone":     false,
		"vRetrying": false,
		"vGaveUp":   true,
		// The failed row is still there; a newer one supersedes it.
		"vRepaired": false,
		"vDone":     false,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d", len(got), len(want))
	}
	for _, e := range got {
		if e.SummaryGaveUp != want[e.VideoID] {
			t.Errorf("%s SummaryGaveUp = %v, want %v", e.VideoID, e.SummaryGaveUp, want[e.VideoID])
		}
	}
}
