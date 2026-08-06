package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

// RagStore is the slice of rag.Store the handlers use — declared here at the
// consumer, following DownloadsRunner, PlaybackStore and ShareLinkStore.
// *rag.Store satisfies it. Unlike those two this is a genuine subset: the store
// also owns the chunk write/delete path, which the summarize worker drives and
// no handler touches.
//
// HasChunks is the one method here that is not the search endpoint's: the
// ignore handler asks it whether deleting a video row would take a search
// index with it. It reads, like the rest.
//
// Note this does NOT let httpapi drop its rag import. rag.Hit is the return
// type, and the query builders (rag.ParseFTSQuery, rag.BuildFTSQueries) are
// package-level FUNCTIONS the handler calls directly — an interface cannot
// capture either. The gain here is testability: the
// search endpoint's degraded-path branches can now be driven by a fake instead
// of by a real sqlite-vec store.
//
// Same typed-nil caveat as the other two: the handler nil-checks the INTERFACE,
// so a (*rag.Store)(nil) placed in it would panic rather than return the
// documented 503.
type RagStore interface {
	SearchFTS(ctx context.Context, match string, n int) ([]rag.Hit, error)
	Retrieve(ctx context.Context, queryEmbedding []float32, k int) ([]rag.Hit, error)
	RetrieveWithin(ctx context.Context, queryEmbedding []float32, k int, maxDistance float64) ([]rag.Hit, error)
	// The *Filtered pair narrows retrieval to the videos a question named — a
	// channel, "unwatched", a date. Both take rag.Filter{} to mean the whole
	// library, so they are supersets of the two above rather than a second way
	// of doing the same thing.
	SearchFTSFiltered(ctx context.Context, match string, n int, f rag.Filter) ([]rag.Hit, error)
	RetrieveWithinFiltered(ctx context.Context, queryEmbedding []float32, k int, maxDistance float64, f rag.Filter) ([]rag.Hit, error)
	// CountVideos answers an inventory question in SQL rather than leaving the
	// model to estimate it from the excerpts it happened to be shown.
	CountVideos(ctx context.Context, f rag.Filter) (rag.LibraryCount, error)
	HasChunks(ctx context.Context, videoID string) (bool, error)
	// ChunkStats backs the player's Search index card: what the index actually
	// holds for one video, as opposed to HasChunks' yes/no.
	ChunkStats(ctx context.Context, videoID string) ([]rag.KindCount, error)
}

// queryVectors memoizes one question's embedding across the retrieval passes a
// single request may make. Embedding is the one network call in retrieval, and
// it depends on the question alone — not on which videos are being searched — so
// a relaxation pass that widens the filter has nothing to re-embed. err is
// carried too: a failed embed must degrade to FTS-only on both passes rather
// than being retried once per pass.
type queryVectors struct {
	done bool
	vecs [][]float32
	err  error
}

// searchMatch is one hit within a search result's video, in the shape the
// frontend player uses to jump to a timestamp.
//
// Snippet carries rag.HighlightStart/End around matched terms when the hit came
// from the keyword lane; the UI splits on those and renders <mark>.
type searchMatch struct {
	StartSeconds int     `json:"start_seconds"`
	Snippet      string  `json:"snippet"`
	Distance     float64 `json:"distance"`
	Kind         string  `json:"kind"`
}

// searchResult groups every retrieved chunk for a single video, in
// best-distance order (the order its first/closest chunk was seen in).
type searchResult struct {
	Video   videoDTO      `json:"video"`
	Matches []searchMatch `json:"matches"`
}

