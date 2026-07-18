# vark

Self-hosted YouTube archiver. Go backend (JSON API + embedded React SPA), single SQLite file, no
external services required.

## Run

```bash
cp .env.example .env   # fill in the values, especially VARK_SESSION_SECRET and the OIDC block
docker compose up -d --build
```

This starts a single hardened `vark` container (non-root, read-only rootfs, all capabilities
dropped) behind an external Traefik network — see `compose.yaml`. Data (SQLite DB, downloaded
media, the yt-dlp binary) lives under `./data`.

For local development without OIDC (dev auto-login, host networking):

```bash
docker compose -f compose.dev.yaml up --build
```

or natively:

```bash
make dev   # backend on 127.0.0.1:8080 + Vite dev server proxying /api
```

See `AGENTS.md` for conventions, locked technical choices, and commands.

## Two hard invariants

- **No YouTube call ever fires without a valid cookie.** Every yt-dlp invocation (metadata fetch,
  download, self-update excepted) goes through a cookie gate first; with no cookie configured,
  `POST /api/downloads` is refused with `409`. This is enforced in code, not just documented —
  there is no way to bypass it from the UI or API.
- **Every YouTube request is throttled.** A random 20s+ delay (configurable floor, hard-clamped to
  a 20s minimum) is inserted before each yt-dlp call, to keep request patterns well away from
  anything that looks like scraping.

## Database backup

`VARK_DB_PATH` (default `/data/vark.db`) is the single source of truth for everything that isn't
the media file itself: watch/resume position, favorites, tombstones, the download queue, and
settings (including the stored cookie). **Back this file up.** If it's lost, re-downloading the
media does not restore any of that state — favorited and watched videos, resume positions, and
tombstoned entries are gone for good. SQLite runs in WAL mode; use `sqlite3 vark.db ".backup
backup.db"` (or a continuous tool like Litestream) rather than copying the file directly while the
process is running.

## Legal note

Cookie-authenticated automated downloading violates YouTube's Terms of Service and ties every
request to the Google account whose cookie you configured — use a throwaway account, not your
main one. The built-in throttling (20s+ randomized delay per request) reduces, but does not
eliminate, the risk of that account or its source IP getting rate-limited or blocked.
