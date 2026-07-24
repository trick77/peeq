import { api } from "./http";
import type { Chapter, KeyPoint } from "./types";

// ShareTTL is the fixed set of link lifetimes the share popover offers. The
// empty string / "never" mean the link never expires. These strings are the
// wire contract with the backend's shareTTLs map — keep them in step.
export type ShareTTL = "24h" | "7d" | "30d" | "never";

// ShareStatus is the owner-facing view of a video's share link, returned by
// create/status/stop. `shared` is the one field always present; the rest are
// set only when a live link exists.
export type ShareStatus = {
  shared: boolean;
  /** Absolute (or origin-relative) URL to hand out. */
  url?: string;
  /** The raw token — the path segment of the share link. */
  token?: string;
  /** UTC 'YYYY-MM-DD HH:MM:SS'; absent when the link never expires. */
  expires_at?: string;
};

// PublicVideo is the trimmed video the chromeless /s/<token> page renders. It
// mirrors httpapi.publicVideoDTO — deliberately none of the owner-only fields.
export type PublicVideo = {
  title: string;
  channel_name: string;
  duration_seconds?: number;
  summary: string;
  summary_status: string;
  chapters?: Chapter[];
  key_points?: KeyPoint[];
  has_thumbnail: boolean;
  has_subtitles: boolean;
  audio_language: string;
  expires_at?: string;
};

// getShareStatus reports whether a video currently has a live share link, for
// the player's "Shared" chip and the popover's redisplay of the link.
export async function getShareStatus(id: string): Promise<ShareStatus> {
  return api.get<ShareStatus>(
    `/api/videos/${encodeURIComponent(id)}/share`,
    "failed to load share status",
  );
}

// createShare mints (or replaces) the share link for a video with the given
// lifetime, returning the live link. Re-sharing rotates the token.
export async function createShare(
  id: string,
  ttl: ShareTTL,
): Promise<ShareStatus> {
  return api.post<ShareStatus>(
    `/api/videos/${encodeURIComponent(id)}/share`,
    { ttl },
    "failed to create share link",
  );
}

// stopShare turns off sharing for a video. Idempotent.
export async function stopShare(id: string): Promise<ShareStatus> {
  return api.delete<ShareStatus>(
    `/api/videos/${encodeURIComponent(id)}/share`,
    "failed to stop sharing",
  );
}

// getSharedVideo fetches the public metadata for a share token. Throws an
// ApiError with status 404 when the link is unknown, expired, or revoked —
// the Share view treats that as the "link no longer active" screen.
export async function getSharedVideo(token: string): Promise<PublicVideo> {
  return api.get<PublicVideo>(
    `/api/s/${encodeURIComponent(token)}`,
    "failed to load shared video",
  );
}

// shareStreamUrl / shareThumbnailUrl / shareSubtitlesUrl build the public media
// URLs the chromeless player points at — the /s/<token> analogues of the
// authenticated stream/thumbnail/subtitles endpoints.
export function shareStreamUrl(token: string): string {
  return `/api/s/${encodeURIComponent(token)}/stream`;
}
export function shareThumbnailUrl(token: string): string {
  return `/api/s/${encodeURIComponent(token)}/thumbnail`;
}
export function shareSubtitlesUrl(token: string): string {
  return `/api/s/${encodeURIComponent(token)}/subtitles`;
}