// admits reports whether a hit is a different moment from the ones already
// taken for this video, so the same seconds are not shown twice under two chunk
// kinds. Hits arrive best-first, so the one already kept is the better-ranked
// rendering of that moment.
//
// A summary hit is exempt: it describes the whole video rather than a point in
// it, carries no timestamp, and is badged differently — suppressing it because
// some transcript hit landed near 0s would drop genuinely distinct information.
func (r *searchResult) admits(h rag.Hit) bool {
	if h.Kind == rag.KindSummary {
		for _, m := range r.Matches {
			if m.Kind == rag.KindSummary {
				return false
			}
		}
		return true
	}
	for _, m := range r.Matches {
		if m.Kind == rag.KindSummary {
			continue
		}
		if abs(m.StartSeconds-h.StartSeconds) < minMomentGapSeconds {
			return false
		}
	}
	return true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Search modes. They differ in what they retrieve with, not in what they
// retrieve from — both read the same chunks.
//
// Find is a literal full-text search: FTS5 only, operators honoured, bm25
// ranking, no embedding request and no model call. It is instant and free, and
// it can genuinely return nothing, which is the correct answer when the words
// aren't there.
//
// Ask is the semantic mode: vector KNN with a distance bound, plus the keyword
// lane as recall support, fused with the keyword lane weighted higher.
const (
	searchModeFind = "find"
	searchModeAsk  = "ask"
)

// These four bound the SEARCH response. Ask mode no longer renders through it —
// the Ask view draws its moments from the answer's citations (see
// handleAnswer) — so in practice they now shape Find, plus any client calling
// /api/search?mode=ask directly.
//
// Note what defaultSearchK is and is not: a ceiling, never a target. A query
// with six good chunks must return six moments. Padding it to twenty is the
// bug the lane bounds in rag/hybrid.go exist to prevent.
const (
	// defaultSearchK caps how many moments the response carries, counted after
	// the per-video cap below rather than before it — capping the candidate
	// list instead would let one chatty video consume the whole budget and hide
	// every other video that mentioned the topic, which is the very thing
	// maxMatchesPerVideo exists to prevent.
	defaultSearchK = 20
	// searchCandidates is how many rows the KEYWORD lane retrieves, and how deep
	// the fused list runs. It has to sit well above defaultSearchK for the
	// spread to have anything to work with: 200 chunks from one video still
	// leave room for other videos below them.
	searchCandidates = 200
	// semanticCandidates is the vector lane's own, much shallower depth. The two
	// lanes degrade differently with depth: an FTS row at position 150 still
	// literally contains the searched terms, while a KNN row at position 150 is
	// noise by construction — "nearest" is relative, so the tail of a KNN result
	// is whatever was least distant among the irrelevant. Handing the vector
	// lane the keyword lane's depth gave that tail 160 chances to place into a
	// twenty-slot response.
	semanticCandidates = 40
	// maxMatchesPerVideo caps how many moments one video contributes, so a
	// long video cannot crowd out the rest of the library.
	maxMatchesPerVideo = 4
	// minMomentGapSeconds is how far apart two moments from the same video must
	// be to count as different moments.
	//
	// A chapter chunk contains the transcript of its own span, so the same
	// sentence is indexed twice: once in a ~600-token transcript window and
	// again inside the chapter covering it. Both match the same query, and
	// without this a video's four slots fill with near-duplicate pairs — two
	// renderings of one moment, crowding out the genuinely different places the
	// topic came up.
	minMomentGapSeconds = 30
	// keywordVideoTarget is how many DISTINCT VIDEOS the keyword lane has to offer
	// before the Ask ladder stops relaxing. See retrieveAsk: the ladder used to
	// stop at the first rung returning any row at all, which let a rung matching
	// one chunk in one video pass for an answered query and kept the recall floor
	// from ever running.
	//
	// It IS what the answer's breadth pass can spend (eight) — below that the lane
	// cannot fill the evidence set even if every video it found were used — so it
	// is spelled as that constant rather than as another 8 that would silently stop
	// meaning the same thing the moment the breadth pass is retuned.
	keywordVideoTarget = answerBreadthSources
)

// handleSearch answers GET /api/search?q=&k=&mode=: blank q short-circuits to
// an empty result set without ever calling the embedder (cheap, and avoids
// spending an embed call on a no-op query).
//
// mode selects the retrieval strategy — "find" (default) is FTS5 only, "ask"
// adds distance-bounded vector search. Results are grouped by video either way,
// so both modes render through one component.
func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	mode := searchModeFind
	if r.URL.Query().Get("mode") == searchModeAsk {
		mode = searchModeAsk
	}
	if q == "" {
		writeJSON(w, map[string]any{"results": []any{}, "mode": mode})
		return
	}
	// FTS lives in the rag store and needs no external service; it is the
	// floor. The embedder (semantic) is optional and best-effort.
	if s.rag == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "search is not configured")
		return
	}

	k := defaultSearchK
	if raw := r.URL.Query().Get("k"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			k = n
		}
	}

	var hits []rag.Hit
	if mode == searchModeAsk {
		hits = s.retrieveAsk(r, q)
	} else {
		hits = s.retrieveFind(r, q)
	}

	order := make([]string, 0)
	byVideo := make(map[string]*searchResult)
	// k budgets the moments actually emitted, so a video that hits
	// maxMatchesPerVideo yields the rest of the budget to the videos below it
	// instead of swallowing it.
	emitted := 0
	for _, h := range hits {
		if emitted >= k {
			break
		}
		g, ok := byVideo[h.VideoID]
		if !ok {
			v, err := s.videos.Get(h.VideoID)
			if err != nil || v == nil {
				continue
			}
			g = &searchResult{Video: toVideoDTO(v)}
			byVideo[h.VideoID] = g
			order = append(order, h.VideoID)
		}
		if len(g.Matches) >= maxMatchesPerVideo || !g.admits(h) {
			continue
		}
		emitted++
		g.Matches = append(g.Matches, searchMatch{
			StartSeconds: h.StartSeconds,
			Snippet:      matchSnippet(h),
			Distance:     h.Distance,
			Kind:         h.Kind,
		})
	}

	out := make([]*searchResult, 0, len(order))
	for _, id := range order {
		out = append(out, byVideo[id])
	}
	writeJSON(w, map[string]any{"results": out, "mode": mode})
}

