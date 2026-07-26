# peeq

Self-hosted YouTube watch pipeline: triage, download, summarize, watch.

## What it does

**Nothing downloads without a reason.** Subscribe to a channel and peeq checks it daily. New
uploads land under **Decide**, where you keep or ignore each one — or, for channels you always
want, download themselves. A channel's first scan only takes a baseline, so subscribing never
dumps a back catalogue on you.

**Every video arrives explained.** Captions become a summary, chapters and highlights, so you can
tell whether a 45-minute video is worth 45 minutes before you start it. Re-summarize any video if
the result is poor.

**Search means what you meant.** One box over every transcript and summary, by keyword *and* by
meaning. Results link straight to the moment inside the video.

**A queue you actually work through.** Up next holds what you plan to watch, History what you did.
Playback position follows you between devices; sponsor segments are skippable.

**The library sorts itself.** Videos are classified into twenty-two categories — AI, Science,
Gaming, History, … — which compose with the watched, in-progress and favorite filters.

**Watched videos clear themselves out** after a window you choose, two weeks by default, so the
library stays what you still mean to watch. Mark a video favorite and it stays for good. Nothing is
really lost either way: a cleared video keeps its summary, category and transcript, stays
searchable, and can be pulled down again whenever you want it back.

**Share a video** on an expiring public link — the summary, chapters, transcript and the video
itself, no account needed. Revoke it whenever.

## Requirements

- **Docker**, and a reverse proxy terminating TLS in front of it.
- **Two OpenAI-compatible endpoints** — one chat (summaries, chapters, highlights), one embeddings
  (search). Self-hosted or commercial; peeq only needs the wire format.
- **An OIDC provider** for sign-in.
- **A YouTube sign-in**, pushed from your browser by the companion extension. peeq will not touch
  YouTube without one — see below.

## Running it

```bash
cp .env.example .env   # every setting, annotated with its default
docker compose up -d
```

Edit the `Host()` rule in `compose.yaml` to your domain and point the media bind mount at your own
storage. `.env.example` is the complete configuration reference; five settings have no default and
peeq refuses to start without them.

Development setup and the command list live in `AGENTS.md`.

## YouTube sign-in

YouTube rotates account cookies on open YouTube tabs, so a cookie exported from the profile you
browse with dies within days. The fix is a browser profile that never browses.

Once:

1. Create a dedicated Chrome profile and sign it into a **dedicated** YouTube account, not your
   personal one.
2. In that profile, open `https://www.youtube.com/robots.txt` and close the tab. Leaving a real
   YouTube page open is what starts the rotation.
3. In peeq: **Settings → Access token**, create one, copy it.
4. Install the companion extension in that profile at `chrome://extensions` (Developer mode → Load
   unpacked), from either the `extension/` directory of a checkout or the unzipped
   `peeq-companion-<version>.zip` from a [release](https://github.com/trick77/peeq/releases). Open
   its options, paste peeq's address and the token, and accept the permission prompt.

   It is deliberately not on the Chrome Web Store — it talks to exactly one self-hosted server.
   Keep the checkout at a stable path: Chrome derives the extension ID from it, so moving the
   directory registers a new extension and the address and token must be entered again.

Then, whenever peeq says the cookie has expired: open that profile, click the extension → **Send
cookie to peeq**, close the profile. Never browse YouTube there.

## Being a well-behaved client

- No YouTube request is made without a valid cookie. With none configured, downloads are refused
  rather than attempted anonymously.
- Every request is preceded by a randomized delay, and channel scans are spaced out, so a large
  subscription list is worked through gradually instead of in a burst.
- A kill-switch in Settings stops all YouTube activity, and peeq engages it by itself when failures
  start stacking up — the signal that something has broken generally rather than for one video.

## Back up the database

One SQLite file holds everything that is not the media: watch positions, favorites, categories,
transcripts, settings and the stored cookie. Lose it and re-downloading the videos brings none of
that back.

```bash
sqlite3 peeq.db ".backup backup.db"
```

Use that, or a continuous tool like Litestream — the file is in WAL mode, so copying it while peeq
is running is not a backup. Upgrades migrate the database in place; no reset needed.

## Legal note and disclaimer

peeq is not affiliated with, endorsed by, or sponsored by YouTube or Google. "YouTube" is a
trademark of Google LLC, used here only to identify the service peeq interoperates with.

YouTube's Terms of Service restrict automated access and downloading. Every request peeq makes is
tied to the Google account whose cookie you configured — review those terms and any applicable law
in your jurisdiction before running it. Configure a dedicated account rather than your primary one,
so peeq's activity stays separable from your personal account.

The built-in throttling is there to keep peeq a well-behaved client; it does not make automated
access permitted.
