import { api } from "./http";
import type { Channel } from "./types";

// ChannelFilter mirrors the ?filter= values the channels handler
// understands (Task 12 brief / channels_handlers.go).
export type ChannelFilter = "all" | "subscribed" | "tracked";

export async function listChannels(filter: ChannelFilter = "all"): Promise<Channel[]> {
  return api.get<Channel[]>(`/api/channels?filter=${encodeURIComponent(filter)}`, "failed to load channels");
}

export async function addChannel(
  url: string,
  subscribe: boolean,
): Promise<{ id: string; name: string; subscribed: boolean }> {
  return api.post<{ id: string; name: string; subscribed: boolean }>(
    "/api/channels",
    { url, subscribe },
    "failed to add channel",
  );
}

export async function updateChannel(
  id: string,
  patch: { autodownload?: boolean; format_override?: string },
): Promise<Channel> {
  return api.put<Channel>(`/api/channels/${encodeURIComponent(id)}`, patch, "failed to update channel");
}

export async function subscribeChannel(id: string): Promise<{ status: string }> {
  return api.post<{ status: string }>(
    `/api/channels/${encodeURIComponent(id)}/subscribe`,
    undefined,
    "failed to subscribe",
  );
}

export async function unsubscribeChannel(id: string): Promise<{ status: string }> {
  return api.post<{ status: string }>(
    `/api/channels/${encodeURIComponent(id)}/unsubscribe`,
    undefined,
    "failed to unsubscribe",
  );
}

export async function deleteChannel(id: string): Promise<void> {
  await api.delete(`/api/channels/${encodeURIComponent(id)}`, "failed to delete channel");
}
