# Peeq — Phase 3: Subtitles, Summaries & Search — Design

**Date:** 2026-07-19
**Status:** Approved (brainstorming complete; UI mockup approved)
**Predecessors:** Phase 1 (core watch-and-download loop, PR #1), Phase 2 (channels &
subscriptions, PR #5), rename vark→peeq (PR #6). All merged to `master`.
**Mockup:** interactive, approved — peeq's Warm Editorial dark tokens; stacked player
intelligence panels + collapsible transcript, CC captions, global semantic search.

---

## 1. Goal & framing

Phase 3 makes Peeq's archive *intelligent*: every downloaded video gets an audio-language
subtitle track, an AI-generated **summary + timestamped chapter index + key-points/highlights**,
and its transcript is embedded for **semantic search** across the whole library. The player fills
in the three intelligence panels that P1 left stubbed ("Coming in a later update") and gains caption
display; a new global search view answers "which video mentioned X?" by meaning, not keywords.

**AI is integral, not a bolt-on.** Summaries and semantic search are core product surfaces, not
optional extras. Peeq **refuses to start** without the MiMo (chat) and embeddings endpoints
configured — the same fail-loud contract as `BACKEND_SESSION_SECRET` and the OIDC vars today.

This slice mirrors the sibling `../loom` app: it ports loom's `llm/client.go` (MiMo chat) and its
entire `rag/` stack (chunk → embed → `vec0` KNN), minus loom's multi-user scoping (Peeq is
single-user). yt-dlp subtitle flags are added at the existing Runner choke point.

### Hard invariants (unchanged from P1/P2)
- **No YouTube/subtitle call without a valid cookie.** Subtitle download is added to the existing
  `download.go` path, which already funnels through the single `Runner.exec` cookie-gate + throttle.
- **20s throttle floor + jitter on every YouTube call** — unchanged; no new YouTube-call path is
  introduced (subtitles ride the existing download invocation).
- **Media path safety** — the new `GET /api/videos/{id}/subtitles` endpoint reuses the same sandbox
  (reject `..` / absolute / symlink-escape) as the P1 stream/thumbnail endpoints.
- **MiMo + embeddings are offline background jobs** — they never block a download and never call
  YouTube. They run in a new ticker-worker mirroring the P1 download worker.
- **Tests use fakes** — httptest fake servers for MiMo/embeddings; the existing `fake-ytdlp.sh` for
  yt-dlp. Never a real LLM, real embeddings endpoint, or the real yt-dlp binary in CI.

---

## 2. Scope

**In scope (tight P3):**
1. Subtitle acquisition (audio-language VTT) via yt-dlp, through the existing cookie-gate + throttle.
2. VTT parsing → clean transcript + cue index (timestamp ↔ text).
3. Map-reduce summarization producing **three artifacts**: prose summary, timestamped chapters/TOC,
   key-points/highlights.
4. Transcript chunking + embedding into `sqlite-vec` (`vec0`); embedding model+dim recorded per row.
5. Global semantic search (`vec0` KNN across transcripts + summaries) → video + jump-to timestamp.
6. Player: fill the three intelligence panels (stacked, glanceable) + collapsible transcript with
   in-player find; `<track>` captions + CC toggle.
7. New env config (`BACKEND_MIMO_*`, `BACKEND_EMBED_*`, `BACKEND_DEFAULT_SUB_LANG`), required at boot.

**Explicitly deferred to Phase 3.1 (own brainstorm→plan→SDD slices — NOT this branch):**
- **(3.1-a) API token** — auto-generated on first boot, regenerable in Settings (masked/show/copy);
  token-gated machine endpoint that writes the YouTube cookie (bypasses OIDC). Unblocks the P4
  Chrome extension. See `[[vark-phase3-4-api-token-cookie-extension]]`.
- **(3.1-b) Auto-unsubscribe stale channels + stale filter** — a `subscription.activity`
  (`active|stale`) enum; autodownload stays orthogonal.
