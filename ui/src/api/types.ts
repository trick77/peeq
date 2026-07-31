// types.ts — the JSON shapes peeq's backend actually returns, mirrored
// field-for-field from the Go DTOs (backend/internal/httpapi/*.go). Two
// conventions collide here, both preserved deliberately:
//   - videos/downloads/settings/cookie: snake_case (Go's default json tags)
//   - the User returned by /api/auth/me: camelCase (auth/model.go sets
//     explicit `json:"displayName"`/`json:"role"` tags)
// Do not "fix" one to match the other — that would just diverge from what
// the wire actually sends.
//
// The enum-valued fields below are typed against ./enums, whose sets are held
// to the Go constants by src/wireenums.test.ts. Before that they were plain
// `string`, which is why nothing noticed that the rail's cookie label map had
// an "expired" case the backend never sends.
import type {
  Availability,
  CookieStatus,
  JobState,
  Role,
  SummaryJobState,
  SummaryStatus,
  VideoStatus,
} from "./enums";

export type { Role };

export type User = {
  id: string;
  username: string;
  email: string;
  displayName: string;
  role: Role;
};

// VideoFilter mirrors the ?filter= values videos.Store.List understands.
//
// "downloading" was one of them until the Library became ready-only. Go
// removed it (see the note on videos.Store.List) and it is dropped here rather
// than kept as an alias: the server now folds it into the default branch and
// returns what "all" returns, so a caller still passing it would silently get
// something other than what the name promises.
export type VideoFilter =
  "all" | "unwatched" | "in_progress" | "watched" | "favorites";

// VideoSort mirrors the sort keys videos.Store.List accepts. newest/oldest are
// the default release-date ordering; the added_* pair ranks by when peeq
// fetched the file.
export type VideoSort =
  "newest" | "oldest" | "added_newest" | "added_oldest" | "longest" | "title";

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
  // format_used is the resolved yt-dlp -f selector — what was ASKED FOR, the
  // same string for every video downloaded under one preset. Kept because it
  // is the record of the request, but never shown: the fields below are what
  // the file actually turned out to be.
  format_used?: string;
  // ffprobe's raw values ("mp4", "h264", 1080, "aac"), absent until the file
  // has been probed. codecLabel/resolutionLabel in format.ts do the naming.
  media_container?: string;
  video_codec?: string;
  video_height?: number;
  audio_codec?: string;
  availability: Availability;
  status: VideoStatus;
  error_message?: string;
  watched: boolean;
  watched_at?: string;
  resume_position_seconds: number;
  // state_version is the row's watched-state generation counter. The Player
  // echoes it on every resume POST so a watched toggle made in another tab or
  // on another device can't be undone by a client that never saw it — see
  // setResume in api/videos.ts and issue #97.
  state_version: number;
  favorite: boolean;
  downloaded_at?: string;
  // sponsorblock_segments mirrors httpapi.sponsorblockSegmentDTO, parsed
  // server-side from the stored JSON column. Segments arrive either from the
  // download's own --sponsorblock-mark result or from the backfill worker that
  // reads the SponsorBlock API directly, so an imported video has them too.
  // Absent (undefined, via omitempty) when there are none. Player.tsx skips
  // the [start_time, end_time) ranges whose category is in AUTO_SKIP and marks
  // the rest on the scrubber.
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
  summary_status: SummaryStatus;
  audio_language: string;
  has_subtitles: boolean;
  // category mirrors the Task 7 classification field — always present,
  // "uncategorized" is the fallback (see categories.ts, the TS mirror of
  // backend/internal/videos/category.go).
  category: string;
  // YouTube's own facts about the video, straight from yt-dlp. All optional:
  // they arrive only with downloads made after migration 0009, and nothing
  // backfills older rows. Note `category` above (peeq's classification) and
  // `yt_categories` here (YouTube's labels) are different things.
  media_type?: string;
  live_status?: string;
  yt_tags?: string[];
  yt_categories?: string[];
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

// ActivityEvent mirrors httpapi.activity.Event — one recorded piece of
// automatic background work (the past half of the Activity agenda). subject is
// frozen at write time; outcome is ok|warn|fail.
export type ActivityEvent = {
  id: number;
  at: string;
  kind: string;
  outcome: string;
  subject_id?: string;
  subject?: string;
  summary?: string;
  detail?: string;
};

// UpcomingItem mirrors httpapi.activity.UpcomingItem — one projected future
// task. `at` is an exact instant for scheduled work and absent for an ordered
// (imminent) job; `approx` marks the ordered/estimated ones.
export type UpcomingItem = {
  at?: string;
  kind: string;
  approx: boolean;
  // subject_id identifies what the row is about so the agenda can link it: the
  // channel id on a scan/metadata row, absent on a download/summary row (those
  // name a video, and the agenda links channels only). Mirrors ActivityEvent.
  subject_id?: string;
  subject?: string;
  summary?: string;
};

// Job mirrors httpapi.downloadItem — one download-queue entry, optionally
// joined with its video's title/channel for display.
export type Job = {
  job_id: number;
  video_id: string;
  title?: string;
  channel_name?: string;
  channel_id?: string;
  state: JobState;
  priority: number;
  attempts: number;
  last_error?: string;
  enqueued_at?: string;
};

