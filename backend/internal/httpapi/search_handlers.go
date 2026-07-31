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

const (
	// defaultSearchK caps how many moments the response carries, counted after
	// the per-video cap below rather than before it — capping the candidate
	// list instead would let one chatty video consume the whole budget and hide
	// every other video that mentioned the topic, which is the very thing
	// maxMatchesPerVideo exists to prevent.
	defaultSearchK = 20
	// searchCandidates is how many rows each LANE retrieves, and how deep the
	// fused list runs. It has to sit well above defaultSearchK for the spread
	// to have anything to work with: 200 chunks from one video still leave room
	// for other videos below them.
	searchCandidates = 200
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
	lanes := make([]rag.Lane, 0, 2)

	// Keyword lane, relaxed in steps: a natural question ANDs its function
	// words and matches nothing, so fall through to content-terms-only and
	// finally to OR. First tier with a row wins, so a precise query still gets
	// a precise lane and pays for exactly one round-trip.
	for _, tier := range rag.BuildFTSQueries(q) {
		hits, err := s.rag.SearchFTS(r.Context(), tier.Match, searchCandidates)
		if err != nil {
			slog.Warn("search: FTS degraded", "err", err)
			break
		}
		if len(hits) > 0 {
			// The rung that answered decides how much the lane counts: a query
			// that only matched once relaxed to "any one content word" is a
			// recall floor, not evidence. The weight rides on the rung itself,
			// since the ladder skips rungs that would repeat one another.
			lanes = append(lanes, rag.Lane{Hits: hits, Weight: tier.Weight})
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

	return rag.FuseWeighted(lanes, searchCandidates)
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
	// status='tombstoned'. A tombstone keeps subtitle_path and the .vtt precisely
	// so its analysis stays rebuildable; rejecting it here would leave a swept
	// video permanently unsearchable the moment its chunks needed rebuilding,
	// which is the whole reason the file is kept. A row tombstoned before that
	// changed has a blank subtitle_path and still lands here.
	if v.SubtitlePath == "" {
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