// retrieveFind runs the keyword lane alone, honouring FTS5 operators. It makes
// no network call at all, so it cannot degrade — and when the words are not in
// the library it correctly returns nothing.
func (s *server) retrieveFind(r *http.Request, q string) []rag.Hit {
	match := rag.ParseFTSQuery(q)
	if match == "" {
		return nil
	}
	hits, err := s.rag.SearchFTS(r.Context(), match, searchCandidates)
	if err != nil {
		// A malformed expression should be impossible (ParseFTSQuery re-emits
		// from recognized tokens), so this is a real fault worth logging rather
		// than a routine miss.
		slog.Warn("search: find lane failed", "err", err, "match", match)
		return nil
	}
	return hits
}

// retrieveAsk runs both lanes and fuses them, weighted so literal matches beat
// merely-nearest ones. The vector lane is distance-bounded: without that it
// returns k rows for any query whatsoever, which is why an unrelated question
// used to come back full of confident nonsense.
// retrieveAsk fuses the lanes askLanes built. A caller that needs to know WHICH
// lane a hit came from — the answer's coverage list does, so it can keep the
// recall floor out — uses askLanes directly.
// retrieveAsk keeps the raw question and nothing else. The query-understanding
// step belongs to /api/search/answer, which streams and can tell the reader that
// a pre-step is running; this endpoint answers a direct API caller with JSON and
// would only pay the latency without anywhere to report it. Such a call logs
// understand=skipped, so the two paths are told apart in the log rather than
// guessed at.
// This endpoint also applies no filter: the structured half of a question is
// extracted by the same understanding step it skips, so there is nothing here to
// resolve a channel name or a "unwatched" from. A JSON caller that wants a
// narrowed search has /api/videos' own filters for that.
func (s *server) retrieveAsk(r *http.Request, q string) []rag.Hit {
	lanes, diag := s.askLanes(r, q, "", rag.Filter{}, nil)
	fused := rag.FuseWeighted(lanes, searchCandidates)
	diag.log(q, fused)
	return fused
}

