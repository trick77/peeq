package rag

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

// l2 converts a cosine similarity to the L2 distance between two unit vectors,
// so the tests below can be written in the units the calibration comment uses.
func l2(cos float64) float64 { return math.Sqrt(2 - 2*cos) }

func TestFuseWeightedKeepsLiteralMatchesOnTop(t *testing.T) {
	// This is the reported bug in miniature. Searching "electrolytes" returns a
	// genuinely-matching chunk deep in the keyword lane, and an unrelated chunk
	// near the top of the semantic lane — because KNN always returns its k
	// nearest, however far away they are. Under unweighted RRF the unrelated
	// chunk wins: rank 3 scores 1/63 against the real match's 1/65.
	ufo := Hit{VideoID: "ufo", Ordinal: 1, Text: "unidentified aerial phenomena", Distance: l2(0.19)}
	real1 := Hit{VideoID: "attia", Ordinal: 7, Text: "the electrolytes you replace", Snippet: "electrolytes"}

	keyword := []Hit{
		{VideoID: "x", Ordinal: 1}, {VideoID: "x", Ordinal: 2},
		{VideoID: "x", Ordinal: 3}, {VideoID: "x", Ordinal: 4},
		{VideoID: "x", Ordinal: 5}, real1, // rank 5
	}
	semantic := []Hit{
		{VideoID: "y", Ordinal: 1, Distance: l2(0.4)},
		{VideoID: "y", Ordinal: 2, Distance: l2(0.3)},
		{VideoID: "y", Ordinal: 3, Distance: l2(0.25)},
		ufo, // rank 3
	}

	// Unweighted fusion — the behaviour being fixed.
	before := FuseRRF([][]Hit{keyword, semantic}, 10)
	ufoRank, realRank := rankOf(before, "ufo", 1), rankOf(before, "attia", 7)
	if ufoRank > realRank {
		t.Fatalf("premise broken: unweighted fusion already ranks the real match first (ufo=%d real=%d)", ufoRank, realRank)
	}

	// Weighted fusion — a literal match outranks a merely-nearest one.
	after := FuseWeighted([]Lane{
		{Hits: keyword, Weight: WeightKeyword},
		{Hits: semantic, Weight: WeightSemantic},
	}, 10)
	if got, want := rankOf(after, "attia", 7), rankOf(after, "ufo", 1); got > want {
		t.Errorf("literal match ranked %d, behind semantic noise at %d", got, want)
	}
}

func TestFuseWeightedMergesLaneData(t *testing.T) {
	// A chunk found by both lanes should end up with the keyword lane's
	// highlighted snippet AND the vector lane's distance, whichever arrived
	// first — the two lanes each know something the other does not.
	kw := Hit{VideoID: "v", Ordinal: 1, Snippet: "hi" + HighlightStart + "light" + HighlightEnd}
	sem := Hit{VideoID: "v", Ordinal: 1, Distance: 0.4}
	out := FuseWeighted([]Lane{{Hits: []Hit{kw}, Weight: 1}, {Hits: []Hit{sem}, Weight: 1}}, 5)
	if len(out) != 1 {
		t.Fatalf("same chunk from both lanes should fuse to one hit, got %d", len(out))
	}
	if out[0].Distance != 0.4 {
		t.Errorf("distance lost in fusion: %v", out[0].Distance)
	}
	if !strings.Contains(out[0].Snippet, HighlightStart) {
		t.Errorf("highlighted snippet lost in fusion: %q", out[0].Snippet)
	}
}

func rankOf(hits []Hit, videoID string, ordinal int) int {
	for i, h := range hits {
		if h.VideoID == videoID && h.Ordinal == ordinal {
			return i
		}
	}
	return len(hits) + 1
}

func TestSearchFTSHighlightsTheMatchNotTheHead(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u')`); err != nil {
		t.Fatal(err)
	}
	// A realistic chunk: the searched word is far past the 160-character head
	// the old snippet() returned, so the old preview never contained it.
	head := strings.Repeat("filler words about training and recovery ", 12)
	text := head + "the electrolytes you replace during endurance work matter most " + head
	s := NewStore(db)
	ctx := context.Background()
	vec := make([]float32, 1536)
	vec[0] = 1
	rows := []ChunkRow{{Ordinal: 0, Text: text, StartSeconds: 872, TokenCount: 200}}
	if err := s.ReplaceVideoChunks(ctx, "v1", IndexMeta{Model: "e5", Dim: 1536, Rev: ChunkRecipeRev}, rows, [][]float32{vec}); err != nil {
		t.Fatal(err)
	}

	hits, err := s.SearchFTS(ctx, ParseFTSQuery("electrolytes"), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("want 1 hit, got %d", len(hits))
	}
	snip := hits[0].Snippet
	if snip == "" {
		t.Fatal("snippet is empty")
	}
	if !strings.Contains(strings.ToLower(snip), "electrolytes") {
		t.Errorf("snippet does not contain the searched term: %q", snip)
	}
	if !strings.Contains(snip, HighlightStart) || !strings.Contains(snip, HighlightEnd) {
		t.Errorf("snippet is not highlighted: %q", snip)
	}
	// The whole point: the head of the chunk is NOT what gets shown.
	if strings.HasPrefix(snip, text[:40]) {
		t.Errorf("snippet is still the head of the chunk: %q", snip)
	}
}

func TestRetrieveWithinBoundsDistance(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES ('near','u'),('far','u')`); err != nil {
		t.Fatal(err)
	}
	// Two unit vectors: one aligned with the query, one orthogonal to it.
	// Orthogonal is cosine 0, i.e. L2 sqrt(2) ~ 1.414 — comfortably outside
	// the default cutoff and exactly the "unrelated content" case.
	unit := func(i int) []float32 {
		v := make([]float32, 1536)
		v[i] = 1
		return v
	}
	s := NewStore(db)
	ctx := context.Background()
	if err := s.ReplaceVideoChunks(ctx, "near", IndexMeta{Model: "e5", Dim: 1536, Rev: ChunkRecipeRev},
		[]ChunkRow{{Ordinal: 0, Text: "electrolytes", StartSeconds: 1}}, [][]float32{unit(0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceVideoChunks(ctx, "far", IndexMeta{Model: "e5", Dim: 1536, Rev: ChunkRecipeRev},
		[]ChunkRow{{Ordinal: 0, Text: "unidentified aerial phenomena", StartSeconds: 2}}, [][]float32{unit(5)}); err != nil {
		t.Fatal(err)
	}

	// Unbounded: KNN cannot fail — it returns the far row too.
	all, err := s.Retrieve(ctx, unit(0), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unbounded retrieve should return both rows, got %d", len(all))
	}

	// Bounded: the unrelated row is gone, so the caller can honestly report
	// that the library has nothing on the topic.
	within, err := s.RetrieveWithin(ctx, unit(0), 10, DefaultMaxDistance)
	if err != nil {
		t.Fatal(err)
	}
	if len(within) != 1 || within[0].VideoID != "near" {
		t.Fatalf("bounded retrieve should keep only the near row, got %+v", within)
	}

	// A query about nothing in the library returns nothing at all.
	none, err := s.RetrieveWithin(ctx, unit(900), 10, DefaultMaxDistance)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("query unrelated to every chunk should return no hits, got %+v", none)
	}
}
