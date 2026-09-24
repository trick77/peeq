package channelvideos

import (
	"fmt"
	"testing"
)

func TestGetMany_returnsOnlyKnownRows(t *testing.T) {
	st := newTestStore(t)
	seedChannel(t, st, "UC1")
	for _, id := range []string{"a", "b"} {
		if err := st.Insert(Entry{VideoID: id, ChannelID: "UC1", Title: id, State: StatePending, PublishedAt: "2026-01-01 00:00:00"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.GetMany([]string{"a", "b", "missing", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 (a, b)", len(got))
	}
	if got["a"] == nil || got["a"].Title != "a" || got["a"].PublishedAt != "2026-01-01 00:00:00" {
		t.Fatalf("row a = %+v", got["a"])
	}
	if _, ok := got["missing"]; ok {
		t.Fatal("an unknown id must be absent, not present as nil")
	}
	empty, err := st.GetMany(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty input returned %d rows", len(empty))
	}
}

func TestGetMany_spansChunks(t *testing.T) {
	st := newTestStore(t)
	seedChannel(t, st, "UC1")
	n := inChunk + 3
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("v%04d", i)
		ids = append(ids, id)
		if err := st.Insert(Entry{VideoID: id, ChannelID: "UC1", Title: id, State: StateSeen}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.GetMany(ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("got %d rows, want %d across two chunks", len(got), n)
	}
}