// SummaryJob mirrors httpapi.summaryItem — one in-flight summary job (pending
// or running), optionally joined with its video's title/channel for display.
// The queue only ever surfaces active jobs; a done/failed job leaves this list.
export type SummaryJob = {
  id: number;
  video_id: string;
  title?: string;
  channel_name?: string;
  channel_id?: string;
  state: SummaryJobState;
  last_error?: string;
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
  cookie_status: CookieStatus;
  cookie_updated_at?: string;
  format_preset: string;
  format_custom: string;
  limit_rate: string;
  throttle_base_seconds: number;
  retention_days: number;
  min_free_gb: number;
  min_video_duration_seconds: number;
  // subtitles_default — the global "start videos with subtitles showing"
  // preference. Written from both the Settings page and the player's own
  // subtitles toggle, which is what makes that toggle sticky across videos.
  subtitles_default: boolean;
  // direct_stream_enabled — opt-in to auth-free playback links, which AirPlay
  // needs: an Apple TV fetches the media URL itself and cannot sign in. Off by
  // default; turning it off revokes every link already handed out.
  direct_stream_enabled: boolean;
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
  subtitles_default: boolean;
  direct_stream_enabled: boolean;
}>;

// PlaybackGrant is what POST /api/videos/{id}/playback-grant returns: an
// auth-free URL for one video and when it stops working. There is no token
// field — unlike a share link there is nothing for the owner to copy, so the
// URL is the only useful form.
export type PlaybackGrant = {
  url: string;
  expires_at: string;
};

// Channel mirrors httpapi.channelItem — one listed channel, joined with
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
  // auto_summary is whether peeq reads this channel's new videos — fetching
  // their captions and writing a summary — before you decide on them. It lives
  // on the channel rather than the subscription, so unlike autodownload it is
  // meaningful on an unsubscribed row.
  auto_summary: boolean;
  format_override?: string;
  pending_count: number;
  downloaded_count: number;
  // added is true when the USER added this channel, false for one listed only
  // because the library holds a video downloaded from it. It is what lets the
  // filter chips tell "Not subscribed" (added, no subscription) apart from
  // "From downloads" (never added) off a single unfiltered list — the same
  // distinction channels.Store.List's ?filter= clauses make.
  added: boolean;
  // has_avatar/has_banner tell a list row whether channel art exists, so it can
  // point an <img> at /api/channels/{id}/avatar|banner or fall back to a
  // gradient — same presence flags ChannelDetail carries, now on the list too.
  has_avatar?: boolean;
  has_banner?: boolean;
  dormant: boolean;
  last_video_at?: string;
  // first_seen_at is when peeq first created the channel row — what the list's
  // "Recently added" ordering sorts on. NOT the same as ChannelDetail.added_at,
  // which is when the USER added the channel: a "From downloads" channel was
  // never added, and sorting on that would collapse every one of them to the
  // bottom. omitempty on the wire, so the sort treats a missing value as "" and
  // falls through to the name tiebreak.
  first_seen_at?: string;
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
  // published_at is YYYY-MM-DD, absent when the scan never learned a date (an
  // older ledger row, healed on that channel's next scan). It is yt-dlp's
  // approximate tab date — a day coarser than the exact upload_date a
  // downloaded video carries.
  published_at?: string;
  // discovered_at is when the scan first saw the upload: a sort fallback
  // only, never rendered as a publish date.
  discovered_at: string;
  // summary_status is the video's summary lifecycle, or "" when peeq has not
  // created a row for it yet. auto_summary is whether its channel is opted in
  // to being read at all.
  //
  // The card needs both, because "" alone is ambiguous: on an opted-in channel
  // it means the caption fetcher has not reached this video yet, and on an
  // opted-out one it means it never will. Those are different cards — a
  // progress marker versus nothing at all.
  summary_status: SummaryStatus | "";
  auto_summary: boolean;
  // has_subtitles is whether captions are on disk. 'no_transcript' covers both
  // "YouTube had none" and "they turned out to be music"; only the second
  // leaves a transcript to read, and this is what tells them apart.
  has_subtitles: boolean;
};

// CookieHealth mirrors httpapi.cookieHealthResponse — distinct from
// Settings.cookie_status: this is the dedicated health-check shape used by
// the rail's cookie status indicator.
export type CookieHealth = {
  status: CookieStatus;
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

// ChannelDetail mirrors httpapi.channelDetail — one channel's page data.
// Added is false for a channel the user has only visited; when it is false
// every subscription field below is at its zero value.
export type ChannelDetail = {
  id: string;
  name: string;
  handle?: string;
  description?: string;
  has_avatar: boolean;
  has_banner: boolean;

  // What YouTube publishes, as of the last successful refresh. subscribers is
  // absent when unknown (hidden by the channel, or never read) — YouTube does
  // not report a count of 0, so an absent value means "unknown" and has to
  // render as "—" rather than a number.
  subscribers?: number;
  verified: boolean;
  // resolved_at is when peeq last TRIED to read the channel from YouTube;
  // resolve_ok says whether that attempt worked. resolved_at set with
  // resolve_ok false is the stuck case — no avatar, no banner, no
  // description, and no retry until someone presses Refresh.
  resolved_at?: string;
  resolve_ok: boolean;
  // gone: peeq auto-unsubscribed this channel because YouTube reported it
  // deleted. The archived videos are unaffected.
  gone: boolean;

  added: boolean;
  // added_at is when the USER added this channel — the timestamp the page
  // shows as "Added <date>". Distinct from Channel.first_seen_at on the list
  // DTO, which is merely when peeq created the row.
  added_at?: string;

  archived_count: number;
  runtime_seconds: number;
  disk_bytes: number;
  newest_published_at?: string;

  subscribed: boolean;
  autodownload: boolean;
  format_override?: string;
  last_scanned_at?: string;
  next_scan_at?: string;
  pending_count: number;
};

// ScanResult mirrors POST /api/channels/{id}/scan. "blocked" carries a
// human-readable reason the scan cannot run — a stale cookie or the global
// YouTube pause — which the UI shows verbatim.
export type ScanResult = { status: "scheduled" | "blocked"; reason?: string };
