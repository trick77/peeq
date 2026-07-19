# peeq Phase 2 — Channels & Subscriptions — Design

**Status:** approved design (brainstorm) — precedes the TDD implementation plan.
**Builds on:** Phase 1 (core watch-and-download loop). Branch `feat/phase-2-channels` off `master`
after PR #1 (Phase 1) merges.

## Goal

Turn peeq from "paste a link, download one video" into "follow channels": **track** a channel (record
its identity, take no action) vs **subscribe** to it (scan once/24h for genuinely new uploads). New
uploads on **autodownload** channels enqueue automatically at low priority; otherwise they land in a
**New & pending** list where each item can be **Downloaded now** (manual priority) or **Ignored**.
Single-user, same architecture as Phase 1 — one Go binary, one SQLite file, the existing yt-dlp
Runner (cookie gate + throttle) and download worker reused, plus one new scan-scheduler goroutine.

## Hard invariants carried from Phase 1 (unchanged)

- **No YouTube call without a valid cookie.** The two new wrapper methods funnel through the same
  `exec` choke point, so the cookie gate applies before any network action.
- **20s minimum throttle floor + random jitter on every YouTube call**, enforced by the Runner's
  single exec path. The scan scheduler adds a **≥60s between-channel** spacing on top of that.
- Module `github.com/trick77/peeq`, Go 1.25, `CGO_ENABLED=0`. Conventional Commits. YAML `.yaml`.
  English comments only. TDD (failing test first). Every DB-writing access goes through a store
  method. Automated tests use a fake yt-dlp stub — never the real binary.

## Scope decisions (locked in brainstorming)

- **Tracked but not subscribed = pure marker.** A tracked channel records identity only: no scanning,
  no video listing, no pending items. It exists so you can later flip Subscribe.
- **Channels UI = a new "Channels" rail item** (under the "Collect" section, beside "New & pending").
- **Unified paste box + auto-track.** The Add box detects channel vs. video URLs and routes
  accordingly. Pasting a *video* also silently **tracks** its channel (free — the video's `-J`
  metadata already carries `channel_id` + `channel_name`; no extra YouTube call).
- **Baseline on the next scheduled tick.** Subscribing just marks the channel and sets its
  `next_scan_at` so the scheduler establishes the first-run baseline on its next pass (no immediate
  YouTube call at subscribe time).
- **Delete a channel = full cascade** (in P2): cancel its jobs, unlink its media files, delete its
  video rows, its ledger rows, its subscription, and the channel row. Destructive → UI confirm.
- **Min-duration filter is a configurable setting** (`min_video_duration_seconds`, default 180), with
  a Settings control.
- **Squashed migrations.** Until peeq ships, all schema lives in a **single** `0001_init.sql`
  (Phase 1's `0002_auth.sql` is folded in and deleted, and the P2 tables + settings column are added
  there). Dev DBs are disposable and must be recreated (`rm ./data/peeq.db`) after this change. Once
  peeq is deployed, migrations return to append-only.

## Explicitly deferred to the NEXT phase (recorded, not built in P2)

- **Auto-unsubscribe stale channels:** when a subscribed channel publishes no video for a configurable
  number of weeks (default 3 months), automatically unsubscribe it.
- **Stale-channel filter** on the Channels page to surface such channels.

## Out of scope for P2 (Phase 3, unchanged)

Subtitles, summaries, embeddings, transcript/semantic search, and in-player caption display. P2 must
not build any of these. Autodownloaded channel videos are ordinary downloads (no subtitles) in P2.

---

## Data model — single squashed `0001_init.sql`

The squashed file contains the Phase-1 tables (`settings`, `videos`, `download_jobs`, `users`,
`sessions`) unchanged **except** for one new `settings` column, plus three new P2 tables. `videos` is
untouched (its `status` CHECK constraint stays as-is; scan/pending state lives in the ledger, not on
`videos`).

### New `settings` column

- `min_video_duration_seconds INTEGER NOT NULL DEFAULT 180` — scanned videos shorter than this are
  filtered out (recorded as `seen`, never offered). Validated `>= 0` by the settings API.

### `channels` — identity; **presence = "tracked"**

