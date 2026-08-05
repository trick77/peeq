package rag

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

// filterFixture builds the adversarial library these tests need: 100 "Alpha"
// videos clustered right at the query point, and one "Beta" video far away.
// The global top-40 is therefore entirely Alpha, so any test that finds Beta
// through a filter has proved the filter ran BEFORE the KNN picked its k.
func filterFixture(t *testing.T) (*sql.DB, *Store, []float32) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	const dim = 1536
	mk := func(v float32) []float32 {
		out := make([]float32, dim)
		out[0] = v
		return out
	}
	s := NewStore(db)
	ctx := context.Background()
	meta := IndexMeta{Model: "e5", Dim: dim, Rev: ChunkRecipeRev}

	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("a%d", i)
		if _, err := db.Exec(`INSERT INTO videos
			(id, url, channel_id, channel_name, watched, favorite, category, published_at)
			VALUES (?,?,?,?,1,0,'science','2020-01-01')`,
			id, "u", "UCA", "Alpha"); err != nil {
			t.Fatal(err)
		}
		if err := s.ReplaceVideoChunks(ctx, id, meta,
			[]ChunkRow{{Ordinal: 0, Text: "alpha ontology", StartSeconds: i}},
			[][]float32{mk(0.9 + float32(i)/10000)}); err != nil {
			t.Fatal(err)
		}
	}
	// The needle: far from the query point, a different channel, unwatched,
	// favorited, a different category and a later release date — so one fixture
	// exercises every predicate.
	if _, err := db.Exec(`INSERT INTO videos
		(id, url, channel_id, channel_name, watched, favorite, category, published_at)
		VALUES ('b1','u','UCB','Beta',0,1,'tech','2026-03-04')`); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceVideoChunks(ctx, "b1", meta,
		[]ChunkRow{{Ordinal: 0, Text: "beta ontology", StartSeconds: 42}},
		[][]float32{mk(-0.9)}); err != nil {
		t.Fatal(err)
	}
	return db, s, mk(0.9)
}

func boolPtr(b bool) *bool { return &b }

