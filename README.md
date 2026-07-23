# peeq

Self-hosted YouTube archiver. A Go backend serving a JSON API and an embedded React SPA, with a
single SQLite file for state — no database server, no queue, no external services beyond the two
AI endpoints described below.

## What it does

**Channels.** Adding a channel URL or `@handle` *tracks* it: it appears under Channels, but nothing
is downloaded. *Subscribing* opts it into the daily scan for new uploads. New uploads either land
under **Decide** for a manual keep/ignore call, or — if the channel has **autodownload** on — are
enqueued automatically, optionally with a per-channel format override. A channel's first scan only records a
baseline of its current videos and queues nothing, so subscribing never triggers a bulk backfill.

**Captions and AI artifacts.** Downloaded videos get their captions extracted, and from those peeq
generates a summary, chapters, and highlights. Any video can be re-summarized on demand.

**Search.** One search box over every transcript and summary. Queries run as keyword search (SQLite
FTS5) *and* semantic search (vector similarity) and are fused into a single ranked list, with
results linking to the timestamp inside the video. Keyword search needs no external service, so if
the embeddings endpoint is unreachable, search degrades to keyword-only with a logged warning
rather than failing.

**Categories.** Each video is classified into one of fifteen categories (AI, Science, Gaming,
History, …) on a best-effort basis. The Library has a category filter that composes with the other
filters.

**Recovery.** A failed download can be retried per video from the Library, the player, or a video
card, without re-adding it.

## Requirements

- Two OpenAI-compatible HTTP endpoints: one **chat** endpoint (summaries, chapters, highlights)
  and one **embeddings** endpoint (indexing and semantic search). Self-hosted or commercial —
  peeq only needs the OpenAI wire format.
