package httpapi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/mcpserver"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

// slowFloorRag lets every AND rung through and holds the OR floor until its ctx
// ends, then fails the way sqlite does on an interrupt: an error that is not
// ctx.Err(). That is the production shape — the floor ran 23 s and took the
// whole search down with it.
type slowFloorRag struct {
	RagStore
}

func (f slowFloorRag) SearchFTSFiltered(ctx context.Context, match string, n int, flt rag.Filter) ([]rag.Hit, error) {
	if strings.Contains(match, " OR ") {
		<-ctx.Done()
		return nil, errors.New("sqlite3: interrupted")
	}
	return f.RagStore.SearchFTSFiltered(ctx, match, n, flt)
}

// slowFloorServer seeds v1, which the keyword rungs find, and v2, which only
// the vector lane finds.
func slowFloorServer(t *testing.T) *server {
	t.Helper()
	deps, _, ragStore := searchTestDepsWithStores(t)
	for _, v := range []videos.Video{{ID: "v1", URL: "u1", Title: "Cramp"}, {ID: "v2", URL: "u2", Title: "Hydration"}} {
		if err := deps.Videos.Upsert(v); err != nil {
			t.Fatal(err)
		}
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{{Ordinal: 0, Text: "the electrolytes you replace matter", StartSeconds: 10}})
	seedChunks(t, ragStore, "v2", []rag.ChunkRow{{Ordinal: 0, Text: "drink before the long ride", StartSeconds: 20}})
	return &server{
		rag:      slowFloorRag{RagStore: ragStore},
		videos:   deps.Videos,
		embedder: &fakeEmbedder{vec: dim1536(1.0)},
	}
}

func resultIDs(res mcpserver.SearchResult) []string {
	var ids []string
	for _, v := range res.Videos {
		ids = append(ids, v.Video.ID)
	}
	return ids
}

// The floor gets half of what is left of the tool deadline; the vector lane runs
// beside the ladder, so a floor that times out costs its own rows and nothing
// else.
func TestMCPSearchSurvivesASlowFloor(t *testing.T) {
	testee := slowFloorServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	start := time.Now()
	res, err := mcpLibrary{s: testee}.Search(ctx, mcpserver.SearchQuery{Text: "electrolytes replace", Videos: 10})
	took := time.Since(start)

	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if took >= 1900*time.Millisecond {
		t.Errorf("took %s: the floor ate the whole deadline", took)
	}
	ids := resultIDs(res)
	if !slices.Contains(ids, "v1") || !slices.Contains(ids, "v2") {
		t.Errorf("videos = %v, want v1 (keyword) and v2 (semantic)", ids)
	}
	if !res.KeywordPartial || res.KeywordFailed || res.SemanticDegraded {
		t.Errorf("notes: partial=%v failed=%v semanticDegraded=%v, want partial only",
			res.KeywordPartial, res.KeywordFailed, res.SemanticDegraded)
	}
}

// Only the floor would have matched: the semantic hits still come back, not
// "keyword and semantic lanes both failed".
func TestMCPSearchSlowFloorWithNoKeywordHits(t *testing.T) {
	testee := slowFloorServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	res, err := mcpLibrary{s: testee}.Search(ctx, mcpserver.SearchQuery{Text: "electrolytes zirconium", Videos: 10})
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if ids := resultIDs(res); len(ids) != 2 {
		t.Errorf("videos = %v, want the two semantic hits", ids)
	}
}