// askLanes builds every lane one Ask retrieval fuses, and returns the diagnostic
// alongside them. The caller logs it: handleAnswer waits until it knows which
// excerpts the model was actually given, which is the only number that says
// whether a lane changed what was READ rather than merely what was found.
//
// topic is the question with its framing stripped (see understand.go), or "" for
// no second vector lane.
//
// filter is the structured half of the same question — the channel it named, the
// "unwatched" in it — already resolved to ids and validated (see
// resolve_channel.go). It narrows EVERY lane, keyword and vector alike, because a
// reader who asked about one channel does not want the recall floor answering
// from another. rag.Filter{} means the whole library, which is what every caller
// but handleAnswer passes.
//
// qv carries the embedded query across calls. A relaxation pass re-runs
// retrieval with the filter dropped, and it must not pay a second embedding
// round-trip to ask the same question of a wider set — the vectors do not depend
// on the filter. nil means "embed and discard", which is what a caller that runs
// once wants.
func (s *server) askLanes(r *http.Request, q, topic string, filter rag.Filter, qv *queryVectors) ([]rag.Lane, askDiag) {
	lanes := make([]rag.Lane, 0, 6)

	// Keyword lane, relaxed in steps: a natural question ANDs its function words
	// and matches nothing, so fall through to content-terms-only, to prefixes, and
	// finally to OR.
	//
	// The ladder used to stop at the first rung returning ANY row, and "any" is far
	// too weak a test of whether a rung has answered. Measured: "what are
	// transients and what material do we have on them?" puts every function word in
	// the strict rung (0 rows), falls to "transients" AND "material" — and that
	// matches exactly ONE chunk in ONE video. The ladder declared victory there, so
	// the floor that finds 56 videos never ran, and Ask answered from a single
	// keyword chunk while the plain keyword search beside it showed six videos.
	//
	// So a rung counts as having answered when it offers enough DISTINCT VIDEOS to
	// be evidence, not when it offers a row. Every rung that runs is kept as its
	// own lane at its own weight, and FuseWeighted dedups on video and ordinal — so
	// a chunk two rungs both found outscores one only the floor found, and a
	// precise query's precise chunk stays on top with the floor's videos underneath
	// it rather than in place of it.
	//
	// Be precise about what this costs, because it is not free. Only a rung that
	// reaches keywordVideoTarget on its own stops the descent — so a question whose
	// precise rung matches, say, three genuinely relevant videos now runs the
	// prefix and floor rungs too, and the breadth pass in chooseExcerpts spends
	// five of its eight slots on floor videos that merely share one prefixed word.
	// The precise videos keep the top of the ranking and the depth pass, so the
	// evidence is still mostly theirs (7 of 12 excerpts in that shape), but they no
	// longer have the set to themselves the way they did before.
	//
	// Taken deliberately, in this direction: a question the library answers from
	// three videos is not well served by being told about three videos when the
	// reader can see six in the search box next to it. The failure being fixed —
	// answering from one chunk — was far worse than the dilution being accepted.
	// Widening the bar is what to revisit if focused answers turn out to suffer.
	diag := askDiag{topic: topic, rawLane: -1, topicLane: -1}
	ftsStart := time.Now()
	videosSeen := make(map[string]bool)
	for _, tier := range rag.BuildFTSQueries(q) {
		hits, err := s.rag.SearchFTSFiltered(r.Context(), tier.Match, searchCandidates, filter)
		if err != nil {
			slog.Warn("search: FTS degraded", "err", err)
			break
		}
		if len(hits) == 0 {
			continue
		}
		// The rung decides how much its lane counts: a query that only matched
		// once relaxed to "any one content word" is a recall floor, not evidence.
		// The weight rides on the rung itself, since the ladder skips rungs that
		// would repeat one another.
		lanes = append(lanes, rag.Lane{Hits: hits, Weight: tier.Weight})
		for _, h := range hits {
			videosSeen[h.VideoID] = true
		}
		diag.rungs = append(diag.rungs,
			fmt.Sprintf("w%.1f=%dh/%dv", tier.Weight, len(hits), distinctVideos(hits)))
		if len(videosSeen) >= keywordVideoTarget {
			break
		}
	}

	diag.ftsMs = time.Since(ftsStart).Milliseconds()

	// BOTH vector queries go out in ONE embedding request. The topic lane costs a
	// lane, not a round-trip: Embed already takes a slice, and a second call would
	// put its own network latency in front of the first byte to no purpose.
	if s.embedder != nil {
		inputs := []string{q}
		if topic != "" {
			inputs = append(inputs, topic)
		}
		embedStart := time.Now()
		var vecs [][]float32
		var err error
		if qv != nil && qv.done {
			// Second pass over the same question: reuse rather than re-embed.
			// Recorded as 0ms, which is what it cost.
			vecs, err = qv.vecs, qv.err
		} else {
			vecs, err = s.embedder.Embed(r.Context(), inputs)
			if qv != nil {
				qv.done, qv.vecs, qv.err = true, vecs, err
			}
			diag.embedMs = time.Since(embedStart).Milliseconds()
		}
		if err != nil {
			// Semantic unavailable (endpoint down/misconfigured); fall back to
			// FTS-only rather than failing the whole search.
			slog.Warn("search: semantic degraded, using FTS only", "err", err)
		} else {
			if len(vecs) > 0 {
				if lane, ok := s.semanticLane(r, vecs[0], rag.WeightSemantic, filter, &diag.semRaw); ok {
					diag.rawLane = len(lanes)
					lanes = append(lanes, lane)
				}
			}
			// A reply too short to hold the second vector is a misbehaving
			// endpoint, not a reason to fail: the raw lane is already in, so the
			// topic lane simply does not run and the log says it returned nothing.
			if topic != "" && len(vecs) > 1 {
				if lane, ok := s.semanticLane(r, vecs[1], rag.WeightSemanticTopic, filter, &diag.semTopic); ok {
					diag.topicLane = len(lanes)
					lanes = append(lanes, lane)
				}
			}
		}
	}

	diag.retrievalMs = time.Since(ftsStart).Milliseconds()
	return lanes, diag
}

