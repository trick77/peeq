# peeq

## Conventions
- Commit subjects + PR titles: Go style `area: what`, gated by ship. `Release: minor` / `Release-note: …` commit lines feed the release.
- Fresh clone: `ship hooks` before the first push (check, conform and init install it too); without the pre-push hook a push skips the gate.
- Raise tests, never a coverage floor. Extension Playwright e2e CI only: too slow for the hook.
- Stays in peeq, not kit: yt-dlp throttle + serial queue (one global gated gap, not per-key), `sched` slot math, selfupdate symlink swap.
- TDD: failing test first. One responsibility per file.
- Tests fake LLM/embeddings (`llmwiretest` synthetic models, no real ids/levels/rates) and yt-dlp. Real-cookie/AI flows: `docs/manual-verification.md` by hand.

## Logging
- `slog`; error key **`err`**, never `error`; short lowercase msgs, `snake_case` attrs. No linter checks this.
- 500 → `httpx.ServerError`, 4xx → `httpx.WriteError`, no handler log. A 500 emits two lines on purpose (`request failed` cause + `http_request` access, joined by `request_id`): don't dedupe.
- Never log a full URL or query: OIDC callback carries a live auth `code`. Token routes: `maskPath`, token = the video. Error text leaving logs (Activity, client) → `logx.RedactErr()`.

## Locked (change only if agreed)
- Pure-Go `ncruces/go-sqlite3` pinned `v0.23.3` (matches loom), no CGO. Stdlib `net/http`. Plumbing from kit, never hand-rolled; lint config is ship's.
- `kit/auth`: no session table, no revoke; rotate `BACKEND_SESSION_SECRET` = end all sessions. Bearer routes = `tokenauth`, never a session.

## LLM
- Models = config, no model id default. Every model fact (host, key var, levels, limits, rates) is llmwire's profile's: never restate here, fix upstream.
- Embed model switch with vectors stored → migration rebuilding `vec_chunks`, clearing `vec_model`, `embed_rev = 0` (else nothing re-embeds). No auto re-index. Empty library may switch same-width.
- Wire = `toolkit/llmwire`, never reimplemented (no `ExtraBody`, ever). One `llmwire.Client` per `llm.Client`, not per call: session id lives on it.
- Reasoning: default = offline prose + persisted decisions; `ReasoningBalanced` = prose a person waits on (Ask); `ReasoningMinimal` = only a latency-bound gate failing harmlessly, latency reason at call site. Measured: minimal classified ambiguous videos unstably, wrong category persists forever.
- Cap the answer (`llm.WithMaxAnswerTokens`), llmwire adds the reasoning allowance. GENEROUS: out-thinking it → "length", empty content, NO error. Bit classify once (permanent 'uncategorized').
- Idle bound re-arms on `data:` frames only (reasoning deltas too), never keepalives.
- Lookup no reader sees (id, label) → `llm.ShortGate`; never reader-facing text.
- Summarizer → `forSummary`, never `parsed` (sponsor reads).
- JSON → `llm.AsJSONObject`; "as JSON" prompt alone: 0/8 parseable, 8/8 with it. On `ShortGate` llmwire may refuse it: cut the object out of the reply.
- `BACKEND_CHAT_MODEL` changed → run `httpapi/ask_latency_probe_test.go`.

## MCP
- OIDC bypasses: share/grant routes, `/api/machine` (extension token, cookie write), `/mcp` (`BACKEND_MCP_TOKEN`, read-only). One token never opens another's route.
- Retrieval = Search lanes (`mcp_library.go`), never a 2nd search. Unnumbered items, `sources[i]` = i-th: loom contract.

## Database
- Read `docs/schema.md` first. `TestSchemaDoc` golden, regenerate in `backend/`. Blind to triggers, data-only migrations, table-type fields (adding one passes), fields vs real columns.
- Keep `schema_migrations.version` + `schema_migrations_*` trigger/lookup while rollback to last pre-kit release must work.
- NEVER edit/rename a migration that ran anywhere real (`0001_init.sql` too): runner skips recorded names.
- Data migration → test on populated DB at prior migration (`applyThrough`); fresh DB proves nothing.
- vec0 never frees deleted vectors: re-embeds pile dead chunks every KNN reads. `rag.CompactVectors` boot only: holds write lock for minutes (`rag/compact.go`).
- List queries stay index-only: prod on 7200rpm disks, small cols behind big ones walk overflow pages (Library took 2.5s). Image version → `INDEXED BY idx_*_version`. New card/filter/sort col → into `idx_videos_cards`; new list endpoint → narrow projection + plan test, never `videoColumns`.
- Ad-hoc query: `docker run --rm -v "$PWD/data:/data" keinos/sqlite3 sqlite3 -readonly -box /data/peeq.db "<sql>"`, not alpine+apk. No `:ro`: WAL needs `-shm` to read.

## Extension
- Plain MV3 ES modules, no bundler/framework/deps.
- Never `getAllCookieStores()` (`background.js`). Persist only `baseUrl` + `token`. No jar without a gate cookie (`SID`, `__Secure-1PSID`, `__Secure-3PSID`).
- UI `system-ui` only, no font files. "cookie" singular except counting.

## YouTube invariants
- No YouTube call without a valid loaded cookie, never anonymous. Kill switch `settings.YoutubePaused` fails closed (`settings/store.go`).
- Every YouTube call takes the ONE turn (`Runner.acquire`), CDN images too (`Runner.FetchImage`, never `media.FetchImageBytes`). Downloads (approved too) stay background. Handlers never wait on image work.
- Browser never loads a YouTube host: no remote image URL in any API response.
