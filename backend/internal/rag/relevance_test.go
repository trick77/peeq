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
		{Hits: keyword, Weight: WeightKeywordStrict},
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

// A non-positive lane weight mutes the lane. It used to be promoted to 1 — full
// confidence — so the one value a caller would reach for to silence a lane was
// the value that made it shout loudest.
func TestFuseWeightedZeroWeightMutesTheLane(t *testing.T) {
	loud := []Hit{{VideoID: "muted", Ordinal: 1}}
	real1 := []Hit{{VideoID: "kept", Ordinal: 1}}

	out := FuseWeighted([]Lane{
		{Hits: loud, Weight: 0},
		{Hits: real1, Weight: WeightKeywordStrict},
	}, 10)
	if len(out) != 1 || out[0].VideoID != "kept" {
		t.Fatalf("a zero-weight lane contributed hits: %+v", out)
	}

	// Negative behaves the same way, rather than flipping the ranking.
	out = FuseWeighted([]Lane{{Hits: loud, Weight: -1}, {Hits: real1, Weight: 1}}, 10)
	if len(out) != 1 || out[0].VideoID != "kept" {
		t.Fatalf("a negative-weight lane contributed hits: %+v", out)
	}

	// FuseRRF still treats every list as equal — it builds lanes at weight 1.
	if got := FuseRRF([][]Hit{loud, real1}, 10); len(got) != 2 {
		t.Errorf("FuseRRF should keep both lists, got %+v", got)
	}
}

func TestKeywordTierWeightsDescendBelowSemantic(t *testing.T) {
	if !(WeightKeywordStrict > WeightKeywordContent && WeightKeywordContent > WeightKeywordAny) {
		t.Errorf("tier weights must descend: %v %v %v",
			WeightKeywordStrict, WeightKeywordContent, WeightKeywordAny)
	}
	// The floor sits below the semantic lane on purpose: a chunk that happens to
	// share one word is worse evidence than one the embedding placed near the
	// question.
	if WeightKeywordAny >= WeightSemantic {
		t.Errorf("the OR floor (%v) must weigh less than the semantic lane (%v)",
			WeightKeywordAny, WeightSemantic)
	}
	// The strict rung still outweighs it, which is the older invariant this
	// ladder must not have broken.
	if WeightKeywordStrict <= WeightSemantic {
		t.Errorf("a literal full match (%v) must outweigh the semantic lane (%v)",
			WeightKeywordStrict, WeightSemantic)
	}
}

// The bug in miniature. A question whose strict tiers match nothing falls
// through to "any one content word", and a chunk matching only that word used
// to enter at FULL keyword confidence — outranking a passage the embedding
// placed close to the question.
func TestOrFloorDoesNotOutrankASemanticHit(t *testing.T) {
	shared := []Hit{{VideoID: "shares-a-word", Ordinal: 1, Text: "a talk about sport"}}
	onTopic := []Hit{{VideoID: "on-topic", Ordinal: 1, Text: "electrolyte replacement", Distance: l2(0.55)}}

	// Flat weighting — what this change removes.
	before := FuseWeighted([]Lane{
		{Hits: shared, Weight: WeightKeywordStrict},
		{Hits: onTopic, Weight: WeightSemantic},
	}, 10)
	if before[0].VideoID != "shares-a-word" {
		t.Fatalf("premise broken: flat weighting already ranks the on-topic hit first (%+v)", before)
	}

	after := FuseWeighted([]Lane{
		{Hits: shared, Weight: WeightKeywordAny},
		{Hits: onTopic, Weight: WeightSemantic},
	}, 10)
	if after[0].VideoID != "on-topic" {
		t.Errorf("the OR floor still outranks a genuine semantic hit: %+v", after)
	}
}

// TestWithinSpreadCutsTheTailNotTheQuery is the "a short list is allowed to be
// short" property. The absolute bound cannot supply it: on a query the library
// covers well every row clears the bound, so something has to say when the
// merely-nearest rows begin.
func TestWithinSpreadCutsTheTailNotTheQuery(t *testing.T) {
	hits := []Hit{
		{VideoID: "a", Ordinal: 1, Distance: l2(0.62)},
		{VideoID: "a", Ordinal: 2, Distance: l2(0.58)}, // still about this
		{VideoID: "b", Ordinal: 1, Distance: l2(0.31)}, // nearest available
		{VideoID: "c", Ordinal: 1, Distance: l2(0.30)},
	}
	got := WithinSpread(hits, SemanticSpread)
	if len(got) != 2 {
		t.Fatalf("kept %d hits, want 2 — the tail past the spread should be gone: %+v", len(got), got)
	}
	if got[1].Ordinal != 2 {
		t.Errorf("kept the wrong second hit: %+v", got[1])
	}
}

// A query whose hits are all equally good keeps all of them: the gate is
// relative, so a tight cluster is never thinned.
func TestWithinSpreadKeepsAnEvenCluster(t *testing.T) {
	hits := []Hit{
		{VideoID: "a", Distance: l2(0.60)},
		{VideoID: "b", Distance: l2(0.59)},
		{VideoID: "c", Distance: l2(0.58)},
	}
	if got := WithinSpread(hits, SemanticSpread); len(got) != 3 {
		t.Errorf("kept %d of 3 hits in a tight cluster", len(got))
	}
}

