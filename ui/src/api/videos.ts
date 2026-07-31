import { api } from "./http";
import type { PlaybackGrant, Video, VideoFilter, VideoSort } from "./types";

// ListVideosOptions mirrors the query params handleListVideos understands.
// Every field is optional; omitting all of them is "everything, newest first".
export type ListVideosOptions = {
  filter?: VideoFilter;
  category?: string;
  q?: string;
  sort?: VideoSort;
  /** Scopes the list to one channel (the channel page's Archive tab). */
  channel?: string;
};

export async function listVideos(
  opts: ListVideosOptions = {},
): Promise<Video[]> {
  const p = new URLSearchParams();
  if (opts.filter) p.set("filter", opts.filter);
  if (opts.category && opts.category !== "all")
    p.set("category", opts.category);
  if (opts.q) p.set("q", opts.q);
  if (opts.sort) p.set("sort", opts.sort);
  if (opts.channel) p.set("channel", opts.channel);
  const qs = p.toString();
  return api.get<Video[]>(
    `/api/videos${qs ? `?${qs}` : ""}`,
    "failed to load videos",
  );
}

export async function getVideo(id: string): Promise<Video> {
  return api.get<Video>(
    `/api/videos/${encodeURIComponent(id)}`,
    "failed to load video",
  );
}

export async function deleteVideo(id: string): Promise<void> {
  await api.delete(
    `/api/videos/${encodeURIComponent(id)}`,
    "failed to delete video",
  );
}

export async function setFavorite(
  id: string,
  favorite: boolean,
): Promise<boolean> {
  const res = await api.post<{ favorite: boolean }>(
    `/api/videos/${encodeURIComponent(id)}/favorite`,
    { favorite },
    "failed to set favorite",
  );
  return res.favorite;
}

// setWatched flips the manual watched toggle. It returns the bumped
// state_version alongside the flag so the Player can keep echoing a current
// value on its resume pings — its own toggle pauses and rewinds, and the
// timeupdate that seek fires would otherwise POST the pre-toggle version and
// 409 the toggling client against itself.
export async function setWatched(
  id: string,
  watched: boolean,
): Promise<{ watched: boolean; state_version: number }> {
  return api.post<{ watched: boolean; state_version: number }>(
    `/api/videos/${encodeURIComponent(id)}/watched`,
    { watched },
    "failed to set watched",
  );
}

// setCategory overrides the classifier's guess from the Player. The choice
// sticks even against a classify pass already in flight: the backend's
// classifier write refuses a video that already has a category.
export async function setCategory(
  id: string,
  category: string,
): Promise<string> {
  const res = await api.post<{ category: string }>(
    `/api/videos/${encodeURIComponent(id)}/category`,
    { category },
    "failed to set category",
  );
  return res.category;
}

// setResume records the player's resume position (seconds). Note the
// asymmetry with Video.resume_position_seconds: this is the write body
// (`position`), the video row is read back with a differently-named field.
//
// stateVersion is the video's state_version as the caller last saw it, echoed
// back so the backend can refuse a position written by a client that never saw
// a watched toggle made elsewhere (issue #97). Omitting it skips that check
// server-side. A refused write rejects with an ApiError of status 409; the
// response of an accepted one carries the row's current version, which the
// caller should keep — SetResume's own >=90% auto-watch bumps it.
export async function setResume(
  id: string,
  position: number,
  stateVersion?: number,
): Promise<{ position: number; state_version: number; watched: boolean }> {
  return api.post<{
    position: number;
    state_version: number;
    watched: boolean;
  }>(
    `/api/videos/${encodeURIComponent(id)}/resume`,
    stateVersion === undefined
      ? { position }
      : { position, state_version: stateVersion },
    "failed to set resume position",
  );
}

// redownload re-queues a failed or tombstoned video. 202/empty, so it uses
// postNoContent (like reprocess) — never .json() on an empty body.
export async function redownload(id: string): Promise<void> {
  await api.postNoContent(
    `/api/videos/${encodeURIComponent(id)}/redownload`,
    undefined,
    "failed to redownload video",
  );
}

export function streamUrl(id: string): string {
  return `/api/videos/${encodeURIComponent(id)}/stream`;
}

// createPlaybackGrant mints an auth-free stream URL for one video, for the
// player to use as its <video> src when direct playback is enabled. AirPlay
// hands that src to the Apple TV, which fetches it with no session cookie, so
// streamUrl above simply cannot work over AirPlay — see internal/playbackgrant.
//
// Rejects with a 409 when the setting is off, which callers treat as "use
// streamUrl instead" rather than as an error worth surfacing.
export async function createPlaybackGrant(id: string): Promise<PlaybackGrant> {
  return api.post<PlaybackGrant>(
    `/api/videos/${encodeURIComponent(id)}/playback-grant`,
    undefined,
    "failed to create a direct playback link",
  );
}

// thumbnailUrl points at the Task 14 thumbnail endpoint. Callers should
// only render this (as an <img src>) when Video.has_thumbnail is true —
// VideoCard falls back to a gradient fill otherwise, matching the mockup.
//
// Pass the row's thumbnail_version and the backend serves the poster as
// immutable, so the browser reuses its copy without asking. The URL changes
// when the stored bytes do, which is what keeps that safe — and is also why a
// re-downloaded poster appears at once rather than whenever a cache lapses.
export function thumbnailUrl(id: string, version?: string): string {
  return withVersion(
    `/api/videos/${encodeURIComponent(id)}/thumbnail`,
    version,
  );
}

// withVersion appends the cache-busting stamp, or returns the bare url when
// there is none — an unversioned request still works, it just revalidates.
// encodeURIComponent because the stamp comes from the database: it is a unix
// integer today, and a query parameter is not the place to trust that.
export function withVersion(url: string, version?: string): string {
  return version ? `${url}?v=${encodeURIComponent(version)}` : url;
}

// pendingThumbnailUrl points at the inbox thumbnail endpoint, which fetches and
// caches a pending video's remote thumbnail server-side and serves it locally.
// It is distinct from thumbnailUrl because a pending item has no videos row: it
// lives only in the channel_videos ledger, so the /api/videos/{id} route
// wouldn't find it.
export function pendingThumbnailUrl(id: string, version?: string): string {
  return withVersion(
    `/api/pending/${encodeURIComponent(id)}/thumbnail`,
    version,
  );
}
