// types.ts — the JSON shapes peeq's backend actually returns, mirrored
// field-for-field from the Go DTOs (backend/internal/httpapi/*.go). Two
// conventions collide here, both preserved deliberately:
//   - videos/downloads/settings/cookie: snake_case (Go's default json tags)
//   - the User returned by /api/auth/me: camelCase (auth/model.go sets
//     explicit `json:"displayName"`/`json:"role"` tags)
// Do not "fix" one to match the other — that would just diverge from what
// the wire actually sends.

export type Role = string;

export type User = {
  id: string;
  username: string;
  email: string;
  displayName: string;
  role: Role;
};

// VideoFilter mirrors the ?filter= values videos.Store.List understands.
export type VideoFilter =
  "all" | "unwatched" | "watched" | "favorites" | "downloading";

// Video mirrors httpapi.videoDTO exactly. media_path is deliberately never
// exposed (server-local filesystem path); has_media + the /stream endpoint
// are how the UI knows whether/how to play it.
export type Video = {
  id: string;
  url: string;
  title: string;
  channel_id: string;
  channel_name: string;
  duration_seconds?: number;
  published_at?: string;
  description?: string;
  // has_thumbnail (not the filesystem path) is what the backend sends — see
  // GET /api/videos/{id}/thumbnail (Task 14) for the actual image bytes.
  has_thumbnail: boolean;
  has_media: boolean;
  filesize_bytes?: number;
  format_used?: string;
  availability: string;
  status: string;
  error_message?: string;
  watched: boolean;
  watched_at?: string;
  resume_position_seconds: number;
  favorite: boolean;
  downloaded_at?: string;
  // sponsorblock_segments mirrors httpapi.sponsorblockSegmentDTO — the
  // downloaded video's own --sponsorblock-mark chapters, parsed server-side
  // from the stored JSON column. Absent (undefined, via omitempty) when
  // there are none. Player.tsx auto-skips [start_time, end_time) ranges.
  sponsorblock_segments?: {
    category: string;
    start_time: number;
    end_time: number;
  }[];
  // summary/chapters/key_points/summary_status/audio_language/has_subtitles
  // mirror the Task 14 summarization fields added to httpapi.videoDTO —
  // chapters/key_points arrive as arrays (never omitted, just empty).
  summary: string;
  chapters: Chapter[];
  key_points: KeyPoint[];
  summary_status: string;
  audio_language: string;
  has_subtitles: boolean;
  // category mirrors the Task 7 classification field — always present,
  // "uncategorized" is the fallback (see categories.ts, the TS mirror of
  // backend/internal/videos/category.go).
  category: string;
};

// Chapter mirrors httpapi's per-video chapter DTO — source distinguishes
// yt-dlp-provided chapters from ones inferred by the summarizer.
export type Chapter = {
  ts: number;
  title: string;
  source: string;
};

// KeyPoint mirrors httpapi's per-video key-point DTO — a timestamped
// highlight extracted during summarization.
export type KeyPoint = {
  ts: number;
  text: string;
};

// Job mirrors httpapi.downloadItem — one download-queue entry, optionally
// joined with its video's title/channel for display.
export type Job = {
  job_id: number;
  video_id: string;
  title?: string;
  channel_name?: string;
  state: string;
  priority: number;
  attempts: number;
  last_error?: string;
  enqueued_at?: string;
};

// DownloadProgressEvent mirrors the payload of the SSE "progress" event
// published by the worker's OnProgress callback (cmd/peeq/main.go).
export type DownloadProgressEvent = {
  job_id: number;
  percent: number;
  speed: string;
  eta: string;
};

// Settings mirrors settings.Settings (the non-secret view — the cookie body
// itself is never present in this shape, only its status/timestamp).
export type Settings = {
  cookie_status: string;
  cookie_updated_at?: string;
  format_preset: string;
  format_custom: string;
  limit_rate: string;
  throttle_base_seconds: number;
  retention_days: number;
  min_free_gb: number;
  min_video_duration_seconds: number;
  ytdlp_version: string;
  // youtube_paused/youtube_pause_reason mirror the Task 10 global kill-switch
  // fields — user-writable, but only via the dedicated POST
  // /api/youtube/pause|resume endpoints (downloads.ts), never through
  // SettingsPatch/PUT.
  youtube_paused: boolean;
  youtube_pause_reason: string;
};

// SettingsPatch mirrors settingsPatchRequest — every field optional, PUT
// merges. The cookie is never part of this patch; it only ever moves
// through putCookie/PUT /api/settings/cookie.
export type SettingsPatch = Partial<{
  format_preset: string;
  format_custom: string;
  limit_rate: string;
  throttle_base_seconds: number;
  retention_days: number;
  min_free_gb: number;
  min_video_duration_seconds: number;
}>;

// Channel mirrors httpapi.channelItem — one tracked channel, joined with
// its (optional) subscription state and video counts.
// dormant/last_video_at mirror the Task 4 staleness fields: dormant is
// always present (true only for a subscribed channel quiet 6+ months);
// last_video_at is omitempty — absent means peeq has never seen a video
// from this channel.
export type Channel = {
  id: string;
  handle?: string;
  name: string;
  subscribed: boolean;
  autodownload: boolean;
  format_override?: string;
  pending_count: number;
  downloaded_count: number;
  dormant: boolean;
  last_video_at?: string;
};

// AutoUnsubscribedChannel mirrors httpapi's GET
// /api/channels/auto-unsubscribed entry — a channel peeq unsubscribed on
// its own after repeated "deleted" scans. handle is omitempty; reason is
// currently always "deleted" (channels.ReasonDeleted).
export type AutoUnsubscribedChannel = {
  id: string;
  handle?: string;
  name: string;
  reason: string;
  at: string;
};

// PendingItem mirrors httpapi.pendingItem — one channel_videos ledger entry
// awaiting a keep/ignore decision. No local media yet, so only the remote
// thumbnail_url is present (no thumbnail_path).
export type PendingItem = {
  video_id: string;
  channel_id: string;
  channel_name: string;
  title: string;
  duration_seconds: number;
  url: string;
  thumbnail_url: string;
};

// CookieHealth mirrors httpapi.cookieHealthResponse — distinct from
// Settings.cookie_status: this is the dedicated health-check shape used by
// the rail's cookie status indicator.
export type CookieHealth = {
  status: string;
  updated_at?: string;
  present: boolean;
};

// APITokenStatus is the non-secret view of the machine API token. The token
// itself is write-only: it is never returned after the response that
// creates it, so this type deliberately has no token field.
export type APITokenStatus = {
  present: boolean;
  created_at?: string;
};

// APITokenCreated is the one and only shape that carries the plaintext
// token, returned by createAPIToken. Hold it in component state only — it
// cannot be fetched again.
export type APITokenCreated = {
  token: string;
  created_at: string;
};
