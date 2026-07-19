import { api } from "./http";
import type { Video, VideoFilter } from "./types";

export async function listVideos(filter: VideoFilter = "all"): Promise<Video[]> {
  return api.get<Video[]>(`/api/videos?filter=${encodeURIComponent(filter)}`, "failed to load videos");
}

export async function getVideo(id: string): Promise<Video> {
  return api.get<Video>(`/api/videos/${encodeURIComponent(id)}`, "failed to load video");
}

export async function deleteVideo(id: string): Promise<void> {
  await api.delete(`/api/videos/${encodeURIComponent(id)}`, "failed to delete video");
}

export async function setFavorite(id: string, favorite: boolean): Promise<boolean> {
  const res = await api.post<{ favorite: boolean }>(
    `/api/videos/${encodeURIComponent(id)}/favorite`,
    { favorite },
    "failed to set favorite",
  );
  return res.favorite;
}

export async function setWatched(id: string, watched: boolean): Promise<boolean> {
  const res = await api.post<{ watched: boolean }>(
    `/api/videos/${encodeURIComponent(id)}/watched`,
    { watched },
    "failed to set watched",
  );
  return res.watched;
}

// setResume records the player's resume position (seconds). Note the
// asymmetry with Video.resume_position_seconds: this is the write body
// (`position`), the video row is read back with a differently-named field.
export async function setResume(id: string, position: number): Promise<number> {
  const res = await api.post<{ position: number }>(
    `/api/videos/${encodeURIComponent(id)}/resume`,
    { position },
    "failed to set resume position",
  );
  return res.position;
}

// redownload re-queues a failed or tombstoned video. 202/empty, so it uses
// postNoContent (like resummarize) — never .json() on an empty body.
export async function redownload(id: string): Promise<void> {
  await api.postNoContent(`/api/videos/${encodeURIComponent(id)}/redownload`, undefined, "failed to redownload video");
}

export function streamUrl(id: string): string {
  return `/api/videos/${encodeURIComponent(id)}/stream`;
}

// thumbnailUrl points at the Task 14 thumbnail endpoint. Callers should
// only render this (as an <img src>) when Video.has_thumbnail is true —
// VideoCard falls back to a gradient fill otherwise, matching the mockup.
export function thumbnailUrl(id: string): string {
  return `/api/videos/${encodeURIComponent(id)}/thumbnail`;
}
