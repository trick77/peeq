package channels

import (
	"path/filepath"
	"testing"

	"github.com/trick77/vark/internal/store"
)

// newTestStore opens a migrated temp DB and returns a channels Store backed
// by it, mirroring internal/jobs/store_test.go's openTestDB helper.
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

func TestChannels_trackSubscribeClaim(t *testing.T) {
	st := newTestStore(t)

	if err := st.Upsert(Channel{ID: "UC1", Handle: "@one", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Upsert(Channel{ID: "UC2", Name: "Two"}); err != nil {
		t.Fatal(err)
	}
	// Tracked only → ClaimDue returns nothing.
	if sub, err := st.ClaimDue("2999-01-01 00:00:00"); err != nil || sub != nil {
		t.Fatalf("no subscriptions yet: sub=%v err=%v", sub, err)
	}
	// Subscribe both, UC1 due earlier.
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC2", "2000-06-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	sub, err := st.ClaimDue("2999-01-01 00:00:00")
	if err != nil || sub == nil || sub.ChannelID != "UC1" {
		t.Fatalf("want UC1 first, got %v err=%v", sub, err)
	}
	if sub.BaselinedAt != "" {
		t.Fatalf("new subscription must have NULL baselined_at, got %q", sub.BaselinedAt)
	}
	// Config update reflected in List.
	if err := st.UpdateConfig("UC1", true, "bestvideo+bestaudio"); err != nil {
		t.Fatal(err)
	}
	items, err := st.List("subscribed")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("subscribed count = %d, want 2", len(items))
	}
}

func TestChannels_unsubscribeKeepsChannel(t *testing.T) {
	st := newTestStore(t)
	st.Upsert(Channel{ID: "UC1", Name: "One"})
	st.Subscribe("UC1", "2000-01-01 00:00:00")
	ok, err := st.Unsubscribe("UC1")
	if err != nil || !ok {
		t.Fatalf("unsubscribe: ok=%v err=%v", ok, err)
	}
	if c, err := st.Get("UC1"); err != nil || c == nil {
		t.Fatal("channel must stay tracked after unsubscribe")
	}
	items, _ := st.List("tracked")
	if len(items) != 1 {
		t.Fatalf("tracked count = %d, want 1", len(items))
	}
}
