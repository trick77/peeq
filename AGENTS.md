# peeq

## Working conventions
- Docs, specs, and code comments are **English only**.
- Commit subjects + PR titles: Go style `area: what`, gated by ship; release notes list them by area.
- Fresh clone: `ship hooks` before the first push (check, conform and init install it too); without the pre-push hook a push skips the gate.
- Gate `ship check` = `make check`. Raise tests, never a coverage floor. Extension Playwright e2e is CI only: too slow for the hook.
- Release by hand only, on GitHub (images are built and signed there): `gh workflow run Release -f bump=auto`. auto = patch, or a commit's `Release: minor` + `Release-note: …` lines.
- Stays in peeq, not kit: the yt-dlp throttle and serial queue (one global gated gap, not a per-key throttle), the `sched` slot math, the yt-dlp selfupdate symlink swap.
- TDD: failing test first, then the minimal implementation.
- Keep files focused — one clear responsibility each.
- Tests fake LLM/embeddings (`llmwiretest`, httptest) — never call a real LLM/embeddings endpoint or the real yt-dlp binary.
- Flows needing a real cookie/AI endpoints aren't automated — run `docs/manual-verification.md` by hand.

## Logging
- Structured `slog` only. Error attr key is **`err`**, never `error`. Short lowercase messages; attrs `snake_case`. No linter checks this.
- 500 → `httpx.ServerError`, 4xx → `httpx.WriteError`, no handler log (kit's AccessLog has it). A 500 deliberately emits two lines: `request failed` (cause, no path) + `http_request` (access, masked path), joined by `request_id`. Don't "deduplicate": they answer different questions.
- Token routes (`/s/`, `/api/s/`, `/api/p/`) log a masked path (`maskPath`): the token is the video.
- **Never log a full URL, `RequestURI()`, or a query string.** The OIDC callback carries a live auth `code`. Log `r.URL.Path`. `logx.RedactAttr` scrubs every `err` attr on the process handler; an error whose text goes elsewhere (Activity row, client message) still needs `logx.RedactErr()`.

## Locked technical choices (do not change without explicit agreement)
- **Pure-Go SQLite**: `ncruces/go-sqlite3` pinned `v0.23.3` (matches loom). `CGO_ENABLED=0` everywhere.
- HTTP: stdlib `net/http`, no web framework.
- Plumbing kit (`github.com/trick77/ship/kit`) has comes from kit, never hand-rolled. Lint config is ship's (no local copy).
- Sign-in `kit/auth`: no user/session table, no server-side revoke; rotating `BACKEND_SESSION_SECRET` ends every session. Bearer routes are `internal/tokenauth`, never a session.
- Runtime `debian:13-slim`, not distroless; yt-dlp = unpacked `yt-dlp_linux*.zip`, in image AND self-update. Why: `backend/Containerfile`, `ytdlp.releaseFor`.

## Models
- **Models are config, never code.** No model id default anywhere. Swap = config + the provider's key.
- **Every model fact is llmwire's profile's**: host, key var, reasoning knob and level names,
  output limit, vector width, sampling, rates, quirks. Never restate one in code, comments, docs or
  tests; a wrong fact is fixed upstream.
- Embed model switch with vectors stored = new migration rebuilding `vec_chunks`, clearing `vec_model`, setting `embed_rev = 0` (else nothing re-embeds). No auto re-index. Empty library may switch same-width.

## Chat model
- **The wire protocol is `github.com/trick77/toolkit/llmwire`.** Never reimplement here: SSE parsing,
  header/idle/call bounds, the request body (no `ExtraBody`, ever), usage decoding, reasoning and
  cap rendering, opencode identity. One `llmwire.Client` per `llm.Client`, never one per call: the
  session id lives on it.
- **Reasoning is an intent** (`llm.WithReasoning`), resolved per model by llmwire:
  - Default (model's own): offline prose and persisted decisions — summary, map/reduce, key points, classify.
  - `ReasoningBalanced`: prose a person waits on — the Ask answer (depth is paid in time-to-first-token).
  - `ReasoningMinimal` (may be thinking OFF): only a true gate under a latency bound whose failure
    degrades harmlessly — the Ask understand step. Never on prose, never on a persisted decision:
    measured, the shallowest setting classified ambiguous videos unstably, and a wrong category
    persists forever. Write the latency reason down at the call site.
- **Caps are on the answer** (`llm.WithMaxAnswerTokens`); llmwire adds the reasoning allowance.
  Still a GENEROUS backstop: out-thinking the allowance ends "length", empty content, NO error.
  Bit classify once (empty reply → permanent 'uncategorized').
- **Keepalives do not hold the idle bound off.** Only `data:` frames re-arm it — reasoning deltas
  included, which is why a long silent think is still safe.
- Lookup no reader sees (an id, a label) → `llm.ShortGate(ctx)` (gate model). Never on reader-facing
  text (summary, map, reduce, keypoints, Ask).
- Need JSON → `llm.AsJSONObject(ctx)`. A prompt saying "as JSON" does NOT work: 0/8 raw replies
  strictly parseable without it, 8/8 with it. Except on `ShortGate`: a gate profile may lack JSON
  output and llmwire refuses the call; there, cut the object out of the reply.
- New summarizer call site → feed it `forSummary`, never `parsed`: `parsed` still carries sponsor
  reads. Embedding deliberately keeps `parsed` — search is not narrowed.
- Tests assert intent on `llmwiretest` synthetic models; never a real model id, wire spelling, level name or rate.
- After changing `BACKEND_CHAT_MODEL`, re-run `httpapi/ask_latency_probe_test.go`
  (`PEEQ_ASK_SWEEP=1`) before trusting the chosen intents on the new model.

## MCP server (`/mcp`, `docs/mcp.md`)
- OIDC bypasses: share/grant token routes, `/api/machine` (extension token, cookie write only)
  and `/mcp` (`BACKEND_MCP_TOKEN`, read-only). Never let one token open the other's route.
- Retrieval goes through `httpapi/mcp_library.go` over the Search lanes; never a second
  search implementation. Items unnumbered, `sources[i]` ↔ i-th item: a contract with loom.

## Database / migrations
- `schema_migrations.version` + `schema_migrations_*` trigger/lookup: keep while rollback to the last pre-kit
  release must work; dropping them = deliberate later migration.
- NEVER edit or rename a migration that ran anywhere real, `0001_init.sql` included: the runner skips a
  recorded name → edit never applies. Safe only before it ships; else next number.
- Migration touching DATA → test on a populated DB at the previous migration (`applyThrough`). Fresh-DB test runs it over zero rows: proves nothing.
- **vec0 never frees a deleted vector** — the storage chunk stays, inserts only append, every KNN reads every chunk. Each re-embed adds dead chunks; rongo hit 46k vectors in 380 chunks and minutes per search, surfacing as `chunks iter error` (sqlite-vec's word for an interrupt). `rag.CompactVectors` rebuilds past 2× AT BOOT ONLY: it holds the write lock for minutes (rongo production: 8m33s for 46k vectors), far past `busy_timeout`.
- **List queries are index-only, and must stay so.** Production DB sits on 7200rpm disks: cost = pages touched cold. Small columns live BEHIND big ones (image `bytes`, video description + analysis text), so reading them from the table walks overflow pages per row — the Library took 2.5s. Image version → `INDEXED BY idx_*_version`. New card/filter/sort column → migration adding it to `idx_videos_cards`. New list endpoint → narrow projection + a plan test, never `videoColumns`.
- Ad-hoc query against a containerised DB → sqlite base image, never `alpine` + `apk add sqlite`:
  `docker run --rm -v "$PWD/data:/data" keinos/sqlite3 sqlite3 -readonly -box /data/peeq.db "<sql>"`.
  Mount rw, not `:ro` — WAL needs the `-shm` sidecar even to read; `-readonly` is what protects a live DB.

## Extension (`extension/`)
- Plain MV3 ES modules. No bundler, no framework, no dependencies.
- Never `getAllCookieStores()` — read only this profile's store. Merging stores lets a dead session
  shadow a live one.
- Never persist a cookie; only `baseUrl` and `token` go in storage.
- Never send a jar without a gate cookie (`SID`, `__Secure-1PSID`, `__Secure-3PSID`) — an anonymous jar
  overwrites peeq's good cookie.
- Netscape column 4 is `secure`, not `httpOnly`.
- UI: `system-ui` only, no serif, no font files. "cookie" is singular except when counting.

## peeq invariants (must hold in every feature that talks to YouTube)
- **Cookie gate**: never make a YouTube call without a valid, currently-loaded cookie. None → fail
  closed, never an anonymous request.
- **Kill switch fails closed**: `settings.YoutubePaused` reports paused when the row can't be read.
  Gates are polled, so a blip clears itself; a call let through on a blip does not.
- **Serial queue**: every YouTube call takes the ONE turn (`ytdlp.Runner.acquire`); the next starts
  >= a randomized gap after that END, never after its start. Clicks jump queued work, never a
  running call; downloads (approved too) stay background, else they starve scans. CDN images
  (posters, channel art) too: `Runner.FetchImage`, never `media.FetchImageBytes`. A request handler
  never waits on image work.
- **Browser never loads a YouTube host**: images only via peeq endpoints, no remote image URL in
  any API response.
