# peeq

Self-hosted YouTube watch pipeline — triage, download, summarize, watch; watched non-favorites are
swept off disk. Go backend serving a JSON API + an embedded React SPA, backed by SQLite.

## Working conventions
- Docs, specs, and code comments are **English only**.
- One feature branch per phase (`feat/phase-N-...`); never commit to `master`. Conventional commits.
- TDD: failing test first, then the minimal implementation.
- Keep files focused — one clear responsibility each.
- YAML files use `.yaml`, never `.yml`.
- Phase 3 needs chat + embeddings endpoints (`BACKEND_CHAT_*`, `BACKEND_EMBED_*`); tests fake them
  with httptest — never call a real LLM/embeddings endpoint or the real yt-dlp binary.
- Flows needing a real cookie/AI endpoints aren't automated — run `docs/manual-verification.md` by hand.

## Logging
- Structured `slog` only. Error attr key is **`err`**, never `error`.
- Short lowercase messages; variables go in attrs (`snake_case`: `job_id`, `video_id`, `path`).
- Every 500 goes through `serverError(w, r, err, "client message")` — it logs the cause, returns only the generic message. 4xx uses `writeJSONError`, no handler-level log needed — the request middleware records every request, 4xx at WARN, 5xx at ERROR.
- A 500 therefore deliberately emits two lines: `request failed` (the cause, from `serverError`) and `request` (the access line, from the middleware). Don't "deduplicate" by deleting one — they answer different questions.
- **Never log a full URL, `RequestURI()`, or a query string.** The OIDC callback carries a live auth `code`. Log `r.URL.Path`. Wrap errors that may embed a URL in `redactErr()`.
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
- **Randomized throttle**: every YouTube call is preceded by a randomized delay (not a fixed interval)
  to avoid a bot-detectable request cadence.
