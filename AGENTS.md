# peeq

Self-hosted YouTube watch pipeline — triage, download, summarize, watch; watched non-favorites are
swept off disk. Go backend serving a JSON API + an embedded React SPA, backed by SQLite.

## Working conventions
- Docs, specs, and code comments are **English only**.
- One feature branch per phase (`feat/phase-N-...`). Conventional commits.
- TDD: failing test first, then the minimal implementation.
- Keep files focused — one clear responsibility each.
- Phase 3 needs chat + embeddings models (`BACKEND_*_MODEL` + `LLMWIRE_<PROVIDER>_API_KEY`); tests
  fake them (`llmwiretest`, httptest) — never call a real LLM/embeddings endpoint or the real yt-dlp binary.
- Flows needing a real cookie/AI endpoints aren't automated — run `docs/manual-verification.md` by hand.

## Logging
- Structured `slog` only. Error attr key is **`err`**, never `error`.
- Short lowercase messages; variables go in attrs (`snake_case`: `job_id`, `video_id`, `path`).
- Every 500 goes through `serverError(w, r, err, "client message")` — it logs the cause, returns only the generic message. 4xx uses `writeJSONError`, no handler-level log needed — the request middleware records every request, 4xx at WARN, 5xx at ERROR.
- A 500 therefore deliberately emits two lines: `request failed` (the cause, from `serverError`) and `request` (the access line, from the middleware). Don't "deduplicate" by deleting one — they answer different questions.
- **Never log a full URL, `RequestURI()`, or a query string.** The OIDC callback carries a live auth `code`. Log `r.URL.Path`. The process's slog handler runs `logx.RedactAttr` on every `err` attr, so a URL in an error never reaches a log line whole; still wrap an error that may embed a URL in `logx.RedactErr()` where its text goes elsewhere (an Activity row, a client message).
- Level via `BACKEND_LOG_LEVEL` (debug/info/warn/error), read in `main()` before anything else.

## Commands
- `make test` — backend Go tests (`go test ./...`)
- `make fe-test` — frontend Vitest
- `make fe-build` — build the SPA into `backend/web/dist` (embedded by Go)
- `make build` — full build → `bin/peeq` (CGO_ENABLED=0)
- `make run` — run locally
- `make dev` — backend + Vite dev server with `/api` proxy (`hack/dev.sh`)
- `docker compose up --build` — full stack (copy `.env.example` → `.env` and fill it first)

## Locked technical choices (do not change without explicit agreement)
- Module path `github.com/trick77/peeq`. Go 1.25 (`go.mod`; Containerfile build stage uses `golang:1.25-alpine`).
- **Pure-Go SQLite**: pin `ncruces/go-sqlite3` to `v0.23.3` (matches loom). `CGO_ENABLED=0` everywhere.
- HTTP: stdlib `net/http` (Go 1.22+ method routing), no web framework.
- Runtime image is `debian:12-slim` (glibc, apt), **not** distroless — peeq shells out to `ffmpeg` and
  `yt-dlp`, both needing a real userland. See the comment in `backend/Containerfile` for why this
  deviates from loom's distroless-static runtime.

## Models
- **Models are config, never code.** `BACKEND_CHAT_MODEL` (required), `BACKEND_GATE_MODEL` (short
  gates; empty = chat model), `BACKEND_EMBED_MODEL` (required). No model id default anywhere. Boot
  validates with llmwire `Registry.Require` (`llm.ChatNeeds`/`GateNeeds`) and `LookupEmbedding`;
  the error names the var and the valid choices. Swap = config + the provider's key.
- **Every model fact is llmwire's profile's**: host, key var, reasoning knob and level names,
  output limit, vector width, sampling, rates, quirks. Never restate one in code, comments, docs or
  tests; a wrong fact is fixed upstream.
- `vec_chunks` width is a migration literal; `vec_model` records which model wrote the vectors.
  Boot refuses an embed model of another width (`rag.CheckVecWidth`) or another id at the same width
  (`rag.Store.CheckEmbedModel`: same width, different vector space). An empty library may switch
  to a same-width model; a different width always needs the migration. Switching with vectors = new migration rebuilding `vec_chunks`, clearing `vec_model` and
  setting `embed_rev = 0` (else nothing re-embeds). No auto re-index.

## Chat model
- **The wire protocol is `github.com/trick77/llmwire`.** What that library owns, and what therefore
  must NOT be reimplemented here: the SSE parsing, the header/idle/call bounds and the text naming
  which one fired, the request body (no `ExtraBody`, ever), the usage decoding, reasoning and cap
  rendering, and the opencode identity. This package owns pacing, the heartbeat, the
  `CallInfo`/`Totals` accounting and the context knobs. One `llmwire.Client` per `llm.Client`,
  never one per call: the session id lives on it.
