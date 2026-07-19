import { api } from "./http";
import type { Video } from "./types";

// SearchMatch mirrors one httpapi search-result match — the sqlite-vec
// nearest-neighbor hit for a transcript/summary chunk within a video.
export type SearchMatch = {
  start_seconds: number;
  snippet: string;
  distance: number;
};

// SearchResult mirrors one httpapi.searchResultItem — a video joined with
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

export async function resummarize(id: string): Promise<void> {
  await api.post(`/api/videos/${encodeURIComponent(id)}/resummarize`, undefined, "failed to resummarize video");
}

// subtitlesUrl is just a string builder (no request) — callers render it as
// a <track src> / download link, mirroring streamUrl/thumbnailUrl in videos.ts.
export function subtitlesUrl(id: string): string {
  return `/api/videos/${encodeURIComponent(id)}/subtitles`;
}