// semanticLane runs one vector query and records what the two bounds did to it.
// Both lanes go through here so the raw and topic lanes cannot drift apart in
// how they are bounded — which matters more than usual, because comparing their
// distance ranges on the same question is the measurement that says whether
// DefaultMaxDistance and SemanticSpread still hold once a rewritten query is in
// play. They were calibrated against raw questions only.
func (s *server) semanticLane(r *http.Request, vec []float32, weight float64, filter rag.Filter, d *semLaneDiag) (rag.Lane, bool) {
	// The filter is applied INSIDE the KNN, not to its output — see
	// RetrieveWithinFiltered. That is what keeps semanticCandidates meaningful
	// under a filter: 40 nearest chunks among the ones the reader asked about,
	// rather than 40 nearest overall of which a narrow channel owns none.
	hits, err := s.rag.RetrieveWithinFiltered(r.Context(), vec, semanticCandidates, s.searchMaxDistance, filter)
	if err != nil {
		slog.Warn("search: semantic retrieve degraded", "err", err)
		d.failed = true
		return rag.Lane{}, false
	}
	d.ran = true
	if len(hits) == 0 {
		return rag.Lane{}, false
	}
	// The absolute bound says "not about anything"; the spread says "not about
	// THIS, given what this query actually found". Both are needed — on a query
	// the library covers well every row clears the absolute bound, and the spread
	// is the only thing that can tell the sixth genuinely relevant chunk from the
	// seventh merely-nearest one.
	//
	// A negative searchMaxDistance is the documented opt-out from bounding the
	// vector lane, so it opts out of BOTH: an operator who asks for unbounded KNN
	// gets unbounded KNN, not a floor they cannot see in any setting.
	d.bounded, d.boundedVideos = len(hits), distinctVideos(hits)
	d.nearest, d.farthest = hits[0].Distance, hits[len(hits)-1].Distance
	if s.searchMaxDistance > 0 {
		hits = rag.WithinSpread(hits, rag.SemanticSpread)
	}
	d.kept, d.keptVideos = len(hits), distinctVideos(hits)
	return rag.Lane{Hits: hits, Weight: weight}, true
}

// askDiag is what one Ask retrieval did, gathered so it can be logged as a
// single line.
//
// It exists because this hybrid is genuinely hard to reason about from the
// outside, and reasoning about it from the outside is exactly how it went wrong:
// three rounds of tuning were aimed at the keyword lane because fts_chunks can be
// read with the sqlite CLI, while vec_chunks needs the sqlite-vec module that is
// compiled into this binary and cannot. The keyword side got measured because it
// was measurable, not because it was the problem.
//
// So the vector lane's numbers are the point of this: how many rows the KNN
// returned inside the distance bound, how many the spread then kept, and the
// distances at both ends. Those say whether the embedding already finds the
// library's coverage and is merely outvoted at fusion — WeightSemantic is 0.6
// against a strict rung's 1.0 — or whether it is weak because the raw question,
// conversational framing and all, is what gets embedded.
// semLaneDiag is one vector lane's numbers. There are two lanes now — the raw
// question and its stripped topic — and they are recorded separately on purpose:
// the totals cannot say whether the rewrite contributed anything, and "did the
// rewrite help or hurt" is the only question this whole step has to answer.
type semLaneDiag struct {
	// ran distinguishes a lane that returned nothing from a lane that never
	// ran at all. Without it, "no topic lane" and "a topic lane the bound
	// emptied" read identically, and they mean opposite things.
	ran bool
	// failed is the vector store erroring, which is a THIRD state: without it a
	// broken RetrieveWithin prints the same "-" as a lane that never ran, and the
	// log would read as "no topic lane" on the one occasion it matters most.
	failed                 bool
	bounded, boundedVideos int
	kept, keptVideos       int
	nearest, farthest      float64
}

func (d semLaneDiag) String() string {
	if d.failed {
		return "err"
	}
	if !d.ran {
		return "-"
	}
	// A lane that ran and matched nothing has no distances to report. Printing
	// the zero values as "0.000..0.000" would put a measurement in the field
	// that was never taken — and this field is precisely the recalibration data
	// for DefaultMaxDistance, so a fabricated 0.000 is the worst thing it could
	// say.
	if d.bounded == 0 {
		return "0h/0v"
	}
	return fmt.Sprintf("%dh/%dv→%dh/%dv %.3f..%.3f",
		d.bounded, d.boundedVideos, d.kept, d.keptVideos, d.nearest, d.farthest)
}

