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
  thumbnail_path?: string;
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
}>;

// CookieHealth mirrors httpapi.cookieHealthResponse — distinct from
// Settings.cookie_status: this is the dedicated health-check shape used by
// the rail's cookie status indicator.
export type CookieHealth = {
  status: string;
  updated_at?: string;
  present: boolean;
};
