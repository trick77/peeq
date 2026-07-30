import { api } from "./http";
import type { Video } from "./types";

// SearchMode selects how the backend retrieves.
//
// "find" is a literal full-text search: FTS5 only, operators honoured, no
// embedding request and no model call. It is instant, free, and can genuinely
// return nothing — which is the right answer when the words are not there.
//
// "ask" adds distance-bounded vector search for meaning-based matches, fused
// with the keyword lane weighted higher.
export type SearchMode = "find" | "ask";

// SearchMatch mirrors one httpapi search-result match.
//
// `snippet` may contain HIGHLIGHT_START/HIGHLIGHT_END delimiters around the
// matched terms; render it through splitHighlights (src/highlight.ts) rather
// than as raw text. `distance` is the vector lane's L2 distance and is 0 for a
// keyword-only hit — it is retrieval diagnostics, not something to show a user.
export type SearchMatch = {
  start_seconds: number;
  snippet: string;
  distance: number;
  kind: string;
};

// SearchResult mirrors one httpapi.searchResult — a video joined with
// its best-matching chunks for the query.
export type SearchResult = {
  video: Video;
  matches: SearchMatch[];
};

// searchVideos short-circuits on a blank query (no request at all) since the
// backend has no meaningful empty-string result and the UI shouldn't hit the
// network on every keystroke of a cleared search box.
export async function searchVideos(
  q: string,
  mode: SearchMode = "find",
): Promise<SearchResult[]> {
  const query = q.trim();
  if (!query) return [];
  const res = await api.get<{ results?: SearchResult[] }>(
    `/api/search?q=${encodeURIComponent(query)}&mode=${mode}`,
    "search failed",
  );
  return res.results ?? [];
}

// reprocess POSTs to the reprocess endpoint, which re-runs the whole
// post-import pipeline for a video and replies 202 Accepted with an EMPTY body
// (the work happens asynchronously). It uses `api.postNoContent` rather than
// `api.post`: the latter always calls response.json() on a 2xx response, which
// throws SyntaxError on an empty body and would make every successful reprocess
// request look like a failure to the caller. postNoContent still preserves the
// shared 401 -> AuthExpiredError / non-2xx -> ApiError handling.
export async function reprocess(id: string): Promise<void> {
  await api.postNoContent(
    `/api/videos/${encodeURIComponent(id)}/reprocess`,
    undefined,
    "failed to reprocess video",
  );
}

// subtitlesUrl is just a string builder (no request) — callers render it as
// a <track src> / download link, mirroring streamUrl/thumbnailUrl in videos.ts.
export function subtitlesUrl(id: string): string {
  return `/api/videos/${encodeURIComponent(id)}/subtitles`;
}
