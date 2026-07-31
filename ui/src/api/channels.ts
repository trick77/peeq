import { api, ApiError } from "./http";
import { CookieRequiredError } from "./downloads";
import { withVersion } from "./videos";
import type {
  AutoUnsubscribedChannel,
  Channel,
  ChannelDetail,
  ScanResult,
} from "./types";

// ChannelFilter mirrors the ?filter= values the channels handler
// understands (Task 12 brief / channels_handlers.go).
export type ChannelFilter =
  "all" | "subscribed" | "notsubscribed" | "downloaded" | "autodownload";

export async function listChannels(
  filter: ChannelFilter = "all",
): Promise<Channel[]> {
  return api.get<Channel[]>(
    `/api/channels?filter=${encodeURIComponent(filter)}`,
    "failed to load channels",
  );
}

// addChannel adds a channel (and subscribes it when subscribe is true).
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
  // auto_summary is optional because the handler omits the two subscription
  // fields when the channel is not subscribed and the request only carried
  // this one — see handleChannelsPut. It is the only field settable on an
  // added-but-unsubscribed channel.
  auto_summary?: boolean;
};

export async function updateChannel(
  id: string,
  patch: {
    autodownload?: boolean;
    format_override?: string;
    auto_summary?: boolean;
  },
): Promise<ChannelConfigUpdate> {
  return api.put<ChannelConfigUpdate>(
    `/api/channels/${encodeURIComponent(id)}`,
    patch,
    "failed to update channel",
  );
}

export async function subscribeChannel(
  id: string,
): Promise<{ status: string }> {
  return api.post<{ status: string }>(
    `/api/channels/${encodeURIComponent(id)}/subscribe`,
    undefined,
    "failed to subscribe",
  );
}

export async function unsubscribeChannel(
  id: string,
): Promise<{ status: string }> {
  return api.post<{ status: string }>(
    `/api/channels/${encodeURIComponent(id)}/unsubscribe`,
    undefined,
    "failed to unsubscribe",
  );
}

export async function deleteChannel(id: string): Promise<void> {
  await api.delete(
    `/api/channels/${encodeURIComponent(id)}`,
    "failed to delete channel",
  );
}

export async function getChannel(id: string): Promise<ChannelDetail> {
  return api.get<ChannelDetail>(
    `/api/channels/${encodeURIComponent(id)}`,
    "failed to load channel",
  );
}

export async function scanChannel(id: string): Promise<ScanResult> {
  return api.post<ScanResult>(
    `/api/channels/${encodeURIComponent(id)}/scan`,
    undefined,
    "failed to schedule a check",
  );
}

// SkipResult reports where a skipped schedule landed and where it was. The
// previous instant is what makes undo possible: handed straight back to the
// same endpoint, it returns the occurrence to exactly where it was.
export type SkipResult = {
  status: string;
  at: string;
  previous_at: string;
};

// skipScheduledScan pushes a channel's next scan out by one normal interval, or
// restores `at` when given one. Skipping does not mark the channel scanned —
// the backend moves only next_scan_at — so a channel skipped repeatedly still
// knows how far back it has actually looked, and cannot later arrive as though
// its whole back catalogue were new.
export async function skipScheduledScan(
  id: string,
  at?: string,
): Promise<SkipResult> {
  return api.post<SkipResult>(
    `/api/channels/${encodeURIComponent(id)}/skip-scan`,
    at ? { at } : undefined,
    "failed to skip the scan",
  );
}

// skipScheduledMeta is skipScheduledScan for the weekly metadata refresh.
export async function skipScheduledMeta(
  id: string,
  at?: string,
): Promise<SkipResult> {
  return api.post<SkipResult>(
    `/api/channels/${encodeURIComponent(id)}/skip-meta`,
    at ? { at } : undefined,
    "failed to skip the refresh",
  );
}

// refreshChannel forces an on-demand metadata re-read, the manual way out of a
// failed resolve that peeq will not retry on its own. The handler's 409 is
// mapped to the same CookieRequiredError addChannel raises, so the caller can
// tell the user to refresh their cookie rather than showing a raw error.
export async function refreshChannel(id: string): Promise<{ status: string }> {
  try {
    return await api.post<{ status: string }>(
      `/api/channels/${encodeURIComponent(id)}/refresh`,
      undefined,
      "failed to refresh",
    );
  } catch (err) {
    if (err instanceof ApiError && err.status === 409) {
      throw new CookieRequiredError();
    }
    throw err;
  }
}

// The optional version is the image's avatar_version/banner_version off the
// same row. With it the artwork is served immutable and never re-requested;
// without it the response merely revalidates. Either way the weekly metadata
// refresh shows up immediately, because storing new artwork moves the stamp and
// therefore the URL.
export function channelAvatarUrl(id: string, version?: string): string {
  return withVersion(`/api/channels/${encodeURIComponent(id)}/avatar`, version);
}

export function channelBannerUrl(id: string, version?: string): string {
  return withVersion(`/api/channels/${encodeURIComponent(id)}/banner`, version);
}

// listAutoUnsubscribedChannels returns every channel peeq unsubscribed on
// its own (most recent first) — the "tombstone" list shown in the
// auto-unsubscribed section.
export async function listAutoUnsubscribedChannels(): Promise<
  AutoUnsubscribedChannel[]
> {
  return api.get<AutoUnsubscribedChannel[]>(
    "/api/channels/auto-unsubscribed",
    "failed to load auto-unsubscribed channels",
  );
}

// dismissDormantChannel suppresses a channel's dormancy flag until it posts
// again and then goes quiet a second time. 404s (handled by the caller as a
// regular ApiError) if the channel has no subscription row.
export async function dismissDormantChannel(
  id: string,
): Promise<{ status: string }> {
  return api.post<{ status: string }>(
    `/api/channels/${encodeURIComponent(id)}/dismiss-dormant`,
    undefined,
    "failed to dismiss",
  );
}

// resubscribeChannel restores a subscription peeq auto-unsubscribed,
// clearing the auto-unsubscribe record.
export async function resubscribeChannel(
  id: string,
): Promise<{ status: string }> {
  return api.post<{ status: string }>(
    `/api/channels/${encodeURIComponent(id)}/resubscribe`,
    undefined,
    "failed to resubscribe",
  );
}