type askDiag struct {
	rungs    []string
	semRaw   semLaneDiag
	semTopic semLaneDiag

	// The query-understanding step, filled by the caller that ran it.
	topic        string
	intent       string
	understand   string
	understandMs int64

	// The structured half of the question, also filled by the caller.
	//
	// All four are needed to tell apart failures that look identical from
	// outside. filters is what was APPLIED; filtersDropped is what the model
	// produced and Go refused; unresolved is a channel the library does not
	// have; relaxed says the filter found nothing and was dropped. A search that
	// quietly returned the whole library could be any of the last three, and
	// they call for different fixes — a prompt change, a resolver change, or
	// nothing at all.
	filters        string
	filtersDropped []string
	unresolved     []string
	relaxed        bool

	embedMs, ftsMs, retrievalMs int64

	// excerpts is the attribution the caller fills in once it knows which
	// passages the model was actually handed: the total, and how many of them
	// each lane family had found. It is the only field that separates "the
	// rewrite changed retrieval" from "the rewrite changed what was READ", and
	// those come apart often — the vector lanes are outranked by three keyword
	// rungs, so a lane can win rows and still lose every excerpt slot.
	//
	// attributed separates "no lane contributed an excerpt" from "excerpts were
	// never looked at here" — /api/search?mode=ask has no excerpts to attribute,
	// and four zeroes would read as the lanes having contributed nothing.
	attributed                      bool
	excerpts                        int
	fromRaw, fromTopic, fromKeyword int
	// fromFloor is the OR rung's own count, held apart from fromKeyword.
	//
	// The rungs were counted as one family because they overlap by construction,
	// and that is still right for the three above the floor. The floor is not one
	// of them: it matches ANY ONE content word, so "bike" alone qualifies a
	// passage, and a lane that means "shares a word with the question" is not
	// evidence the way "contains every word the reader typed" is.
	//
	// Merged into the family, an excerpt set assembled entirely from the floor
	// printed the same kw=9 as one the strict rung found — so the log could not
	// tell an answer built on evidence from an answer built on a net. That
	// distinction is what search_handlers.go's own ladder comment predicts will
	// matter ("the breadth pass spends five of its eight slots on floor videos"),
	// and it was the one number missing when a bad answer had to be diagnosed.
	fromFloor int
	// Chunk kinds among the passages actually read. A set that is mostly summary
	// chunks failed differently from one that is mostly transcript: a summary is
	// one vector averaging a whole video, so it matches broad questions loosely
	// and answers them vaguely. The totals cannot say which happened.
	excTranscript, excSummary, excChapter int
	// chosenIDs names the passages themselves, in the order the model read them,
	// for the Debug line beside the Info one.
	//
	// The counts above say what SHAPE the excerpt set had; only the ids say WHICH
	// passages, which is what has to be read back out of the database when an
	// answer makes a claim the videos do not support. Without them a bad answer
	// can only be investigated by re-running the query and hoping retrieval is
	// deterministic enough to hand back the same set.
	//
	// Debug rather than Info because this is twelve ids per Ask against one line,
	// and it is only wanted when an answer is already suspect.
	chosenIDs []string

	// Which lane index is which. The two vector lanes carry the SAME weight by
	// design, so weight cannot identify them and the attribution above would
	// otherwise have nothing to key on. -1 means the lane never ran.
	rawLane, topicLane int
}

// attribute counts how many of the passages the model was actually given came
// from each lane family.
//
// Passages are keyed by video AND CHUNK ORDINAL — the same key FuseWeighted
// dedups on, and the only one that is unique. Video plus start second is not: a
// chapter chunk carries the transcript of its own span, so one moment is indexed
// twice under two kinds, which is what minMomentGapSeconds exists to hide. Keyed
// on the pair, a lane holding the chapter rendering would be credited for the
// transcript rendering the answer actually read.
//
// The counts overlap on purpose. A passage found by the topic lane AND a keyword
// rung counts once for each, because the question being asked is "did this lane
// contribute to what was read", not "which single lane owns this row".
func (d *askDiag) attribute(lanes []rag.Lane, chosen []rag.Hit) {
	d.attributed = true
	d.excerpts = len(chosen)
	if len(chosen) == 0 {
		return
	}
	key := func(h rag.Hit) string {
		return h.VideoID + ":" + strconv.Itoa(h.Ordinal)
	}
	read := make(map[string]bool, len(chosen))
	d.chosenIDs = make([]string, 0, len(chosen))
	for _, h := range chosen {
		read[key(h)] = true
		d.chosenIDs = append(d.chosenIDs, key(h))
		// Kinds are counted off chosen itself rather than off the lanes: a passage
		// is one kind however many lanes found it, so counting per lane would
		// multiply it by its own popularity.
		switch h.Kind {
		case rag.KindSummary:
			d.excSummary++
		case rag.KindChapter:
			d.excChapter++
		default:
			// Rows written before the kind column, and anything unrecognized,
			// count as transcript — which is what Store.Upsert already defaults
			// a blank kind to.
			d.excTranscript++
		}
	}
	count := func(lane rag.Lane) int {
		seen := make(map[string]bool)
		for _, h := range lane.Hits {
			if k := key(h); read[k] {
				seen[k] = true
			}
		}
		return len(seen)
	}
	keywordSeen := make(map[string]bool)
	floorSeen := make(map[string]bool)
	for i, lane := range lanes {
		switch {
		case i == d.rawLane:
			d.fromRaw = count(lane)
		case i == d.topicLane:
			d.fromTopic = count(lane)
		case lane.Weight <= rag.WeightKeywordAny:
			// The floor keeps its own count. Same test relevantVideos uses to keep
			// floor-only videos out of the coverage list, spelled the same way so
			// the two cannot drift: what is too weak to claim a video covers the
			// subject is the thing worth knowing about the excerpts too.
			for _, h := range lane.Hits {
				if k := key(h); read[k] {
					floorSeen[k] = true
				}
			}
		default:
			// The remaining keyword rungs are several lanes over one ladder, and
			// they overlap heavily by construction — a chunk the strict rung found
			// is usually found by the prefix rung too. Counting them as one family
			// avoids a number larger than the excerpt count itself.
			for _, h := range lane.Hits {
				if k := key(h); read[k] {
					keywordSeen[k] = true
				}
			}
		}
	}
	d.fromKeyword = len(keywordSeen)
	d.fromFloor = len(floorSeen)
}

