package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

// RagStore is the slice of rag.Store the search endpoint uses — declared here
// at the consumer, following DownloadsRunner, PlaybackStore and ShareLinkStore.
// *rag.Store satisfies it. Unlike those two this is a genuine subset: the store
// also owns the chunk write/delete path, which the summarize worker drives and
// no handler touches.
//
// Note this does NOT let httpapi drop its rag import. rag.Hit is the return
// type, and rag.BuildFTSMatch is a package-level FUNCTION passed as an argument
// below — an interface cannot capture either. The gain here is testability: the
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

const (
	// defaultSearchK caps how many fused chunks are returned.
	defaultSearchK = 20
	// searchCandidates is how many rows each LANE retrieves before fusion.
	// Keeping it well above defaultSearchK matters for a corpus-wide question:
	// with both numbers at 20, a single chatty video could fill the entire
	// result set and hide every other video that mentioned the topic.
	searchCandidates = 200
	// maxMatchesPerVideo caps how many moments one video contributes, so a
	// long video cannot crowd out the rest of the library.
	maxMatchesPerVideo = 4
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
		hits = s.retrieveAsk(r, q, k)
	} else {
		hits = s.retrieveFind(r, q, k)
	}

	order := make([]string, 0)
	byVideo := make(map[string]*searchResult)
	for _, h := range hits {
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
		if len(g.Matches) >= maxMatchesPerVideo {
			continue
		}
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
func (s *server) retrieveFind(r *http.Request, q string, k int) []rag.Hit {
	match := rag.ParseFTSQuery(q)
	if match == "" {
		return nil
	}
	hits, err := s.rag.SearchFTS(r.Context(), match, k)
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
func (s *server) retrieveAsk(r *http.Request, q string, k int) []rag.Hit {
	lanes := make([]rag.Lane, 0, 2)

	// Keyword lane, relaxed in steps: a natural question ANDs its function
	// words and matches nothing, so fall through to content-terms-only and
	// finally to OR. First tier with a row wins, so a precise query still gets
	// a precise lane and pays for exactly one round-trip.
	for _, match := range rag.BuildFTSQueries(q) {
		hits, err := s.rag.SearchFTS(r.Context(), match, searchCandidates)
		if err != nil {
			slog.Warn("search: FTS degraded", "err", err)
			break
		}
		if len(hits) > 0 {
			lanes = append(lanes, rag.Lane{Hits: hits, Weight: rag.WeightKeyword})
			break
		}
	}

	if s.embedder != nil {
		if vecs, err := s.embedder.Embed(r.Context(), []string{q}); err == nil && len(vecs) > 0 {
			semHits, err := s.rag.RetrieveWithin(r.Context(), vecs[0], searchCandidates, s.searchMaxDistance)
			switch {
			case err != nil:
				slog.Warn("search: semantic retrieve degraded", "err", err)
			case len(semHits) > 0:
				lanes = append(lanes, rag.Lane{Hits: semHits, Weight: rag.WeightSemantic})
			}
		} else if err != nil {
			// Semantic unavailable (endpoint down/misconfigured); fall back to
			// FTS-only rather than failing the whole search.
			slog.Warn("search: semantic degraded, using FTS only", "err", err)
		}
	}

	return rag.FuseWeighted(lanes, k)
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
	// A tombstoned or subtitle-less video has no transcript to (re)summarize;
	// re-enqueuing would only flip a valid summary to no_transcript. Point the
	// caller at re-download (Phase 3.1b) instead of corrupting the summary.
	if v.Status == videos.StatusTombstoned || v.MediaPath == "" || v.SubtitlePath == "" {
		writeJSONError(w, http.StatusConflict, "media not present; re-download to restore before reprocessing")
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
	// Force a fresh SponsorBlock fetch: clearing the refresh sentinel makes the
	// video sort first in the worker's stale-claim query, so its segments are
	// re-read on the next pass. Independent of the summary job above.
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
