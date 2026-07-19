# peeq

Self-hosted YouTube archiver. Go backend (JSON API + embedded React SPA), single SQLite file, no
external services required.

## Run

```bash
cp .env.example .env   # fill in the values, especially BACKEND_SESSION_SECRET and the OIDC block
docker compose up -d --build
```

This starts a single hardened `peeq` container (non-root, read-only rootfs, all capabilities
dropped) behind an external Traefik network — see `compose.yaml`. The SQLite DB and the yt-dlp
binary live under `./data`; downloaded media lives on its own bind mount (`/mnt/ark/peeq` by
default — adjust to your host).

For local development without OIDC (dev auto-login, host networking):

```bash
docker compose -f compose.dev.yaml up --build
```

or natively:

```bash
make dev   # backend on 127.0.0.1:8080 + Vite dev server proxying /api
```

See `AGENTS.md` for conventions, locked technical choices, and commands.

### Phase 2 upgrade note

Phase 2 (channels & subscriptions) squashed all migrations into a single `0001_init.sql`, so an
existing dev database predating this change won't pick up the new tables on startup. Delete it and
let peeq re-migrate from scratch:

```bash
rm ./data/peeq.db*
```

(This is a dev-only concern — a fresh volume/deploy just migrates cleanly.)

## Channels & subscriptions

Adding a channel URL or `@handle` **tracks** it (it shows up under Channels, but nothing is
downloaded automatically). **Subscribing** to a tracked channel opts it into the daily scan, which
looks for new uploads and either lists them under **Pending** for a manual decision, or — if
the channel has **autodownload** enabled — enqueues them automatically (optionally with a
per-channel format override). A channel's first scan only records a baseline (its current videos)
and queues nothing, so subscribing never triggers a bulk backfill. The scan itself respects the
same throttle as everything else: at least 60s between channels and a 20s+ randomized delay per
yt-dlp call, so a large subscription list is scanned gradually rather than in a burst.

## Two hard invariants

- **No YouTube call ever fires without a valid cookie.** Every yt-dlp invocation (metadata fetch,
  download, self-update excepted) goes through a cookie gate first; with no cookie configured,
  `POST /api/downloads` is refused with `409`. This is enforced in code, not just documented —
  there is no way to bypass it from the UI or API.
- **Every YouTube request is throttled.** A random 20s+ delay (configurable floor, hard-clamped to
  a 20s minimum) is inserted before each yt-dlp call, to keep request patterns well away from
  anything that looks like scraping.

## Database backup

`BACKEND_DB_PATH` (default `/data/peeq.db`) is the single source of truth for everything that isn't
the media file itself: watch/resume position, favorites, tombstones, the download queue, and
settings (including the stored cookie). **Back this file up.** If it's lost, re-downloading the
media does not restore any of that state — favorited and watched videos, resume positions, and
tombstoned entries are gone for good. SQLite runs in WAL mode; use `sqlite3 peeq.db ".backup
backup.db"` (or a continuous tool like Litestream) rather than copying the file directly while the
process is running.

## Manual verification checklist (channels & subscriptions)

The full channels/subscriptions flow needs a real YouTube cookie and is not covered by automated
tests. Run this checklist by hand after any change that touches channels, scanning, or the
download pipeline:

1. Boot with a real DB/media dir; sign in; paste a real cookie in Settings and confirm status
   shows "active".
2. **Add** a channel by `@handle` under Channels — it should appear tracked with the resolved
   channel name. **Subscribe** to it.
3. Wait for (or force) the scheduler's first pass on that channel — it should show `baselined_at`
   set and queue **nothing** (first-run baseline, no backfill).
4. Once a genuinely new upload exists on a subscribed, non-autodownload channel, confirm it lands
   in **Pending** on the next scan. **Download now** should enqueue it (progress visible in
   the dock) and it should land in Library as `downloaded`. **Ignore** on another pending item
   should remove it from the list.
5. Flip a channel to **Autodownload** with a format override — the next new upload on that channel
   should enqueue automatically at low priority and download using the override format.
6. Paste a **video** URL (not a channel URL) into Add — confirm its channel is silently tracked
   (appears under Channels → Tracked) without being subscribed.
7. **Delete** a channel that has a downloaded, favorited video — confirm the video row and its
   media file are both gone (cascade delete overrides the favorite), and that deleting a channel
   mid-download cancels the running job for that channel.
8. With multiple subscribed channels, watch the logs across a scan cycle and confirm at least 60s
   between channels and a 20s+ delay per yt-dlp call.

## Legal note

Cookie-authenticated automated downloading violates YouTube's Terms of Service and ties every
request to the Google account whose cookie you configured — use a throwaway account, not your
main one. The built-in throttling (20s+ randomized delay per request) reduces, but does not
eliminate, the risk of that account or its source IP getting rate-limited or blocked.