// log writes the line. Info rather than Debug: an Ask is user-initiated and rare,
// one line per retrieval is not noise, and a diagnostic nobody can see without
// redeploying at a different level is a diagnostic nobody uses.
func (d askDiag) log(q string, fused []rag.Hit) {
	rungs := "none"
	if len(d.rungs) > 0 {
		rungs = strings.Join(d.rungs, " ")
	}
	understand := d.understand
	if understand == "" {
		understand = string(understandSkipped)
	}
	// "-" for a path that never had excerpts to attribute, matching semLaneDiag.
	excerpts := "-"
	if d.attributed {
		excerpts = fmt.Sprintf("%d raw=%d topic=%d kw=%d floor=%d t%d/s%d/c%d",
			d.excerpts, d.fromRaw, d.fromTopic, d.fromKeyword, d.fromFloor,
			d.excTranscript, d.excSummary, d.excChapter)
	}
	slog.Info("ask retrieval",
		"q", q,
		// The extracted query, beside the raw one. If a rewrite ever mangles a
		// question this is the field that shows it, and it is why the pair is
		// logged rather than just the topic.
		"topic", d.topic,
		"intent", d.intent,
		"understand", understand,
		"understand_ms", d.understandMs,
		// What the question asked for structurally and what became of it. A
		// filter that silently vanished is the failure mode this whole feature
		// introduces, and these four fields are the only place it is visible.
		"filters", orDash(d.filters),
		"filters_dropped", orDash(strings.Join(d.filtersDropped, "|")),
		"channels_unresolved", orDash(strings.Join(d.unresolved, "|")),
		"relaxed", d.relaxed,
		"keyword_rungs", rungs,
		// bounded → kept, plus the distance range, per lane. Comparing the two
		// ranges on one question is the recalibration data for DefaultMaxDistance
		// and SemanticSpread, both tuned before a rewritten query existed.
		"semantic_raw", d.semRaw.String(),
		"semantic_topic", d.semTopic.String(),
		"fused", fmt.Sprintf("%dh/%dv", len(fused), distinctVideos(fused)),
		"excerpts", excerpts,
		"ms", fmt.Sprintf("embed=%d fts=%d retrieval=%d", d.embedMs, d.ftsMs, d.retrievalMs),
	)
	// Which passages, in reading order, for the case where the shape above is not
	// enough and the excerpts themselves have to be pulled from the database.
	if len(d.chosenIDs) > 0 {
		slog.Debug("ask excerpts", "q", q, "chunks", strings.Join(d.chosenIDs, " "))
	}
}

// orDash keeps an absent value out of the log as "-" rather than as an empty
// string, so a field that was never set reads differently from one deliberately
// set to nothing — the same convention semLaneDiag.String uses.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// distinctVideos counts how many different videos a set of hits covers, which is
// the number that matters for every bound in this file: hits are cheap, videos
// are what the reader sees.
func distinctVideos(hits []rag.Hit) int {
	seen := make(map[string]struct{}, len(hits))
	for _, h := range hits {
		seen[h.VideoID] = struct{}{}
	}
	return len(seen)
}

// matchSnippet prefers the keyword lane's match-centred window and falls back
// to the head of the chunk for a hit that only the vector lane found (where
// there is no single matched term to centre on).
func matchSnippet(h rag.Hit) string {
	if h.Snippet != "" {
		return h.Snippet
	}
	return snippet(h.Text)
}

// snippet truncates s to a short preview, appending an ellipsis when
// truncated. 160 runes is generous enough for a search-result line without
// risking an oversized response body. It slices on a rune boundary — cutting
// mid-rune would emit replacement characters mid-word for any non-ASCII text.
func snippet(s string) string {
	rs := []rune(s)
	if len(rs) <= 160 {
		return s
	}
	return string(rs[:160]) + "…"
}