// A caller giving up (loom's Stop) is not the ladder's budget running out. The
// rung reads err, and an empty search reports the cancel.
func TestAskLanesOuterCancelIsNotALadderTimeout(t *testing.T) {
	testee := slowFloorServer(t)
	testee.embedder = nil
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)

	_, diag := testee.askLanes(ctx, "electrolytes zirconium", "", rag.Filter{}, nil)

	if diag.ftsTimedOut || !diag.ftsFailed {
		t.Errorf("timedOut=%v failed=%v, want a failure: the caller cancelled", diag.ftsTimedOut, diag.ftsFailed)
	}
	last := diag.rungs[len(diag.rungs)-1]
	if !strings.Contains(last, "=err/") {
		t.Errorf("last rung %q, want err", last)
	}

	ctx2, cancel2 := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel2)
	if _, err := (mcpLibrary{s: testee}).Search(ctx2, mcpserver.SearchQuery{Text: "electrolytes zirconium", Videos: 10}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// Ask (SSE) and /api/search carry no deadline: the floor is capped on its own,
// so a slow floor degrades there too instead of holding the reader.
func TestAskLanesCapsTheFloorWithoutADeadline(t *testing.T) {
	testee := slowFloorServer(t)
	testee.floorBudget = 200 * time.Millisecond

	start := time.Now()
	lanes, diag := testee.askLanes(t.Context(), "electrolytes replace", "", rag.Filter{}, nil)

	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("took %s with a 200ms ladder cap", took)
	}
	if !diag.ftsTimedOut || diag.ftsFailed {
		t.Errorf("timedOut=%v failed=%v, want a ladder timeout", diag.ftsTimedOut, diag.ftsFailed)
	}
	if last := diag.rungs[len(diag.rungs)-1]; !strings.HasPrefix(last, "w0.4=timeout/") {
		t.Errorf("last rung %q, want the floor marked timeout", last)
	}
	if !diag.semRaw.ran || diag.rawLane < 0 || len(lanes) < 2 {
		t.Errorf("semantic ran=%v rawLane=%d lanes=%d, want keyword + semantic", diag.semRaw.ran, diag.rawLane, len(lanes))
	}
}

// A far deadline does not loosen the cap: half of five minutes would hand the
// floor the 23 s it took in production.
func TestAskLanesCapsTheFloorUnderAFarDeadline(t *testing.T) {
	testee := slowFloorServer(t)
	testee.floorBudget = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()

	start := time.Now()
	_, diag := testee.askLanes(ctx, "electrolytes replace", "", rag.Filter{}, nil)
	if took := time.Since(start); took > 2*time.Second || !diag.ftsTimedOut {
		t.Errorf("took %s timedOut=%v, want the floor cut at its cap", took, diag.ftsTimedOut)
	}
}

// slowAndRag makes every AND rung slower than the floor budget. Those rungs
// are the precise matches; the budget is the floor's alone.
type slowAndRag struct {
	RagStore
}

func (f slowAndRag) SearchFTSFiltered(ctx context.Context, match string, n int, flt rag.Filter) ([]rag.Hit, error) {
	if !strings.Contains(match, " OR ") {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-ctx.Done():
			return nil, errors.New("sqlite3: interrupted")
		}
	}
	return f.RagStore.SearchFTSFiltered(ctx, match, n, flt)
}

func TestAskLanesLeavesTheAndRungsUncapped(t *testing.T) {
	testee := slowFloorServer(t)
	testee.rag = slowAndRag{RagStore: testee.rag.(slowFloorRag).RagStore}
	testee.floorBudget = 100 * time.Millisecond

	lanes, diag := testee.askLanes(t.Context(), "electrolytes replace", "", rag.Filter{}, nil)
	if diag.ftsTimedOut || diag.ftsFailed {
		t.Errorf("rungs = %v: a slow AND rung was cut by the floor's budget", diag.rungs)
	}
	if len(lanes) < 2 {
		t.Errorf("lanes = %d, want the keyword rungs kept", len(lanes))
	}
}

// A timed-out floor with nothing found and no vector lane is a timeout to the
// caller ("narrow the query"), not an unreadable index.
func TestMCPSearchSlowFloorWithoutSemanticReportsATimeout(t *testing.T) {
	testee := slowFloorServer(t)
	testee.embedder = nil
	testee.floorBudget = 100 * time.Millisecond

	_, err := mcpLibrary{s: testee}.Search(t.Context(), mcpserver.SearchQuery{Text: "electrolytes zirconium", Videos: 10})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want a deadline error", err)
	}
}

