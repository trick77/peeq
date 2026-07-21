import { api, ApiError } from "./http";
import { CookieRequiredError } from "./downloads";
import type { Channel, ChannelDetail, ScanResult } from "./types";

// ChannelFilter mirrors the ?filter= values the channels handler
// understands (Task 12 brief / channels_handlers.go).
export type ChannelFilter = "all" | "subscribed" | "tracked" | "autodownload";

export async function listChannels(filter: ChannelFilter = "all"): Promise<Channel[]> {
  return api.get<Channel[]>(`/api/channels?filter=${encodeURIComponent(filter)}`, "failed to load channels");
}

// addChannel tracks a channel (and subscribes it when subscribe is true).
// Resolving a channel shells out to yt-dlp and needs a cookie, so the
// handler's 409 is mapped to the same CookieRequiredError addDownload
// raises — callers can then use one cookie-missing branch for both.
export async function addChannel(
  url: string,
  subscribe: boolean,
): Promise<{ id: string; name: string; subscribed: boolean }> {
  try {
    return await api.post<{ id: string; name: string; subscribed: boolean }>(
      "/api/channels",
      { url, subscribe },
      "failed to add channel",
    );
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      throw new CookieRequiredError();
    }
    throw err;
  }
}

// ChannelConfigUpdate mirrors handleChannelsPut's actual response body
// (channels_handlers.go: writeJSON(w, map[string]any{"id", "autodownload",
// "format_override"})) — deliberately NOT the full Channel shape. The
// handler never re-reads name/subscribed/pending_count/downloaded_count
// after the update, so callers must not assume this return value has them.
export type ChannelConfigUpdate = {
  id: string;
  autodownload: boolean;
  format_override: string;
};

export async function updateChannel(
  id: string,
  patch: { autodownload?: boolean; format_override?: string },
): Promise<ChannelConfigUpdate> {
  return api.put<ChannelConfigUpdate>(`/api/channels/${encodeURIComponent(id)}`, patch, "failed to update channel");
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

export async function getChannel(id: string): Promise<ChannelDetail> {
  return api.get<ChannelDetail>(`/api/channels/${encodeURIComponent(id)}`, "failed to load channel");
}

export async function scanChannel(id: string): Promise<ScanResult> {
  return api.post<ScanResult>(`/api/channels/${encodeURIComponent(id)}/scan`, undefined, "failed to schedule a check");
}

export function channelAvatarUrl(id: string): string {
  return `/api/channels/${encodeURIComponent(id)}/avatar`;
}

export function channelBannerUrl(id: string): string {
  return `/api/channels/${encodeURIComponent(id)}/banner`;
}