| column        | type | notes |
|---------------|------|-------|
| `id`          | TEXT PRIMARY KEY | the channel UCID (`UC…`) |
| `handle`      | TEXT | `@handle` if known, else empty |
| `name`        | TEXT | display name |
| `avatar_path` | TEXT | reserved, unused in P2 |
| `added_at`    | TEXT NOT NULL DEFAULT (datetime('now')) | |

### `subscriptions` — **presence = "subscribed"**; one row per subscribed channel

| column            | type | notes |
|-------------------|------|-------|
| `channel_id`      | TEXT PRIMARY KEY REFERENCES channels(id) ON DELETE CASCADE | |
| `autodownload`    | INTEGER NOT NULL DEFAULT 0 | 1 → new uploads enqueue automatically |
| `format_override` | TEXT | per-channel yt-dlp format string; empty → use global preset |
| `baselined_at`    | TEXT | NULL until the first-run baseline scan completes |
| `last_scanned_at` | TEXT | last successful scan start |
| `next_scan_at`    | TEXT NOT NULL | when this channel is next eligible; set to `now` on subscribe |
| `created_at`      | TEXT NOT NULL DEFAULT (datetime('now')) | |

### `channel_videos` — per-channel **scan ledger + pending list**

The authoritative "video ids we've seen from scans" set (the first-run baseline id-set), the pending
queue, and the dedup source.

| column             | type | notes |
|--------------------|------|-------|
| `video_id`         | TEXT PRIMARY KEY | a YouTube id belongs to one channel |
| `channel_id`       | TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE | |
| `title`            | TEXT | from the flat listing |
| `duration_seconds` | INTEGER | from the flat listing (for the min-duration filter) |
| `url`              | TEXT | canonical watch URL |
| `thumbnail_url`    | TEXT | from the flat listing (remote URL; no download in P2) |
| `state`            | TEXT NOT NULL CHECK (state IN ('seen','pending','ignored','queued')) | see below |
| `discovered_at`    | TEXT NOT NULL DEFAULT (datetime('now')) | |
| `decided_at`       | TEXT | when the user/auto acted (pending → queued/ignored) |

Indexes: `idx_channel_videos_channel (channel_id)`, `idx_channel_videos_state (state)`.

**`state` semantics:**
- `seen` — baseline entry, or a post-baseline entry filtered out (too short / upcoming / live).
  Recorded for dedup; no action; never shown.
- `pending` — post-baseline, passed filters, on a **non-autodownload** channel → shows in
  "New & pending".
- `queued` — sent to the download queue (auto on an autodownload channel, or via "Download now").
- `ignored` — user dismissed a pending item. Never shown; kept for dedup.

**Dedup rule:** a scanned id is acted on only if it exists in **neither** `channel_videos` **nor**
`videos`. (A manually-added video that a channel later lists must not be re-offered.)

---

## yt-dlp wrapper additions (`internal/ytdlp/`)

Both new methods go through the existing `exec`/`execWithProgress` choke point, inheriting the cookie
gate and the 20s+ throttle. No parallel exec path is introduced.