- **Reasoning is an intent** (`llm.WithReasoning`), resolved per model by llmwire:
  - Default (nothing sent, the model's own default): offline prose and persisted decisions —
    summary, map/reduce, key points, classify.
  - `ReasoningBalanced`: prose a person waits on — the Ask answer (depth is paid in
    time-to-first-token).
  - `ReasoningMinimal` (may be thinking OFF): only a true gate under a latency bound whose failure
    degrades harmlessly — the Ask understand step. Never on prose, never on a persisted decision:
    measured, the shallowest setting classified ambiguous videos unstably, and a wrong category
    persists forever. Write the latency reason down at the call site.
- **Caps are on the answer**: `llm.WithMaxAnswerTokens` sized for the answer alone; llmwire adds
  the profile's reasoning allowance and clamps to the output limit. Still a GENEROUS backstop: a
  call that out-thinks the allowance ends "length" with empty content and NO error. This bit
  classify once (empty reply → permanent 'uncategorized').
- **Keepalives do not hold the idle bound off.** Only `data:` frames re-arm it — reasoning deltas
  included, which is why a long silent think is still safe.
- Lookup no reader sees (an id, a label) → `llm.ShortGate(ctx)`: routes to the gate model. Never on
  reader-facing text (summary, map, reduce, keypoints, Ask). Independent of the reasoning intent.
- Need JSON → `llm.AsJSONObject(ctx)`. A prompt saying "as JSON" does NOT work: 0/8 raw replies
  strictly parseable without it, 8/8 with it.
- New summarizer call site → feed it `forSummary`, never `parsed` (summarize/worker.go). `parsed` is
  the raw transcript and still carries sponsor reads; `forSummary` has them stripped so no chapter,
  key point or summary sentence can be drawn from one. See `summarize/sponsor.go`. Embedding
  deliberately keeps `parsed` — search is not narrowed.
- Tests assert intent on `llmwiretest` synthetic models: `llm.ReasoningFor`/`ShortGateFrom` on a
  fake completer's ctx, `srv.Last().Reasoning()` vs `llmwiretest.MinimalSent`/`BalancedSent`,
  `MaxTokens()` vs answer cap + overhead. Never a real model id, wire spelling, level name or rate.
- After changing `BACKEND_CHAT_MODEL`, re-run `httpapi/ask_latency_probe_test.go`
  (`PEEQ_ASK_SWEEP=1`) before trusting the chosen intents on the new model.

## Config
- All runtime config comes from `BACKEND_*` env vars — see `.env.example`.
- Secrets via env only; never commit them.

## Database / migrations
- New migration → new numbered file `backend/internal/store/migrations/NNNN_*.sql`. Runner applies pending
  ones in order, records them in `schema_migrations`.
- NEVER edit a migration that has run anywhere real, `0001_init.sql` included: the runner skips a recorded
  version, so the edit silently never applies. Safe only before it ships; else write the next number.
- Migration touching DATA (not just shape) → test on a populated DB stood up at the previous migration
  (`applyThrough`). Fresh-DB test runs it over zero rows and passes whatever it says.
- Ad-hoc query against a containerised DB → sqlite base image, never `alpine` + `apk add sqlite`:
  `docker run --rm -v "$PWD/data:/data" keinos/sqlite3 sqlite3 -readonly -box /data/peeq.db "<sql>"`.
  Mount rw, not `:ro` — WAL needs the `-shm` sidecar even to read; `-readonly` is what protects a live DB.

## Frontend
- Vite + React + TS + Tailwind. `npm run build` empties `backend/web/dist` and overwrites the tracked
  placeholder `index.html`. Never commit built assets — only that placeholder is tracked; restore it
  (`git checkout -- backend/web/dist/index.html`) after a local build.

## Extension (`extension/`)
- Plain MV3 ES modules. No bundler, no framework, no dependencies. Tests are `node --test`.
- Never `getAllCookieStores()` — read only this profile's store. Merging stores lets a dead session
  shadow a live one.
- Never persist a cookie; only `baseUrl` and `token` go in storage.
- Never send a jar without a gate cookie (`SID`, `__Secure-1PSID`, `__Secure-3PSID`) — an anonymous jar
  overwrites peeq's good cookie.
- Netscape column 4 is `secure`, not `httpOnly`. After changing the serializer, regenerate the
  cross-language fixture:
  `cd extension && node testdata/generate_fixture.js > ../backend/internal/cookie/testdata/extension_output.txt`
- `Authorization: Bearer`, never `Token`.
- UI: `system-ui` only, no serif, no font files. "cookie" is singular except when counting.

## peeq invariants (must hold in every feature that talks to YouTube)
- **Cookie gate**: never make a YouTube call without a valid, currently-loaded cookie. No valid cookie
  configured → fail closed (refuse), never attempt an anonymous request.
- **Kill switch fails closed**: `settings.YoutubePaused` reports paused, plus the error, when the
  row cannot be read; gates may ignore the error, handlers answer 500 with it. Gates are polled,
  so a blip clears itself; a call let through on a blip does not.
- **Randomized throttle**: every YouTube call is spaced from the previous one by a randomized gap
  (floor + jitter, enforced by `ytdlp.Runner`'s pacer across the whole process). The gap is
  trailing, not leading: an idle Runner goes at once, so a click after hours of quiet does not
  wait 20-35s for nothing. What matters is that consecutive starts are never closer than a gap.