// handleReprocess answers POST /api/videos/{id}/reprocess: re-runs the whole
// post-import pipeline for one video. It resets the summary_status to pending
// and re-queues the analysis (summarize → classify → embed), and clears the
// SponsorBlock refresh sentinel so segments re-fetch too. Returns 202 once the
// summary job is enqueued. This is the Player's manual recovery action when a
// step went wrong (e.g. a summarize that failed).
func (s *server) handleReprocess(w http.ResponseWriter, r *http.Request) {
	if s.videos == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "videos are not configured")
		return
	}
	id := r.PathValue("id")
	v, err := s.videos.Get(id)
	if err != nil {
		serverError(w, r, err, "get video failed")
		return
	}
	if v == nil {
		writeJSONError(w, http.StatusNotFound, "video not found")
		return
	}
	// The pipeline reads the .vtt and nothing else, so the transcript is the only
	// precondition: a subtitle-less video has nothing to (re)summarize, and
	// re-enqueuing would flip a valid summary to no_transcript. Point the caller
	// at re-download (Phase 3.1b) instead of corrupting the summary.
	//
	// media_path is deliberately NOT part of the gate, and neither is
	// status='tombstoned'. A tombstone takes the media file only; the transcript
	// stays on the row, so a swept video is still rebuildable and rejecting it
	// here would make it permanently unsearchable the moment its chunks needed
	// rebuilding. Since migration 0023 this asks whether the text exists rather
	// than whether a path column happens to be set — a pointer standing in for
	// the thing itself is exactly what used to make this gate lie.
	if !v.HasTranscript {
		writeJSONError(w, http.StatusConflict, "no transcript present; re-download to restore before reprocessing")
		return
	}
	// An in-flight download is the one status that does disqualify: the .vtt
	// subtitle_path names is about to be rewritten under the summarizer (a
	// re-download of a tombstoned video keeps the old path while yt-dlp fetches
	// a new file), and the download's own success path enqueues a summary job of
	// its own — so reprocessing now risks a summary built from a half-written
	// transcript, which the second job then skips over because summary <> ''.
	// Wait for the file to land; the download re-runs the pipeline anyway.
	if v.Status == videos.StatusQueued || v.Status == videos.StatusDownloading {
		writeJSONError(w, http.StatusConflict, "download in progress; the transcript is being replaced")
		return
	}
	if s.summaryJobs == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "summaries are not configured")
		return
	}
	if err := s.videos.SetSummaryStatus(id, videos.SummaryPending, ""); err != nil {
		serverError(w, r, err, "reset summary status failed")
		return
	}
	// Wipe the stored analysis, or this whole endpoint is a no-op. The summarize
	// pipeline is resumable: it skips the summary step whenever summary <> '', so
	// that a retry of the fragile key-points step does not pay for the summary a
	// second time. Reprocess is precisely the case where the existing text is
	// the thing to throw away.
	if err := s.videos.ClearSummary(id); err != nil {
		serverError(w, r, err, "clear summary failed")
		return
	}
	// Clear the category too, so the worker re-classifies. Classification is
	// skipped for a video that already has one (otherwise every resumed job
	// would pay for a redundant call), which would make a wrong category
	// permanent — Reprocess is the only way a user can correct one.
	if err := s.videos.SetCategory(id, videos.UncategorizedCategory); err != nil {
		serverError(w, r, err, "reset category failed")
		return
	}
	// Mark the search index stale. Embedding is gated on the content recipe, so
	// without this a reprocess would clear the summary and then SKIP embedding
	// entirely — leaving the old summary chunk indexed against a video whose
	// summary has been thrown away.
	if err := s.videos.ClearEmbedRev(id); err != nil {
		serverError(w, r, err, "reset embed rev failed")
		return
	}
	// Force a fresh SponsorBlock fetch: clearing the refresh sentinel makes the
	// video sort first in the worker's stale-claim query, so its segments are
	// re-read on the next pass. Independent of the summary job above.
	//
	// On a tombstoned video the reset lands but nothing acts on it: that query
	// claims status='downloaded' rows only. That is right — segments exist to
	// be skipped during playback, and there is nothing to play — so the fetch
	// simply waits for a re-download to put the file back.
	if err := s.videos.ResetSponsorblockRefresh(id); err != nil {
		serverError(w, r, err, "reset sponsorblock refresh failed")
		return
	}
	if _, err := s.summaryJobs.Enqueue(id); err != nil {
		serverError(w, r, err, "enqueue summary job failed")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