- `url.Canonicalize` gains a **`channel`** kind, recognizing `/channel/UC…`, `/@handle`, `/c/<name>`,
  and `/user/<name>` (returns the canonical channel URL + a channel identifier that may be a handle
  when the UCID isn't in the URL).
- `ResolveChannel(ctx, channelURL string) (ucid, name string, err error)` — runs
  `--flat-playlist --playlist-items 0 <channelURL>` (metadata only, no entries) to resolve a
  handle/custom URL to a UCID + display name. One throttled call, made only on **explicit** channel
  add. `/channel/UC…` URLs already carry the UCID but still resolve the name.
- `ChannelVideos(ctx, ucid string, n int) ([]ChannelEntry, error)` — runs
  `--flat-playlist -J --playlist-items ":N:1" https://www.youtube.com/channel/{ucid}/videos`.
  Querying the **`/videos` tab only** means shorts and livestreams (separate tabs) are excluded by
  construction. `ChannelEntry{ ID, Title, URL, DurationSeconds, ThumbnailURL, LiveStatus }` — parsed
  from the flat entries. `n` is a fixed constant (see scheduler) large enough that a channel cannot
  post more than `n` videos between daily scans.

Filters applied by the **caller** (scheduler) to each entry: drop `DurationSeconds > 0 &&
DurationSeconds < settings.min_video_duration_seconds`; drop `LiveStatus` in
{`is_upcoming`, `is_live`}. (Shorts/finished-streams already excluded by tab choice.)

---

## Scan scheduler goroutine (`internal/scan/scheduler.go`)

Mirrors the download worker's `Run(ctx)` structure: a single serial goroutine, `recover()` per
channel, ctx-cancel graceful shutdown, cookie-gated. It never downloads — it only discovers and
routes; the **existing download worker** performs downloads.

Constants (named, not settings in P2): scan interval `24h`, interval jitter `±3h`, between-channel
spacing `60s`, listing size `N = 50`.

Loop each tick:
1. If ctx done → return. If no valid cookie (`settings.cookie_status != "valid"`) → skip this pass
   (sleep a beat; do not hammer), mirroring the worker's pause behavior.
2. Claim the **one** due subscribed channel: `next_scan_at <= now`, oldest `next_scan_at` first. None
   due → sleep a beat.
3. Enforce ≥60s since the previous channel-scan start (sleep the remainder if needed).
4. `ChannelVideos(ctx, ucid, N)`. On error → log, back off (`next_scan_at = now + ~1h`), leave
   `baselined_at` unchanged, continue.
5. For each returned entry, in listing order:
   - **Dedup:** skip if `video_id` already in `channel_videos` or `videos`.
   - **First-run baseline** (`baselined_at IS NULL`): insert every new id as `seen`. Queue nothing.
   - **Subsequent scans:** apply filters. Filtered out → insert `seen`. Passed:
     - autodownload channel → insert `queued`, `videos.Upsert` the row (status stays `new` → the
       worker sets `queued`/`downloading`), `jobs.Enqueue(video_id, priority=0)`.
     - non-autodownload → insert `pending`.
6. After a successful scan: set `last_scanned_at = now`; if this was the baseline, set
   `baselined_at = now`; set `next_scan_at = now + 24h ± jitter`.

**Reuse:** enqueue goes through the existing `jobs.Store` and is drained by the existing
`download.Worker` (priority 0 = below manual 10, per Phase 1). No second worker, no second exec path.

---

## HTTP API (`internal/httpapi/`)

New handlers file `channels_handlers.go`; small additions to `videos_handlers.go` for pending
actions; routes registered in `server.go`.

- `POST /api/channels` `{url, subscribe?:bool}` — canonicalize (must be `channel` kind), resolve
  UCID+name (`ResolveChannel`), upsert the `channels` row (track). If `subscribe` is true, also create
  the subscription (`next_scan_at = now`). No-cookie → 409. Idempotent on an already-tracked channel.
- `GET /api/channels?filter=all|subscribed|tracked` — each item: id, handle, name, subscribed,
  autodownload, format_override, pending count, downloaded count.
- `PUT /api/channels/{id}` `{autodownload?, format_override?}` — update subscription config
  (400 if the channel isn't subscribed).
- `POST /api/channels/{id}/subscribe` — create the subscription (`next_scan_at = now`); idempotent.
- `POST /api/channels/{id}/unsubscribe` — delete the subscription row; the channel stays tracked.
- `DELETE /api/channels/{id}` — **full cascade** (see store method below).
- `GET /api/videos?filter=pending` (reads `channel_videos` where `state='pending'`, newest first),
  or an explicit `GET /api/pending` returning the same — one canonical route, chosen at plan time.
- `POST /api/videos/{id}/download` — enqueue an existing ledger/video row at **manual priority (10)**:
  `videos.Upsert` (from the ledger row) + `jobs.Enqueue(id, 10)` + ledger `state='queued'`,
  `decided_at=now`. Also serves tombstone re-download later.
- `POST /api/videos/{id}/ignore` — set the ledger row `state='ignored'`, `decided_at=now`.

**Auto-track hook:** the existing `POST /api/downloads` handler, after `Metadata` succeeds, upserts a
`channels` row from `channel_id`/`channel_name` (track only, no subscription) — reusing already-fetched
data, no extra YouTube call.

---

## Store methods

- `channels.Store`: `Upsert(Channel)`, `Get(id)`, `List(filter)` (with pending/downloaded counts),
  `DeleteCascade(id)`.
- `subscriptions.Store` (may live in the same package): `Subscribe(channelID)` /
  `Unsubscribe(channelID)`, `UpdateConfig(channelID, autodownload, formatOverride)`,
  `ClaimDue(now) (*Subscription, error)` (oldest `next_scan_at <= now`),
  `MarkScanned(channelID, baseline bool, nextScanAt)`, `Backoff(channelID, nextScanAt)`.
- `channelvideos.Store` (ledger): `Exists(videoID)`, `Insert(entry, state)`,
  `SetState(videoID, state)`, `ListPending()`, counts by channel.
- `DeleteCascade(id)` orchestration (single store method or a service): collect the channel's
  `video_id`s (from `videos` where `channel_id=id`), cancel any of their pending/running jobs, unlink
  their media/thumbnail files (path-safe, reuse the Phase-1 media helper), delete those `videos` rows
  (FK cascades `download_jobs`), then FK-cascade deletes `channel_videos` and `subscriptions` via the
  `channels` row delete. Running-job cancel goes through the worker's `Cancel` so a live child is
  killed.

---

## Frontend (`ui/`)

- **Channels view** (`views/Channels.tsx`) — new "Channels" rail item. Filter chips
  (All / Subscribed / Tracked). Per channel: name/handle, Subscribe toggle, Autodownload toggle,
  format-override field, pending/downloaded counts, and a **Delete** button behind a confirm
  ("removes all downloaded videos from this channel").
- **Add view** — the paste box now detects channel URLs (via the `channel` canonicalize kind) and
  posts to `/api/channels`; video URLs still post to `/api/downloads` (which auto-tracks the channel).
  A small hint distinguishes "added a video" vs "tracked a channel".
- **New & pending view** (nav already stubbed in Phase 1) — grid of pending items, each showing
  thumbnail/title/duration/channel with **Download now** and **Ignore** actions; the rail badge count
  reflects `state='pending'`.
- **Settings view** — add the `min_video_duration_seconds` control (number input, minutes or seconds)
  alongside the existing throttle/retention controls.
- API client: `api/channels.ts` (+ types); `api/videos.ts` gains `download(id)` and `ignore(id)`;
  pending fetch.

---

## Testing (TDD, fake yt-dlp stub)

- **Wrapper:** `ChannelVideos` flat-JSON parse (id/title/duration/thumbnail/live_status);
  `ResolveChannel` returns UCID+name; `Canonicalize` `channel` kind for `/channel`, `/@handle`,
  `/c/`, `/user/`; **cookie gate** — both new methods return `ErrNoCookie` without touching the binary
  when the cookie is absent.
- **Scheduler:** first-run baseline records `seen` and **queues nothing**; a subsequent genuinely-new
  id on a non-autodownload channel → `pending`; on an autodownload channel → `queued` +
  `jobs.Enqueue(priority 0)`; filters drop `<min_duration` and `is_upcoming`/`is_live`; dedup skips
  ids already in `channel_videos` or `videos`; ≥60s between-channel spacing; no-cookie → no scan.
- **Stores:** track/upsert; subscribe/unsubscribe (channel stays tracked); config update;
  `ClaimDue` ordering; `DeleteCascade` removes media + video rows + jobs + ledger + subscription +
  channel and leaves nothing behind.
- **API:** `POST /api/channels` tracks (+ optional subscribe), 409 without cookie; pending
  download/ignore transitions; auto-track on video add; delete-cascade endpoint.
- **UI:** Channels list + filters + toggles + delete confirm; Add routes by kind; pending
  Download/Ignore; Settings min-duration round-trip.

## Process (per the Phase-1 workflow)

Branch `feat/phase-2-channels` off `master` (after PR #1 merges). Subagent-driven development: a fresh
implementer per task; **every returning subagent result validated by a reviewer on the Fable model**;
Critical/Important findings fixed before closing a task, minors recorded in the SDD ledger. End with a
holistic whole-branch review on Fable, then `finishing-a-development-branch`. Keep CI green
(backend + UI, `-race`). PR to `master`.
