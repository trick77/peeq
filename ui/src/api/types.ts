// types.ts — the JSON shapes vark's backend actually returns, mirrored
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
export type VideoFilter = "all" | "unwatched" | "watched" | "favorites" | "downloading";

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
  sponsorblock_segments?: { category: string; start_time: number; end_time: number }[];
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
// published by the worker's OnProgress callback (cmd/vark/main.go).
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
export type Channel = {
  id: string;
  handle?: string;
  name: string;
  subscribed: boolean;
  autodownload: boolean;
  format_override?: string;
  pending_count: number;
  downloaded_count: number;
};

// PendingItem mirrors httpapi.pendingItem — one channel_videos ledger entry
// awaiting a keep/ignore decision. No local media yet, so only the remote
// thumbnail_url is present (no thumbnail_path).
export type PendingItem = {
  video_id: string;
  channel_id: string;
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