// TestRetrieveWithinFilteredIsAPreFilter is the contract the whole feature rests
// on. If vec0 ever stops honouring `rowid IN (...)` as a KNN constraint — a
// version bump, a build without SQLITE_ENABLE_VTAB_IN — the filter silently
// degrades to a post-filter and every narrow-channel search starts returning
// nothing. This test fails loudly when that happens.
func TestRetrieveWithinFilteredIsAPreFilter(t *testing.T) {
	_, s, q := filterFixture(t)
	ctx := context.Background()

	// The fixture must actually be adversarial, or the rest proves nothing.
	unfiltered, err := s.RetrieveWithinFiltered(ctx, q, 40, 0, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range unfiltered {
		if h.VideoID == "b1" {
			t.Fatal("fixture is not adversarial: b1 is inside the global top-40")
		}
	}

	cases := []struct {
		name   string
		filter Filter
	}{
		{"channel id", Filter{ChannelIDs: []string{"UCB"}}},
		{"unwatched", Filter{Watched: boolPtr(false)}},
		{"favorite", Filter{Favorite: boolPtr(true)}},
		{"category", Filter{Category: "tech"}},
		{"released after", Filter{After: "2026-01-01"}},
		{"video id", Filter{VideoIDs: []string{"b1"}}},
		{"channel and unwatched", Filter{ChannelIDs: []string{"UCB"}, Watched: boolPtr(false)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// maxDistance 0 disables the bound: b1 is deliberately far away, and
			// this test is about the filter, not about the relevance floor.
			hits, err := s.RetrieveWithinFiltered(ctx, q, 40, 0, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) != 1 {
				t.Fatalf("got %d hits, want 1 — the filter did not run before the KNN", len(hits))
			}
			if hits[0].VideoID != "b1" {
				t.Fatalf("got %s, want b1", hits[0].VideoID)
			}
		})
	}
}

// A filter that admits nothing must return nothing, rather than falling back to
// the unfiltered query — the failure mode where a mistyped constraint quietly
// searches the whole library.
func TestRetrieveWithinFilteredExcludesEverything(t *testing.T) {
	_, s, q := filterFixture(t)
	hits, err := s.RetrieveWithinFiltered(context.Background(), q, 40, 0,
		Filter{ChannelIDs: []string{"UCNOBODY"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("got %d hits for a channel with no videos, want 0", len(hits))
	}
}

// The empty filter must produce byte-for-byte the behaviour that existed before
// filtering did, so every current caller is untouched.
func TestRetrieveWithinFilteredEmptyMatchesUnfiltered(t *testing.T) {
	_, s, q := filterFixture(t)
	ctx := context.Background()
	a, err := s.RetrieveWithin(ctx, q, 40, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.RetrieveWithinFiltered(ctx, q, 40, 0, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) || len(a) == 0 {
		t.Fatalf("unfiltered %d vs empty-filter %d", len(a), len(b))
	}
	for i := range a {
		if a[i].VideoID != b[i].VideoID {
			t.Fatalf("row %d: %s vs %s", i, a[i].VideoID, b[i].VideoID)
		}
	}
}

// k is clamped to vec0's own ceiling; passing more is an error from the vtab,
// not a bigger result set.
func TestRetrieveWithinFilteredClampsK(t *testing.T) {
	_, s, q := filterFixture(t)
	if _, err := s.RetrieveWithinFiltered(context.Background(), q, vecKMax*2, 0, Filter{}); err != nil {
		t.Fatalf("k above the vec0 ceiling should be clamped, not passed through: %v", err)
	}
}

func TestSearchFTSFiltered(t *testing.T) {
	_, s, _ := filterFixture(t)
	ctx := context.Background()

	all, err := s.SearchFTS(ctx, "ontology", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 101 {
		t.Fatalf("unfiltered FTS got %d, want 101", len(all))
	}

	beta, err := s.SearchFTSFiltered(ctx, "ontology", 200, Filter{ChannelIDs: []string{"UCB"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(beta) != 1 || beta[0].VideoID != "b1" {
		t.Fatalf("channel-filtered FTS = %+v, want one b1 hit", beta)
	}
	// The snippet contract survives the added join: highlights still delimited.
	if beta[0].Snippet == "" {
		t.Fatal("snippet lost when the videos join was added")
	}

	unwatched, err := s.SearchFTSFiltered(ctx, "ontology", 200, Filter{Watched: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if len(unwatched) != 1 || unwatched[0].VideoID != "b1" {
		t.Fatalf("unwatched FTS = %d hits, want 1 (b1)", len(unwatched))
	}
}

// Legacy rows carry an empty channel_id, so a by-name arm has to exist — but it
// must never widen a by-id match into a by-name one on rows that DO have an id.
func TestFilterChannelNameFallback(t *testing.T) {
	db, s, q := filterFixture(t)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO videos (id, url, channel_id, channel_name, watched)
		VALUES ('legacy','u','','Alpha',0)`); err != nil {
		t.Fatal(err)
	}
	out := make([]float32, 1536)
	out[0] = -0.85
	if err := s.ReplaceVideoChunks(ctx, "legacy",
		IndexMeta{Model: "e5", Dim: 1536, Rev: ChunkRecipeRev},
		[]ChunkRow{{Ordinal: 0, Text: "legacy ontology", StartSeconds: 7}},
		[][]float32{out}); err != nil {
		t.Fatal(err)
	}

	// Asking for Alpha by id AND name finds both the id rows and the legacy one.
	hits, err := s.RetrieveWithinFiltered(ctx, q, 4096, 0,
		Filter{ChannelIDs: []string{"UCA"}, ChannelNames: []string{"Alpha"}})
	if err != nil {
		t.Fatal(err)
	}
	var sawLegacy bool
	for _, h := range hits {
		if h.VideoID == "legacy" {
			sawLegacy = true
		}
		if h.VideoID == "b1" {
			t.Fatal("the name arm leaked a Beta row")
		}
	}
	if !sawLegacy {
		t.Fatal("legacy row with an empty channel_id was not matched by name")
	}
}

func TestFilterEmpty(t *testing.T) {
	if !(Filter{}).Empty() {
		t.Fatal("zero Filter must be Empty")
	}
	cases := map[string]Filter{
		"channel id":   {ChannelIDs: []string{"UCA"}},
		"channel name": {ChannelNames: []string{"Alpha"}},
		"watched":      {Watched: boolPtr(false)},
		"favorite":     {Favorite: boolPtr(true)},
		"category":     {Category: "tech"},
		"after":        {After: "2026-01-01"},
		"before":       {Before: "2026-01-01"},
		"video ids":    {VideoIDs: []string{"v1"}},
	}
	for name, f := range cases {
		if f.Empty() {
			t.Errorf("%s: Empty() = true, want false", name)
		}
	}
	// watched=false is a real constraint, not an absent one. This is the whole
	// reason the field is a pointer.
	if (Filter{Watched: boolPtr(false)}).Empty() {
		t.Fatal("watched=false must not read as an absent filter")
	}
}
