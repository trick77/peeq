package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/trick77/peeq/internal/rag"
)

// searchMatch is one hit within a search result's video, in the shape the
// frontend player uses to jump to a timestamp.
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

// defaultSearchK is the KNN breadth used when ?k= is absent or invalid.
const defaultSearchK = 20

// handleSearch answers GET /api/search?q=&k=: blank q short-circuits to an
// empty result set without ever calling the embedder (cheap, and avoids
// spending an embed call on a no-op query). Otherwise it runs a hybrid
// search: FTS5 keyword search (always, needs no external service) plus
// semantic vector search (best-effort — the embedder is optional and any
// failure just degrades to FTS-only), fused via reciprocal rank fusion and
// grouped by video.
func (s *server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, map[string]any{"results": []any{}})
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

	lists := make([][]rag.Hit, 0, 2)
	if ftsHits, err := s.rag.SearchFTS(r.Context(), rag.BuildFTSMatch(q), k); err == nil && len(ftsHits) > 0 {
		lists = append(lists, ftsHits)
	}
	if s.embedder != nil {
		if vecs, err := s.embedder.Embed(r.Context(), []string{q}); err == nil && len(vecs) > 0 {
			if semHits, err := s.rag.Retrieve(r.Context(), vecs[0], k); err == nil && len(semHits) > 0 {
				lists = append(lists, semHits)
			}
		} else if err != nil {
			// Semantic unavailable (endpoint down/misconfigured); fall back to
			// FTS-only rather than failing the whole search.
			slog.Warn("search: semantic degraded, using FTS only", "err", err)
		}
	}

	hits := rag.FuseRRF(lists, k)

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
		g.Matches = append(g.Matches, searchMatch{
			StartSeconds: h.StartSeconds,
			Snippet:      snippet(h.Text),
			Distance:     h.Distance,
			Kind:         h.Kind,
		})
	}

	out := make([]*searchResult, 0, len(order))
	for _, id := range order {
		out = append(out, byVideo[id])
	}
	writeJSON(w, map[string]any{"results": out})
}

// snippet truncates s to a short preview, appending an ellipsis when
// truncated. 160 runes/bytes is generous enough for a search-result line
// without risking an oversized response body.
func snippet(s string) string {
	if len(s) <= 160 {
		return s
	}
	return s[:160] + "…"
}

// handleResummarize answers POST /api/videos/{id}/resummarize: resets the
// video's summary_status to pending and hands it to SummaryJobs for
// (re)processing, returning 202 once the job is enqueued.
func (s *server) handleResummarize(w http.ResponseWriter, r *http.Request) {
	if s.videos == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "videos are not configured")
		return
	}
	id := r.PathValue("id")
	v, err := s.videos.Get(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "get video failed")
		return
	}
	if v == nil {
		writeJSONError(w, http.StatusNotFound, "video not found")
		return
	}
	// A tombstoned or subtitle-less video has no transcript to (re)summarize;
	// re-enqueuing would only flip a valid summary to no_transcript. Point the
	// caller at re-download (Phase 3.1b) instead of corrupting the summary.
	if v.Status == "tombstoned" || v.MediaPath == "" || v.SubtitlePath == "" {
		writeJSONError(w, http.StatusConflict, "media not present; re-download to restore before resummarizing")
		return
	}
	if s.summaryJobs == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "summaries are not configured")
		return
	}
	if err := s.videos.SetSummaryStatus(id, "pending", ""); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "reset summary status failed")
		return
	}
	if _, err := s.summaryJobs.Enqueue(id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "enqueue summary job failed")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
