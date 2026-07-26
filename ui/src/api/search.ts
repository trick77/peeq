import { api } from "./http";
import type { Video } from "./types";

// SearchMatch mirrors one httpapi search-result match — the sqlite-vec
// nearest-neighbor hit for a transcript/summary chunk within a video.
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
// backend's semantic search has no meaningful empty-string result and the UI
// shouldn't hit the network on every keystroke of a cleared search box.
export async function searchVideos(q: string): Promise<SearchResult[]> {
  const query = q.trim();
  if (!query) return [];
  const res = await api.get<{ results?: SearchResult[] }>(
    `/api/search?q=${encodeURIComponent(query)}`,
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