- **(3.1-c) Re-download button + kill-switch** — `POST /api/videos/{id}/redownload` + a global
  `youtube_paused` flag enforced at `Runner.exec`. See `[[vark-failed-download-recovery-killswitch]]`.
  **Cross-cutting note for 3.1-c:** the summary+embedding job auto-enqueues on *any* successful
  download, so a re-download after an error re-indexes automatically — no manual step. This design
  (§6) already guarantees it; 3.1-c only adds the re-download trigger.

**Out of scope entirely (later phases):**
- **Phase 5 — conversational RAG-QA agent.** P3 ships *retrieval* (`/api/search` returns matching
  videos+timestamps). Phase 5 wraps the *same* `transcript_chunks`/`vec_chunks` store in a MiMo
  Q&A agent: question → embed → KNN retrieve → grounded natural-language answer with citations
  (e.g. "did we ever have a video that showed the new iPhone 27?" → "Yes — *Gadget Review Weekly*,
  2026-09-12, at 4:32"). **P3 must not foreclose this**: `transcript_chunks` keeps raw cue text +
  `start_seconds`, and retrieval is a reusable store method. No Phase-5 code in P3.
- Local Whisper/ASR fallback when no captions exist (big new dependency — not P3).

---

## 3. Migrations (squashed — user override)

Per the user's explicit override of the append-only rule: **Phase 3 schema changes are squashed into
the existing `0001_init.sql`** (as Phase 2 did), NOT a new `0002_*.sql`. Dev DBs are recreated
(`rm ./data/peeq.db*`); the existing boot check fails loud on a stale/mismatched DB. Append-only
resumes at the *next* phase boundary, not this one.

### `videos` — new columns
- `audio_language TEXT NOT NULL DEFAULT ''` — resolved audio language (from info-json or fallback).
- `subtitle_path TEXT NOT NULL DEFAULT ''` — media-dir-relative path to the `.vtt` (or empty).
- `summary TEXT NOT NULL DEFAULT ''` — prose summary.
- `chapters TEXT NOT NULL DEFAULT '[]'` — JSON `[{ts:int_seconds, title, source:'yt-dlp'|'mimo', subtopics?:[{ts,title}]}]`.
- `key_points TEXT NOT NULL DEFAULT '[]'` — JSON `[{ts:int_seconds, text}]`.
- `summary_status TEXT NOT NULL DEFAULT 'pending' CHECK (summary_status IN ('pending','running','done','error','no_transcript'))`.
- `summary_error TEXT NOT NULL DEFAULT ''`.
- `embed_model TEXT NOT NULL DEFAULT ''`, `embed_dim INTEGER NOT NULL DEFAULT 0` — recorded when chunks are embedded.

`no_transcript` is a **terminal, non-error** state: yt-dlp found no manual and no auto captions. The
video is fully watchable; the player shows "No transcript available"; no MiMo calls are wasted.

### `transcript_chunks` — new table (the TEXT-id → INTEGER-rowid bridge, per loom)
```sql
CREATE TABLE transcript_chunks (
    id            INTEGER PRIMARY KEY,          -- = rowid, bridges to vec_chunks.rowid
    video_id      TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    ordinal       INTEGER NOT NULL,
    text          TEXT NOT NULL,
    start_seconds INTEGER NOT NULL DEFAULT 0,   -- earliest cue start in this chunk (jump target)
    token_count   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_transcript_chunks_video ON transcript_chunks(video_id, ordinal);
```

### `vec_chunks` — new `vec0` virtual table (single-user; no partition keys)
```sql
CREATE VIRTUAL TABLE vec_chunks USING vec0( embedding float[<BACKEND_EMBED_DIM default 1536>] );
```
- The dimension is **fixed at DDL time**. It is written into `0001_init.sql` as the default (1536).
- **Dim guard (boot reconcile, loom pattern):** on startup, compare the configured `BACKEND_EMBED_DIM`
  and `BACKEND_EMBED_MODEL` against what's stored. If the configured dim differs from the built
  `vec_chunks` dimension, the whole vector table is invalid — log loudly, drop `transcript_chunks` +
  `vec_chunks` content, and re-enqueue summary jobs to re-embed. Because a model/dim change
  invalidates the entire `vec0` table, `embed_model`/`embed_dim` are stored per video row so the
  reconcile can detect drift. (During squashed-migration development, changing the default dim means
  recreating the dev DB anyway; the guard protects a *running* deployment whose env changes.)
- **Manual vec cleanup:** SQLite forbids `vec0` in triggers/FK cascades ("unsafe use of virtual
  table"). The store layer therefore deletes matching `vec_chunks` rows in the **same transaction**
  whenever `transcript_chunks` rows are removed (video delete, tombstone, re-index) — exactly loom's
  contract.

### `summary_jobs` — new queue table (mirrors `download_jobs`)
```sql
CREATE TABLE summary_jobs (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    video_id     TEXT NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    state        TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','running','done','failed')),
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    last_error   TEXT NOT NULL DEFAULT '',
    enqueued_at  TEXT NOT NULL DEFAULT (datetime('now')),
    started_at   TEXT,
    finished_at  TEXT
);
CREATE INDEX idx_summary_jobs_state ON summary_jobs(state);
CREATE INDEX idx_summary_jobs_video ON summary_jobs(video_id);
```
Boot resets orphaned `running` → `pending` (loom's `ResetStuckIngestions` pattern).

---

## 4. Config (env-based, required at boot)

> **Env prefix:** a prerequisite PR (`chore/env-prefix-backend`) renames all existing `PEEQ_*` vars →
> `BACKEND_*` before this phase (matching loom's `BACKEND_EMBED_*`), so the app can be renamed later
> without touching env config. All vars below are in the `BACKEND_` form.

New vars in `config.go`, validated in the existing `validate()` (fatal on missing, like
`BACKEND_SESSION_SECRET`):

| Var | Required | Default | Notes |
|---|---|---|---|
| `BACKEND_MIMO_BASE_URL` | **yes** | — | OpenAI-compatible chat endpoint. |
| `BACKEND_MIMO_API_KEY` | no | `""` | Optional bearer. |
| `BACKEND_EMBED_BASE_URL` | **yes** | — | OpenAI-compatible `/embeddings` endpoint. |
| `BACKEND_EMBED_MODEL` | **yes** | — | Embedding model id. |
| `BACKEND_EMBED_DIM` | no | `1536` | Must match the `vec0` DDL dim; drives the boot dim-guard. |
| `BACKEND_EMBED_API_KEY` | no | `""` | Optional bearer. |
| `BACKEND_DEFAULT_SUB_LANG` | no | `en` | Fallback when info-json has no audio language. |

Chat model is **hardcoded** `mimo-v2.5-pro` at `reasoning_effort=high` for **all** summarization
calls (map and reduce), ported from loom — an offline job, latency is free, quality is the priority.
`.env.example`, README, and `AGENTS.md` updated to document the new required vars and that Peeq will
not boot without them.

---

## 5. Subtitle acquisition (existing yt-dlp Runner)

`download.go` gains, on the existing download invocation (no new call path):
`--write-subs --write-auto-subs --sub-langs <lang> --convert-subs vtt`.

- `<lang>` resolution: prefer the download info-json's audio `language` field; else
  `BACKEND_DEFAULT_SUB_LANG`. (Audio-language VTT only — never comment subs.)
- yt-dlp writes the `.vtt` into the staging dir; on the existing atomic-rename success path it lands
  in the video's media dir alongside the `.mp4`/`.jpg`. `subtitle_path` + `audio_language` persisted.
- **On download success (any origin — initial or a 3.1 re-download):** enqueue a `summary_jobs` row.
  If no subtitle file was produced, still enqueue: the worker resolves it to `no_transcript`.
- Prefer yt-dlp's own chapter metadata: `download.go` already reads `<id>.info.json` for
  SponsorBlock; extend that read to capture `chapters` when present, stored for the summarizer to use
  instead of generating.

---

## 6. Summarization + embedding worker (new ticker-worker)

New package (e.g. `backend/internal/summarize/worker.go`), structurally a twin of
`download/worker.go`: single-concurrency, `recover()` per job, ctx-cancel shutdown, boot-reset of
orphaned `running`. Claims the oldest `pending` summary job and runs:

1. **Parse VTT** (new `subtitles` package): strip timestamps/positioning/tags; **dedup rolling
   auto-caption repeats** (auto-captions restate the previous line with a word appended — collapse
   to first-full-occurrence); produce (a) a clean plain transcript and (b) a cue index
   (`[]{start_seconds, text}`) for timestamp mapping. No `.vtt` (or empty transcript) → set
   `summary_status='no_transcript'`, finish the job cleanly, no MiMo/embedding calls.
2. **Chunk** — reuse loom's `rag/chunk.go` (`DefaultChunkOptions`, ~600 tok target, ~12% overlap).
   Each chunk records the earliest cue `start_seconds` it covers (jump target for search hits).
3. **Map-reduce summarization** (dodges the MiMo context window — do NOT single-pass a raw
   transcript): summarize each chunk (map), then reduce the chunk-summaries into the three artifacts:
   - **summary** — prose.
   - **chapters** — timestamped TOC. **Prefer yt-dlp's own chapters** (from info-json) when present;
     else generate from the reduce, mapping topics to cue timestamps. Each entry tagged
     `source:'yt-dlp'|'mimo'`.
   - **key_points** — short list of notable/surprising/quotable moments, each with a jump-to ts.
   All calls `reasoning_effort=high`. Persist `summary`, `chapters`, `key_points`.
4. **Embed** the same chunks via the ported `rag/embed.go` client → insert `transcript_chunks` +
   `vec_chunks` (same tx); persist `embed_model` + `embed_dim` on the video. `summary_status='done'`.
5. **SSE** surfaces summary-job phase transitions (queued → summarizing → embedding → done) so the
   player can show a live "summarizing…" state.

**Tombstone interaction (P1 contract preserved):** deleting media (expiry or manual) keeps
`summary`/`chapters`/`key_points`/`transcript_chunks`/`vec_chunks` — watch history stays searchable.
Only a full row delete cascades the chunks (and the store drops the matching `vec_chunks` rows).

---

## 7. HTTP API

- `GET /api/videos/{id}/subtitles` — serves the `.vtt` (path-safe sandbox; `text/vtt`). 404 when
  `subtitle_path` empty. Consumed by the player's `<track>`.
- `GET /api/search?q=<query>&k=<n>` — embed the query → `vec_chunks` KNN → group hits by video →
  return `[{video, matches:[{start_seconds, snippet, score}]}]`. Also matches against summaries.
  Empty/blank `q` → empty result (no call). Requires auth like every other route.
- `POST /api/videos/{id}/resummarize` — re-enqueue a `summary_jobs` row for a video whose media is
  present. Covers a *summary-job* failure or a deliberate re-index; **not** the download-recovery
  path (that auto-enqueues on download success, §5). Resets `summary_status='pending'`.
- `GET /api/videos/{id}` — response gains `summary`, `chapters`, `key_points`, `summary_status`,
  `audio_language`, `has_subtitles`. `GET /api/videos` list stays lean (adds `summary_status` +
  `has_subtitles` badges only).
- SSE: existing feed carries summary-job progress events.

---

## 8. Frontend

Design system unchanged (`[[vark-reuses-music-design-system]]`): Warm Editorial dark, Anthropic
Sans/Serif, lucide 1.9-stroke. **Wordmark is `Peeq`** with the middle **`ee` in terracotta `#D97757`**
and the `P`/`q` in ink `#FAF9F5` (`P<span accent>ee</span>q`).

### Player (`ui/src/views/Player.tsx`)
Replace the three "Coming in a later update" stubs with a **mixed layout** (approved): a sticky
sidebar beside the video holds the glanceable "what's this about" artifacts; the wider, longer
artifacts sit full-width below the video — so nothing dangles far below the video frame.
- **Beside the video (sticky sidebar):**
  - **Summary** card — prose + a "Summarized" status line; a "summarizing…" state while the job
    runs; "No transcript available" when `summary_status='no_transcript'`.
  - **Highlights** card — gold-starred key-points, each **click-to-seek**.
- **Below the video (full content width):**
  - **Contents** — chapter rows (a 2-column grid at wide widths), **click-to-seek**; a small
    `yt-dlp`/`MiMo` source tag per chapter.
  - **Transcript** — **collapsible** (long + search-driven): an in-player **find** box (highlight +
    count, seek on click) over the cue list. Collapsed by default.
- **Captions**: `<track kind="subtitles" srclang=<audio_language> src=/api/videos/{id}/subtitles>` +
  a **CC toggle** in the video controls (off by default; toggles `track.mode`).
- A **Re-summarize** action (calls `POST .../resummarize`) for the media-present-but-summary-failed
  or re-index case.

### Global search (new view + rail item)
A dedicated **Search** view (own left-rail item, as approved): a prominent query box → `GET
/api/search`; results are video cards each listing matched moments (snippet + timestamp + similarity
score) that jump to the video at that timestamp. A note frames it as retrieval today, with Phase 5
adding the conversational answer layer.

---

## 9. Testing strategy

- **config**: boot fails when a required MiMo/embed var is missing; parses defaults.
- **subtitles parser**: VTT → transcript + cue index; timestamp/tag stripping; auto-caption dedup;
  empty/no-file → `no_transcript`. Table-driven, pure, no I/O.
- **ytdlp download**: `fake-ytdlp.sh` writes a `.vtt` + info-json (with/without `chapters`); assert
  the new `--write-subs/--sub-langs/--convert-subs` args, `subtitle_path`/`audio_language` persisted,
  a summary job enqueued on success (incl. the no-subs case → job still enqueued).
- **summarize worker**: fake MiMo httptest server returns canned map/reduce completions; fake embed
  httptest server returns fixed-dim vectors. Assert the three artifacts persisted, chunks +
  `vec_chunks` written with `embed_model`/`embed_dim`, `no_transcript` short-circuits without calling
  the fakes, boot-reset of orphaned `running`, prefer-yt-dlp-chapters branch.
- **store / vec**: KNN retrieval returns expected order; deleting chunks drops `vec_chunks` in the
  same tx; dim-guard reconcile drops+re-enqueues on a changed dim.
- **search handler**: query embed → grouped results; blank query → empty, no embed call.
- **subtitles endpoint**: path-safe (reject `..`/absolute/symlink), `text/vtt`, 404 when absent.
- **frontend (Vitest)**: player renders summary/chapters/highlights from a fixture; chapter/highlight
  click seeks; transcript collapse + find highlights + counts; CC toggle flips track mode;
  `no_transcript` shows the empty state; global search view renders grouped results + jump.
- **Never** call a real LLM, real embeddings endpoint, or the real yt-dlp binary in CI. Backend green
  with `-race`; gofmt clean; CGO_ENABLED=0.

**Manual (not CI):** authenticated end-to-end — real cookie + a running MiMo + embeddings endpoint:
download a real video, confirm VTT + CC, the three artifacts populate, and a global search hits the
right timestamp. Documented as a README operator checklist (as P1/P2 did).

---

## 10. Reuse ledger (port from loom — do not reinvent)

| Concern | loom source |
|---|---|
| MiMo chat client (hardcoded model, `reasoning_effort=high`) | `backend/internal/llm/client.go` |
| Embedding client (OpenAI-compatible `/embeddings`) | `backend/internal/rag/embed.go` |
| Chunking (~600 tok, overlap) | `backend/internal/rag/chunk.go` |
| `vec0` store + `vecLiteral` + KNN retrieve + manual vec cleanup | `backend/internal/rag/{store.go,retrieve.go}` |
| Background ticker-worker (recover-per-item, ctx-cancel, boot-reset) | Peeq's own `download/worker.go` (itself from loom `memory_worker.go`) |
| SSE progress | Peeq's existing `sse/` |

Single-user simplification vs loom: drop `user_id`/`project_id` scoping and partition keys throughout
the ported `rag/` code.

---

## 11. Process & guardrails

- Branch `feat/phase-3-subtitles-summaries-search` off `master`; PR to `master` (never commit to
  master; this is the user's own repo, no upstream). Conventional commits, `.yaml`, English comments.
- Built via `subagent-driven-development`: fresh implementer per TDD task; **every returning result
  validated by a reviewer on the FABLE model**; fix Critical/Important before closing each task,
  record minors in `.superpowers/sdd/progress.md`. End with a holistic whole-branch Fable review,
  then `finishing-a-development-branch`.
- Keep CI green (backend `-race` + UI). Recreate dev DBs after the squashed-migration change.