func TestWithinSpreadDegenerateInputs(t *testing.T) {
	if got := WithinSpread(nil, SemanticSpread); len(got) != 0 {
		t.Errorf("nil in, %d out", len(got))
	}
	hits := []Hit{{Distance: 0.1}, {Distance: 1.4}}
	// A non-positive spread is the opt-out, not a spread of zero that would
	// keep only the best hit.
	if got := WithinSpread(hits, 0); len(got) != 2 {
		t.Errorf("a zero spread should pass everything through, kept %d", len(got))
	}
}

// The Ask ladder keeps EVERY rung that returned rows as its own lane now, rather
// than only the first (see httpapi.retrieveAsk). That stacks weights on a chunk
// several rungs found, so the contract above needs restating for more than two
// lanes: a chunk only the floor found must still not outrank a semantic hit, no
// matter how many keyword lanes are in the fuse.
func TestStackedKeywordLanesDoNotFloatTheFloor(t *testing.T) {
	// The shape the ladder produces on a natural question: the precise rungs match
	// one chunk, the floor matches that one plus a chunk sharing a single word.
	precise := []Hit{{VideoID: "precise", Ordinal: 1, Text: "electrolyte replacement in heat"}}
	floor := []Hit{
		{VideoID: "precise", Ordinal: 1, Text: "electrolyte replacement in heat"},
		{VideoID: "shares-a-word", Ordinal: 1, Text: "a talk about sport"},
	}
	onTopic := []Hit{{VideoID: "on-topic", Ordinal: 1, Text: "salt and cramping", Distance: l2(0.55)}}

	got := FuseWeighted([]Lane{
		{Hits: precise, Weight: WeightKeywordContent},
		{Hits: precise, Weight: WeightKeywordPrefix},
		{Hits: floor, Weight: WeightKeywordAny},
		{Hits: onTopic, Weight: WeightSemantic},
	}, 10)

	// Stacking is allowed to lift the chunk three rungs agreed on — that is more
	// evidence, not less — and it must lift it above the semantic hit.
	if got[0].VideoID != "precise" {
		t.Errorf("the chunk every rung found is not first: %+v", got)
	}
	// But the floor-only chunk appears in exactly ONE lane at the floor weight, so
	// it must still sit below the semantic hit.
	var floorAt, semanticAt = -1, -1
	for i, h := range got {
		switch h.VideoID {
		case "shares-a-word":
			floorAt = i
		case "on-topic":
			semanticAt = i
		}
	}
	if floorAt < 0 || semanticAt < 0 {
		t.Fatalf("expected both the floor and semantic hits in the fuse: %+v", got)
	}
	if floorAt < semanticAt {
		t.Errorf("a chunk sharing one word outranked a semantic hit (%d < %d): %+v",
			floorAt, semanticAt, got)
	}
}

// The regression this constant was raised for, in the shape it was measured in.
//
// On a 137-video library, "transients" produced a vector lane of 40 rows across
// 11 videos spanning L2 0.995 to 1.213, and every video behind it was on topic.
// A 0.12 spread kept 5 of those rows, taking the fused result from 13 videos back
// to 6 — because related passages here cluster at the FAR end of the band, so a
// window measured from the best hit catches a twentieth of the lane rather than
// its better half.
//
// 0.20 reaches 1.195 from a 0.995 best, so it keeps that band's body and still
// trims its extreme edge. That is the intended split rather than a shortfall:
// DefaultMaxDistance decides what is admissible at all, and the spread's job is
// only to say where the merely-nearest rows begin. A spread wide enough to keep
// every row measured here would keep every row on this library, which is a
// constant that has stopped doing anything.
//
// Distances are the real ones, not cosine-derived, since the point is the band's
// observed shape.
func TestWithinSpreadKeepsAFlatFarBand(t *testing.T) {
	hits := []Hit{
		{VideoID: "a", Ordinal: 1, Distance: 0.995},
		{VideoID: "b", Ordinal: 1, Distance: 1.061},
		{VideoID: "c", Ordinal: 1, Distance: 1.114}, // 0.12 cut everything from here
		{VideoID: "d", Ordinal: 1, Distance: 1.168},
		{VideoID: "e", Ordinal: 1, Distance: 1.213}, // the edge 0.20 still trims
	}
	got := WithinSpread(hits, SemanticSpread)
	if len(got) != 4 {
		t.Fatalf("kept %d of 5 rows in the band this constant was measured against, want 4: %+v",
			len(got), got)
	}
	// The row at 1.168 is the regression: a 0.12 spread dropped it, and the videos
	// behind rows like it were on topic.
	if got[3].VideoID != "d" {
		t.Errorf("the row a 0.12 spread would have cut is still missing: %+v", got)
	}
}

// The other half of the same trade: the spread has to keep biting when a query
// DOES have a close best hit, or it has stopped doing anything.
func TestWithinSpreadStillCutsWhenTheBestHitIsClose(t *testing.T) {
	hits := []Hit{
		{VideoID: "a", Ordinal: 1, Distance: 0.70}, // genuinely close
		{VideoID: "b", Ordinal: 1, Distance: 0.85},
		{VideoID: "c", Ordinal: 1, Distance: 1.10}, // merely nearest
		{VideoID: "d", Ordinal: 1, Distance: 1.20},
	}
	got := WithinSpread(hits, SemanticSpread)
	if len(got) != 2 {
		t.Errorf("kept %d rows, want 2 — a close best hit must still cut its tail: %+v",
			len(got), got)
	}
}