// With almost nothing left of the deadline the floor is skipped, not started
// and cut at once.
func TestAskLanesSkipsTheFloorWithNoTimeLeft(t *testing.T) {
	testee := slowFloorServer(t)
	testee.embedder = nil
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()

	_, diag := testee.askLanes(ctx, "electrolytes replace", "", rag.Filter{}, nil)
	last := diag.rungs[len(diag.rungs)-1]
	if !diag.ftsTimedOut || last != "w0.4=skipped/0ms" {
		t.Errorf("timedOut=%v rungs=%v, want the floor skipped", diag.ftsTimedOut, diag.rungs)
	}
}

// A timed-out floor over a BROKEN vector lane is both lanes failing, not a
// query to narrow.
func TestMCPSearchSlowFloorWithABrokenVectorLaneFails(t *testing.T) {
	testee := slowFloorServer(t)
	testee.embedder = &fakeEmbedder{err: errors.New("embeddings are down")}
	testee.floorBudget = 100 * time.Millisecond

	_, err := mcpLibrary{s: testee}.Search(t.Context(), mcpserver.SearchQuery{Text: "electrolytes zirconium", Videos: 10})
	if !errors.Is(err, errSearchLanesFailed) {
		t.Errorf("err = %v, want errSearchLanesFailed", err)
	}
}

// The panel sums the stages, so the vector half bills only the wait it added
// past the ladder: fts + embed + vector stays the wall clock.
func TestSemanticWaitSpansAreAdditive(t *testing.T) {
	t0 := time.Now()
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	for _, tc := range []struct {
		name                        string
		embedEnd, vectorEnd, ladder int
		embed, vector               int64
	}{
		{"done before the ladder", 100, 300, 500, 0, 0},
		{"KNN ran past the ladder", 100, 800, 500, 0, 300},
		{"embed ran past the ladder", 700, 900, 500, 200, 200},
	} {
		r := semanticResult{start: t0, embedEnd: at(tc.embedEnd), vectorEnd: at(tc.vectorEnd)}
		embed, vector := r.waitSpans(at(tc.ladder))
		if embed != tc.embed || vector != tc.vector {
			t.Errorf("%s: embed=%d vector=%d, want %d/%d", tc.name, embed, vector, tc.embed, tc.vector)
		}
		// What each really took is logged whatever the overlap.
		tookEmbed, tookVector := r.tookSpans()
		if tookEmbed != int64(tc.embedEnd) || tookVector != int64(tc.vectorEnd-tc.embedEnd) {
			t.Errorf("%s: took embed=%d vector=%d", tc.name, tookEmbed, tookVector)
		}
	}
}

// The relaxation pass reuses the vectors and still runs its KNN beside the
// ladder; neither pass bills a negative span.
func TestAskLanesReusesVectorsAcrossPasses(t *testing.T) {
	testee := slowFloorServer(t)
	testee.floorBudget = 200 * time.Millisecond
	emb := &countingEmbedder{fakeEmbedder: fakeEmbedder{vec: dim1536(1.0)}}
	testee.embedder = emb

	var qv queryVectors
	_, first := testee.askLanes(t.Context(), "electrolytes replace", "", rag.Filter{}, &qv)
	_, second := testee.askLanes(t.Context(), "electrolytes replace", "", rag.Filter{}, &qv)

	if emb.calls != 1 {
		t.Errorf("embedder called %d times, want 1: the second pass reuses the vectors", emb.calls)
	}
	for _, d := range []askDiag{first, second} {
		if d.vectorMs < 0 || !d.semRaw.ran {
			t.Errorf("vectorMs=%d ran=%v", d.vectorMs, d.semRaw.ran)
		}
	}
	if second.embedMs != 0 {
		t.Errorf("second pass embedMs = %d, want 0: nothing was embedded", second.embedMs)
	}
}

type countingEmbedder struct {
	fakeEmbedder
	calls int
}

func (c *countingEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	c.calls++
	return c.fakeEmbedder.Embed(ctx, inputs)
}
