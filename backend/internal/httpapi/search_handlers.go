package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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
	HasChunks(ctx context.Context, videoID string) (bool, error)
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
func (s *server) retrieveAsk(r *http.Request, q string) []rag.Hit {
	lanes := make([]rag.Lane, 0, 5)

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
	diag := askDiag{}
	videosSeen := make(map[string]bool)
	for _, tier := range rag.BuildFTSQueries(q) {
		hits, err := s.rag.SearchFTS(r.Context(), tier.Match, searchCandidates)
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

	if s.embedder != nil {
		if vecs, err := s.embedder.Embed(r.Context(), []string{q}); err == nil && len(vecs) > 0 {
			semHits, err := s.rag.RetrieveWithin(r.Context(), vecs[0], semanticCandidates, s.searchMaxDistance)
			switch {
			case err != nil:
				slog.Warn("search: semantic retrieve degraded", "err", err)
			case len(semHits) > 0:
				// The absolute bound says "not about anything"; the spread says
				// "not about THIS, given what this query actually found". Both
				// are needed — on a query the library covers well every row
				// clears the absolute bound, and the spread is the only thing
				// that can tell the sixth genuinely relevant chunk from the
				// seventh merely-nearest one.
				//
				// A negative searchMaxDistance is the documented opt-out from
				// bounding the vector lane, so it opts out of BOTH: an operator
				// who asks for unbounded KNN gets unbounded KNN, not a floor
				// they cannot see in any setting.
				diag.semBounded, diag.semBoundedVideos = len(semHits), distinctVideos(semHits)
				diag.nearest, diag.farthest = semHits[0].Distance, semHits[len(semHits)-1].Distance
				if s.searchMaxDistance > 0 {
					semHits = rag.WithinSpread(semHits, rag.SemanticSpread)
				}
				diag.semKept, diag.semKeptVideos = len(semHits), distinctVideos(semHits)
				lanes = append(lanes, rag.Lane{Hits: semHits, Weight: rag.WeightSemantic})
			}
		} else if err != nil {
			// Semantic unavailable (endpoint down/misconfigured); fall back to
			// FTS-only rather than failing the whole search.
			slog.Warn("search: semantic degraded, using FTS only", "err", err)
		}
	}

	fused := rag.FuseWeighted(lanes, searchCandidates)
	diag.log(q, fused)
	return fused
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
type askDiag struct {
	rungs                        []string
	semBounded, semBoundedVideos int
	semKept, semKeptVideos       int
	nearest, farthest            float64
}

// log writes the line. Info rather than Debug: an Ask is user-initiated and rare,
// one line per retrieval is not noise, and a diagnostic nobody can see without
// redeploying at a different level is a diagnostic nobody uses.
func (d askDiag) log(q string, fused []rag.Hit) {
	rungs := "none"
	if len(d.rungs) > 0 {
		rungs = strings.Join(d.rungs, " ")
	}
	slog.Info("ask retrieval",
		"q", q,
		"keyword_rungs", rungs,
		"semantic_bounded", fmt.Sprintf("%dh/%dv", d.semBounded, d.semBoundedVideos),
		"semantic_kept", fmt.Sprintf("%dh/%dv", d.semKept, d.semKeptVideos),
		"distance_range", fmt.Sprintf("%.3f..%.3f", d.nearest, d.farthest),
		"fused", fmt.Sprintf("%dh/%dv", len(fused), distinctVideos(fused)),
	)
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