- A **YouTube cookie**, supplied by the companion browser extension. peeq will not touch YouTube
  without one; see [YouTube access](#youtube-access).
- For production, an external Traefik reverse proxy terminating TLS, and an OIDC provider.

## Running it

```bash
cp .env.example .env   # fill it in — see Configuration
docker compose up -d
```

This runs a single hardened `peeq` container (non-root, read-only rootfs, all capabilities
dropped) on an external `traefik` network. Create that network first if it does not exist
(`docker network create traefik`) and edit the `Host()` rule in `compose.yaml` to your domain.
The SQLite DB and the self-updating yt-dlp binary live under `./data`; media lives on its own
bind mount, `/mnt/ark/peeq` by default — adjust it to your host.

For local development without OIDC, which auto-signs you in as a fixed dev admin and binds to
loopback only:

```bash
docker compose -f compose.dev.yaml up --build
```

Or natively, with the backend on `127.0.0.1:8080` and a Vite dev server proxying `/api` to it:

```bash
make dev
```

`make dev` keeps its database at `/tmp/peeq-dev.db` rather than under `./data`.

See `AGENTS.md` for conventions and the full command list.

## Configuration

Everything is read from `BACKEND_*` environment variables. `.env.example` is the exhaustive list,
annotated with defaults; the tables below cover what matters most.

**Required — peeq refuses to start without these:**

| Variable | Purpose |
| --- | --- |
| `BACKEND_SESSION_SECRET` | Signs session cookies. Generate with `openssl rand -hex 32`. |
| `BACKEND_AUTH_MODE` | `oidc` or `dev`. No default. |
| `BACKEND_CHAT_BASE_URL` | Chat endpoint, e.g. `http://localhost:8000/v1`. |
| `BACKEND_EMBED_BASE_URL` | Embeddings endpoint, e.g. `http://localhost:8001/v1`. |
| `BACKEND_EMBED_MODEL` | Embedding model name, e.g. `text-embedding-3-small`. |

**Required when `BACKEND_AUTH_MODE=oidc`:** `BACKEND_OIDC_ISSUER`, `BACKEND_OIDC_CLIENT_ID`,
`BACKEND_OIDC_CLIENT_SECRET`, `BACKEND_OIDC_REDIRECT_URL`.

**Optional, with defaults:**

| Variable | Default | Notes |
| --- | --- | --- |
| `BACKEND_PUBLIC_URL` | *(empty)* | Externally reachable base URL. Not enforced at boot, but OIDC redirect and logout URLs are built from it, so OIDC will not work without it. |
| `BACKEND_ADDR` | `:8080` | Dev auth requires a loopback address here. |
| `BACKEND_DB_PATH` | `/data/peeq.db` | |
| `BACKEND_MEDIA_DIR` | `/data/media` | Written as `<dir>/<channel>/<video>/`. |
| `BACKEND_YTDLP_DIR` | `/data/bin` | Where yt-dlp is fetched and self-updates. |
| `BACKEND_CHAT_API_KEY` | *(empty)* | Omit for an endpoint that needs no auth. |
| `BACKEND_EMBED_API_KEY` | *(empty)* | Omit for an endpoint that needs no auth. |
| `BACKEND_EMBED_DIM` | `1536` | Must match the model's real output dimension. Changing it later requires recreating the database — see [Database](#database). |
| `BACKEND_DEFAULT_SUB_LANG` | `en` | Fallback subtitle language when none is detected. |
| `BACKEND_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error`. |

## YouTube access

peeq needs a YouTube sign-in to download videos. YouTube rotates account cookies on open YouTube
browser tabs, so a cookie exported from the profile you browse with dies quickly. The fix is a
profile that never browses.

Once:

1. Create a dedicated Chrome profile and sign it into a dedicated YouTube account (not your
   personal one — this isolates any rate-limit or block).
2. In that tab, navigate to `https://www.youtube.com/robots.txt`, then close the tab. This stops a
   YouTube app page from rotating the cookie underneath you.
3. In peeq: Settings → Access token → create one and copy it.
4. Install the extension in that profile at `chrome://extensions` (Developer mode → Load unpacked),
   pointing it at either:
   - the `extension/` directory of a checkout, or
   - the unzipped `peeq-companion-<version>.zip` from a
     [release](https://github.com/trick77/peeq/releases) — the same build, no checkout needed.

   Then open its options, paste peeq's address and the token, and allow the permission prompt.

   The extension is deliberately **not** on the Chrome Web Store: it talks to one self-hosted
   server, so the store's discovery and auto-update buy nothing, while `cookies` plus a
   user-configured host permission would make for a long and uncertain review. Keep a checkout at a
   stable path — Chrome derives the extension ID from it, so moving the directory registers it as a
   new extension and the address and token must be re-entered.

Whenever peeq reports the cookie is no longer valid:

1. Open the dedicated profile.
2. Click the extension → **Send cookie to peeq**.
3. Close the profile.

Do not browse YouTube in that profile — that is what starts rotation.

### JavaScript runtime (deno)

yt-dlp has to run YouTube's player JavaScript to solve the `sig` and `n` challenges that gate
stream URLs. Without a runtime it falls back to a deprecated path where formats silently go
missing and transfers are throttled, so the runtime image ships deno, copied from deno's
official image and pinned by digest.

deno is the only runtime yt-dlp enables by default, so it is picked up automatically from
`PATH` — no `--js-runtimes` flag and no configuration. peeq logs which runtime it found at
boot:

    yt-dlp JavaScript runtime detected  runtime=deno-2.9.3

To bump deno, change **both** the tag and the digest in `backend/Containerfile`. Local dev on a
host without deno logs a warning and keeps working, with extraction on the same deprecated path
production used to be on — `brew install deno` if you want dev to match production.

## Safety rails

Three mechanisms constrain how peeq talks to YouTube. All are enforced in code, not merely
documented.

- **No YouTube call fires without a valid cookie.** Every yt-dlp invocation (self-update excepted)
  passes a cookie gate first. With no cookie configured, `POST /api/downloads` is refused with
  `409`. There is no way to bypass this from the UI or the API. There is one env-level escape
  hatch, `BACKEND_ALLOW_ANONYMOUS_YOUTUBE`, which exists so local testing can work around YouTube
  serving no usable formats to authenticated requests; it is a hard boot error unless
  `BACKEND_AUTH_MODE=dev`, which is itself loopback-only. Never enable it in production.
- **Every YouTube request is throttled.** A randomized delay — configurable floor, hard-clamped to
  a 20s minimum — precedes each yt-dlp call, and channel scans additionally wait at least 60s
  between channels. A large subscription list is scanned gradually rather than in a burst.
- **A kill-switch stops all YouTube activity.** You can pause everything from Settings. peeq also
  engages the pause automatically when enough *distinct* videos or channels fail in a row with
  extractor or rate-limit errors — the signal that the extractor is broken generally rather than
  that one video is bad. Any success resets the streak. While paused, downloads and scans stop;
  resume from Settings.

## Database

`BACKEND_DB_PATH` (default `/data/peeq.db`) is the single source of truth for everything that is
not the media file itself: watch and resume positions, favorites, tombstones, categories, the
download queue, transcripts and their embeddings, and settings including the stored cookie.

**Back this file up.** If it is lost, re-downloading the media restores none of that state.
SQLite runs in WAL mode, so use `sqlite3 peeq.db ".backup backup.db"` — or a continuous tool like
Litestream — rather than copying the file while the process is running.

**Upgrades.** Migrations are append-only: each schema change is a new numbered file under
`backend/internal/store/migrations/`, and the runner applies pending ones in order and records
them. Existing databases upgrade in place — no reset needed.

Two exceptions:

- A database created before **0.0.11** predates the append-only rule, back when the initial
  migration was still being rewritten in place. The runner will not re-apply it, so such a
  database silently lacks later columns and tables. Recreate it.
- Changing `BACKEND_EMBED_DIM` requires recreating the database regardless, because the
  `vec_chunks` table's dimension is fixed at DDL time.

In both cases:

```bash
rm ./data/peeq.db*        # or /tmp/peeq-dev.db* for `make dev`
```

## Legal note

Cookie-authenticated automated downloading violates YouTube's Terms of Service and ties every
request to the Google account whose cookie you configured — use a throwaway account, not your main
one. The built-in throttling reduces, but does not eliminate, the risk of that account or its
source IP getting rate-limited or blocked.
