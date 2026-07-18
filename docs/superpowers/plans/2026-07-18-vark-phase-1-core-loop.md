# vark Phase 1 — Core Watch-and-Download Loop — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the usable core loop: paste a YouTube link → it downloads in a throttled, resumable queue → watch it in-browser with resume → watched/favorite lifecycle with automatic cleanup — single-user, behind Authentik (dev auto-login locally).

**Architecture:** A single Go binary (stdlib `net/http`, no framework) serves an embedded React/Vite SPA and a JSON/SSE API, backed by one SQLite file (pure-Go `ncruces/go-sqlite3`, `CGO_ENABLED=0`). Downloads are long-running jobs executed by a single-concurrency claiming worker goroutine that shells out to the `yt-dlp` binary through one wrapper package (the only place cookies, throttling, and block-detection live). The whole thing mirrors the sibling `../loom` app; infrastructure (config, store+migrations, OIDC+dev-auth, SSE, SPA embed, background worker) is ported from loom with a `VARK_` env prefix, and yt-dlp patterns are adapted from `../tubearchivist`.

**Tech Stack:** Go 1.25 · `net/http` (Go 1.22 method routing) · `ncruces/go-sqlite3` v0.23.3 + `sqlite-vec-go-bindings/ncruces` v0.1.7-alpha.2 · `coreos/go-oidc/v3` + `golang.org/x/oauth2` · React 19 · Vite 8 · Tailwind v4 (CSS-first `@theme`) · `lucide-react@^1.24.0` · Vitest · yt-dlp (binary) + ffmpeg.

## Global Constraints

- Module path `github.com/trick77/vark`. Go 1.25. `CGO_ENABLED=0` everywhere.
- Pins (do not bump): `ncruces/go-sqlite3` **v0.23.3**, `sqlite-vec-go-bindings/ncruces` **v0.1.7-alpha.2** (ABI-matched, same as loom). One SQLite file; `sqlite-vec` for vectors; no separate DB service.
- All runtime config via `VARK_*` env vars. Required to boot: `VARK_SESSION_SECRET`. Dev-auth (`VARK_AUTH_MODE=dev`) must hard-fail unless bound to loopback (loom's `validateDevAuthLocalOnly` rule).
- **Hard invariant:** NO call to YouTube without a valid cookie present. Every yt-dlp invocation gates on cookie presence up front.
- **Throttle invariant:** a randomized sleep (≈0.5–1.5× a configurable base interval) before every YouTube network action.
- Docs/code/comments **English only**. Conventional Commits. One feature branch per phase (`feat/phase-1-...`); never commit to `master`. YAML files use `.yaml`, never `.yml`.
- TDD: failing test first, then minimal implementation. Every DB-writing table access goes through a store method (no ad-hoc SQL in handlers).
- Swiss German only applies to user-facing German copy (none in P1 — UI copy is English).
- Design system = `../music` (Warm Editorial dark). Tokens/fonts/icons per the design spec in `~/.claude/plans/vark-is-a-yt-dlp-tender-moth.md`.
- Media path safety: always pass full URLs to yt-dlp (never bare IDs — YouTube IDs can start with `-`); `-o` templates use IDs only, never titles; reject `..`/absolute/symlink-escape on any media path.

---

## File Structure

```
vark/
├── backend/
│   ├── go.mod  go.sum
│   ├── cmd/vark/main.go                 entrypoint: config → store → wire deps → serve + workers
│   ├── web/embed.go                     //go:embed all:dist ; SPA fileserver + SPA fallback
│   ├── Containerfile                    3-stage node→go→distroless (+ yt-dlp + ffmpeg)
│   └── internal/
│       ├── config/config.go             VARK_* env load + validation + dev-auth loopback guard
│       ├── store/
│       │   ├── store.go                 Open() (WAL, busy_timeout, sqlite-vec), vecLiteral helper
│       │   ├── migrate.go               embedded numbered-SQL migration runner
│       │   └── migrations/0001_init.sql settings, videos, download_jobs (+ later vec table in P3)
│       ├── auth/                        oidc.go session.go user_store.go middleware.go model.go
│       ├── settings/store.go            settings singleton read/write (+ cookie status)
│       ├── cookie/netscape.go           parse+validate pasted Netscape cookie text
│       ├── ytdlp/
│       │   ├── ytdlp.go                 wrapper: cookie gate, throttle, run, error classify
│       │   ├── errors.go                ErrNoCookie/ErrCookieExpired/ErrBlocked/ErrTerminal
│       │   ├── meta.go                  Metadata(url) via `-J`
│       │   ├── download.go              Download(job) staging→args→run→atomic place
│       │   ├── selfupdate.go            version + `-U`/download-latest
│       │   └── url.go                   canonicalize pasted input → watch URL + id
│       ├── videos/store.go              videos CRUD, lifecycle, tombstone
│       ├── jobs/store.go                download_jobs queue store
│       ├── download/worker.go           single-concurrency claiming worker (retry, watchdog, pause)
│       ├── retention/sweeper.go         auto-delete sweep + disk-space guard
│       ├── httpapi/
│       │   ├── server.go                routes + Deps + requireAuth
│       │   ├── auth_handlers.go         port loom (dev short-circuit)
│       │   ├── settings_handlers.go     GET/PUT settings, cookie health
│       │   ├── downloads_handlers.go    POST/list/cancel + SSE progress
│       │   ├── videos_handlers.go       list/get/delete/favorite/watched/resume/stream
│       │   └── health_handlers.go       /healthz
│       └── sse/sse.go                   SSE writer + heartbeat (port loom)
├── ui/  (Vite React TS Tailwind v4)
│   ├── package.json  vite.config.ts  index.html
│   └── src/
│       ├── main.tsx  App.tsx  index.css (music @theme + fonts)
│       ├── fonts/                        AnthropicSans/Serif woff2 (copied from ../music)
│       ├── api/  http.ts stream.ts videos.ts downloads.ts settings.ts auth.ts types.ts index.ts
│       ├── icons.tsx                     lucide-react wrappers (Icon/Glyph)
│       ├── shell/ Rail.tsx TopBar.tsx DownloadDock.tsx CookieStatus.tsx
│       └── views/ Library.tsx Add.tsx Player.tsx Settings.tsx
├── .github/{dependabot.yaml,workflows/{ci.yaml,release.yaml,cleanup-images.yaml}}
├── compose.yaml  compose.dev.yaml  .env.example  Makefile  hack/dev.sh
├── AGENTS.md  README.md
```

**Data model (0001_init.sql), single-user (no `user_id`):**
- `settings` — one-row: `cookie_text`, `cookie_status` (`absent|valid|stale|blocked`), `cookie_updated_at`, `format_preset` (id), `format_custom`, `limit_rate`, `throttle_base_seconds`, `retention_days`, `min_free_gb`, `ytdlp_version`.
- `videos` — `id` TEXT PK (YouTube id), `url`, `title`, `channel_id`, `channel_name`, `duration_seconds`, `published_at`, `description`, `thumbnail_path`, `media_path`, `filesize_bytes`, `format_used`, `availability` (`available|deleted|private|geo|unknown`), `status` (`new|queued|downloading|downloaded|tombstoned|error`), `error_message`, `sponsorblock_segments` JSON, `watched` INT, `watched_at`, `resume_position_seconds` REAL, `favorite` INT, `favorited_at`, `created_at`, `downloaded_at`. (`summary`,`chapters`,embeddings land in P3.)
- `download_jobs` — `id` INTEGER PK, `video_id` FK, `state` (`pending|running|done|failed|canceled`), `priority` INT (manual=10 > auto=0), `attempts` INT, `max_attempts` INT, `last_error`, `log_tail` TEXT, `enqueued_at`, `started_at`, `finished_at`.

---

## Task 1: Repo scaffold + buildable skeleton + CI + private GitHub repo

**Files:**
- Create: `backend/go.mod`, `backend/cmd/vark/main.go`, `backend/internal/version/version.go`, `backend/web/embed.go`, `backend/web/dist/index.html` (placeholder), `ui/package.json`, `ui/index.html`, `ui/vite.config.ts`, `ui/src/main.tsx`, `ui/src/App.tsx`, `ui/src/App.test.tsx`, `backend/Containerfile`, `Makefile`, `hack/dev.sh`, `.env.example`, `.gitignore`, `AGENTS.md`, `README.md`, `.github/dependabot.yaml`, `.github/workflows/ci.yaml`, `.github/workflows/release.yaml`, `.github/workflows/cleanup-images.yaml`.
- Reference (copy-adapt): `../loom/backend/web/embed.go`, `../loom/Makefile`, `../loom/hack/dev.sh`, `../loom/backend/Containerfile`, `../music/.github/{dependabot.yaml,workflows/{ci.yaml,release.yaml}}`, `../loom/.github/workflows/cleanup-images.yaml`.

**Interfaces:**
- Produces: `version.Version` (string, ldflags-injected, default `dev`); `web.Handler() http.Handler` (serves embedded `dist` with SPA fallback to `index.html`); a `vark` binary that serves `:8080`.

- [ ] **Step 1: Init git + module.** Branch `feat/phase-1-scaffold`. `cd backend && go mod init github.com/trick77/vark`. Set `go 1.25` in `go.mod`.

- [ ] **Step 2: Write failing test for the health-serving binary.** `backend/internal/version/version_test.go`:
```go
package version
import "testing"
func TestVersionDefault(t *testing.T) {
    if Version == "" { t.Fatal("Version must not be empty") }
}
```
- [ ] **Step 3: Run — expect FAIL** (`go test ./internal/version/` → build error, no `Version`).
- [ ] **Step 4: Implement** `version.go`: `package version; var Version = "dev"`.
- [ ] **Step 5: Run — expect PASS.**

- [ ] **Step 6: SPA embed.** Copy `../loom/backend/web/embed.go` verbatim (it embeds `all:dist` and serves SPA fallback). Add `backend/web/dist/index.html` placeholder: `<!doctype html><title>vark</title><div id=root></div>`. Add a test `backend/web/embed_test.go` that does an `httptest` GET `/` and asserts 200 + body contains `root`.
- [ ] **Step 7: Minimal `cmd/vark/main.go`** that listens on `:8080` and mounts `web.Handler()` at `/` plus a literal `GET /healthz` returning `200 ok`. (Config/DB/auth arrive in later tasks; keep this minimal so the image builds.)
- [ ] **Step 8: UI skeleton.** `ui/package.json` with React 19, Vite 8, Tailwind v4, `lucide-react@^1.24.0`, Vitest (copy versions from `../music/ui/package.json`). `vite.config.ts` builds to `../backend/web/dist`. `App.tsx` renders `<div>vark</div>`. `App.test.tsx` (Vitest + Testing Library) asserts it renders "vark".
- [ ] **Step 9: Build/test tooling.** `Makefile` targets `test` (`go test ./...`), `fe-test` (`vitest --run`), `fe-build`, `build` (fe-build then `CGO_ENABLED=0 go build -o bin/vark ./backend/cmd/vark`), `dev` (`hack/dev.sh`) — adapt from `../loom/Makefile`. `hack/dev.sh` runs backend on `127.0.0.1:8080` in dev-auth + `vite` proxying `/api` → backend (adapt `../loom/hack/dev.sh`).
- [ ] **Step 10: Containerfile** — adapt `../loom/backend/Containerfile` 3-stage build; **add `yt-dlp` and `ffmpeg`** to the runtime stage (distroless lacks them → use a minimal glibc base such as `debian:12-slim` for runtime, install `ffmpeg`, and fetch the `yt-dlp` binary into `/usr/local/bin`; a writable `/data/bin` is used later for self-update). Document this deviation from loom's distroless-static in a comment.
- [ ] **Step 11: CI + dependabot.** Copy `../music/.github/dependabot.yaml` verbatim (gomod `/backend`, npm `/ui`, github-actions, docker `/backend`). `ci.yaml` = music's minus the Python `sidecar` job (backend `go build/vet/test`, ui `npm ci/build/test`). `release.yaml` = music's (build+push `ghcr.io/${{github.repository}}` on push to `master`, auto-version tag). `cleanup-images.yaml` = loom's with `image-names: "vark"`.
- [ ] **Step 12: `.env.example`** (adapt loom's): `VARK_PUBLIC_URL`, `VARK_SESSION_SECRET`, OIDC block, and the dev block (`VARK_AUTH_MODE=dev`, `VARK_ADDR=127.0.0.1:8080`). Add `VARK_DB_PATH=/data/vark.db`, `VARK_MEDIA_DIR=/data/media`, `VARK_YTDLP_DIR=/data/bin`. `.gitignore` ignores `.env`, `bin/`, `backend/web/dist/*` (except the placeholder `index.html`), `node_modules`, `/data`.
- [ ] **Step 13: `AGENTS.md`** — lean, adapt loom's (module path, pins, commands, migration rule, security invariants, the two vark invariants: cookie-gate + throttle). `README.md` — short overview + `docker compose up`.
- [ ] **Step 14: Verify full build.** Run: `make fe-build && make build && make test && make fe-test`. Expected: binary at `bin/vark`, all tests PASS. Run `./bin/vark &` then `curl -s localhost:8080/healthz` → `ok`; `curl -s localhost:8080/` → contains `root`.
- [ ] **Step 15: Create the private GitHub repo + push.** Confirm with the user this targets `trick77/vark` (personal, private). Then:
```bash
gh repo create trick77/vark --private --source=. --remote=origin --push=false
git add -A && git commit -m "chore: scaffold vark (go backend, react ui, ci, containerfile)"
git push -u origin feat/phase-1-scaffold
```
Open a PR to `master`; confirm CI (backend + ui jobs) goes green. Merge. (Release build on master produces the first `ghcr.io/trick77/vark` image — expected to succeed since the skeleton builds.)

---

## Task 2: Config package

**Files:**
- Create: `backend/internal/config/config.go`, `backend/internal/config/config_test.go`.
- Reference: `../loom/backend/internal/config/config.go` (esp. `validateDevAuthLocalOnly`, `env()` helper, `DevUserConfig`).

**Interfaces:**
- Produces: `config.Config` struct { `Addr`, `PublicURL`, `SessionSecret`, `DBPath`, `MediaDir`, `YtdlpDir`, `AuthMode` (`oidc|dev`), `OIDC OIDCConfig`, `Dev DevUserConfig`, `LogLevel` }; `config.Load() (Config, error)`.

- [ ] **Step 1: Failing test — required secret + dev loopback guard.**
```go
func TestLoad_devAuthRejectsNonLoopback(t *testing.T) {
    t.Setenv("VARK_SESSION_SECRET", "x"); t.Setenv("VARK_AUTH_MODE", "dev")
    t.Setenv("VARK_ADDR", "0.0.0.0:8080"); t.Setenv("VARK_PUBLIC_URL", "")
    if _, err := Load(); err == nil { t.Fatal("dev auth on non-loopback must fail") }
}
func TestLoad_devAuthLoopbackOK(t *testing.T) {
    t.Setenv("VARK_SESSION_SECRET", "x"); t.Setenv("VARK_AUTH_MODE", "dev")
    t.Setenv("VARK_ADDR", "127.0.0.1:8080"); t.Setenv("VARK_PUBLIC_URL", "")
    if _, err := Load(); err != nil { t.Fatalf("loopback dev auth must pass: %v", err) }
}
func TestLoad_missingSecretFails(t *testing.T) {
    t.Setenv("VARK_SESSION_SECRET", ""); if _, err := Load(); err == nil { t.Fatal("missing secret must fail") }
}
```
- [ ] **Step 2: Run — expect FAIL** (no `Load`).
- [ ] **Step 3: Implement** `config.go`: adapt loom's `Load()` and `validateDevAuthLocalOnly` (loopback hosts `localhost`/`127.0.0.1`/`::1`; require empty-or-loopback `PublicURL`). Defaults: `Addr=:8080`, `DBPath=/data/vark.db`, `MediaDir=/data/media`, `YtdlpDir=/data/bin`. OIDC fields required only when `AuthMode==oidc`.
- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** `feat: config loader with dev-auth loopback guard`.

---

## Task 3: Store — SQLite open, migration runner, 0001 schema

**Files:**
- Create: `backend/internal/store/store.go`, `backend/internal/store/migrate.go`, `backend/internal/store/migrations/0001_init.sql`, `backend/internal/store/store_test.go`.
- Reference: `../loom/backend/internal/store/{store.go,migrate.go}`, `../loom/backend/internal/store/migrations/0001_init.sql`.

**Interfaces:**
- Produces: `store.Open(path string) (*sql.DB, error)` (WAL, `busy_timeout=10000`, `foreign_keys=on`, sqlite-vec extension linked); `store.Migrate(db *sql.DB) error` (applies embedded `migrations/*.sql` in lexicographic order, records in `schema_migrations`).

- [ ] **Step 1: Failing test — migrate creates tables.**
```go
func TestMigrate_createsCoreTables(t *testing.T) {
    db, err := Open(filepath.Join(t.TempDir(), "t.db")); if err != nil { t.Fatal(err) }
    if err := Migrate(db); err != nil { t.Fatal(err) }
    for _, tbl := range []string{"settings","videos","download_jobs","schema_migrations"} {
        var n int
        if err := db.QueryRow("select count(*) from sqlite_master where type='table' and name=?", tbl).Scan(&n); err != nil || n != 1 {
            t.Fatalf("table %s missing (n=%d err=%v)", tbl, n, err)
        }
    }
}
func TestMigrate_idempotent(t *testing.T) { /* Open+Migrate twice on same file → no error */ }
```
- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement** `store.go` (copy loom's driver wiring: `ncruces/go-sqlite3` + `sqlite-vec-go-bindings/ncruces`, `vecLiteral` helper for later) and `migrate.go` (copy loom's embedded runner).
- [ ] **Step 4: Write `0001_init.sql`** — the three tables per the Data model above. `settings` seeded with one row (`INSERT OR IGNORE ... id=1`, defaults: `format_preset='apple-1080p'`, `retention_days=14`, `throttle_base_seconds=10`, `min_free_gb=5`, `cookie_status='absent'`). FK `download_jobs.video_id → videos(id) ON DELETE CASCADE`.
- [ ] **Step 5: Run — expect PASS.**
- [ ] **Step 6: Commit** `feat: sqlite store + migration runner + 0001 schema`.

---

## Task 4: Auth — OIDC + sessions + dev auto-login

**Files:**
- Create: `backend/internal/auth/{oidc.go,session.go,user_store.go,middleware.go,model.go}`, `backend/internal/auth/session_test.go`, `backend/internal/store/migrations/0002_auth.sql` (`users`, `sessions` — SHA-256-hashed tokens, per loom).
- Reference: `../loom/backend/internal/auth/*`, `../loom/backend/internal/store/migrations/0001_init.sql` (users/sessions DDL).

**Interfaces:**
- Produces: `auth.Service` (`StartLogin`, `HandleCallback`, `CreateSessionFromClaims`, `Revoke`); `auth.Middleware.RequireAuth(next) http.Handler`; `auth.Claims`; single-user note: admin == the one authenticated user (no admin group gating needed for P1, keep the field for parity).

- [ ] **Step 1: Failing test — session round-trip.** Create session from claims → look up by raw token → returns the user; expired session → not found; revoked → not found. (Adapt loom's `session_test.go`.)
- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement** by porting loom's `auth` package + `0002_auth.sql`. Drop multi-user/admin-group specifics beyond a single role field. Keep opaque 32-byte token, SHA-256 at rest, `vark_session` cookie (HttpOnly, SameSite=Lax, Secure when PublicURL https), 30-day TTL.
- [ ] **Step 4: Run — expect PASS.**
- [ ] **Step 5: Commit** `feat: authentik OIDC + sessions + middleware`.

---

## Task 5: HTTP server skeleton + wiring + boot milestone

**Files:**
- Create: `backend/internal/httpapi/{server.go,auth_handlers.go,health_handlers.go}`, `backend/internal/sse/sse.go`, `backend/internal/httpapi/server_test.go`.
- Modify: `backend/cmd/vark/main.go` (full wiring).
- Reference: `../loom/backend/internal/httpapi/server.go` (route table + `Deps` + `requireAuth`), `../loom/backend/internal/httpapi/auth_handlers.go` (dev short-circuit `handleAuthLogin`), `../loom/backend/internal/sse/sse.go`.

**Interfaces:**
- Produces: `httpapi.New(deps Deps) http.Handler`; `Deps` struct (Store handles, AuthService, DevAuthClaims, SSE, config); routes registered: `GET /healthz`, `GET /api/auth/{login,callback,logout,me}`, and a placeholder `GET /api/videos` returning `[]`.

- [ ] **Step 1: Failing test — dev auth yields a session, `/api/videos` empty.**
```go
func TestServer_devLoginThenEmptyVideos(t *testing.T) {
    h := New(testDeps(t /* dev claims set */))
    // GET /api/auth/login (dev) sets vark_session cookie
    // reuse cookie → GET /api/videos → 200, body "[]"
}
```
- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement** `server.go` (route table, `Deps`, `requireAuth` wrapper), `auth_handlers.go` (port loom incl. `if devAuthClaims.Subject != "" { createSessionFromClaims(...) }` short-circuit), `health_handlers.go`, and `sse.go` (port loom). Placeholder `handleListVideos` returns `[]`.
- [ ] **Step 4: Wire `main.go`:** `config.Load` → `store.Open`+`Migrate` → build `auth.Service` (OIDC discovery only when `AuthMode==oidc`; build `DevAuthClaims` when `dev`) → `httpapi.New` → `http.Server` on `cfg.Addr` with `signal.NotifyContext` graceful shutdown. Log a `dev auth enabled; loopback only` warning in dev.
- [ ] **Step 5: Run — expect PASS.**
- [ ] **Step 6: Manual smoke:** `VARK_SESSION_SECRET=x VARK_AUTH_MODE=dev VARK_ADDR=127.0.0.1:8080 ./bin/vark`, then `curl -c j -s localhost:8080/api/auth/login` and `curl -b j -s localhost:8080/api/videos` → `[]`.
- [ ] **Step 7: Commit** `feat: http server skeleton with dev auto-login and SSE`.

---

## Task 6: Settings store + cookie validation + Settings API

**Files:**
- Create: `backend/internal/settings/store.go`, `backend/internal/cookie/netscape.go`, `backend/internal/httpapi/settings_handlers.go`, tests `settings/store_test.go`, `cookie/netscape_test.go`, `httpapi/settings_handlers_test.go`.
- Modify: `backend/internal/httpapi/server.go` (register routes).

**Interfaces:**
- Produces: `settings.Store` (`Get() (Settings, error)`, `Update(patch) error`, `SetCookie(text string, status string) error`, `CookieStatus() string`); `cookie.Parse(text string) (Cookies, error)` + `cookie.Validate(text string) error` (Netscape parse; reject if no `.youtube.com` lines or missing key cookies `SID`/`__Secure-3PSID`); routes `GET /api/settings` (**cookie body never returned** — only `cookie_status`+`cookie_updated_at`), `PUT /api/settings`, `PUT /api/settings/cookie`, `GET /api/cookie/health`.
- Consumes: store DB from Task 3.

- [ ] **Step 1: Failing test — cookie validation.**
```go
func TestValidate_rejectsGarbage(t *testing.T){ if cookie.Validate("hello") == nil { t.Fatal("garbage must fail") } }
func TestValidate_rejectsNoYoutube(t *testing.T){ if cookie.Validate("# Netscape HTTP Cookie File\n.example.com\tTRUE\t/\tTRUE\t0\tX\ty") == nil { t.Fatal() } }
func TestValidate_acceptsMinimalYoutube(t *testing.T){
    ok := "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1789000000\tSID\tabc\n.youtube.com\tTRUE\t/\tTRUE\t1789000000\t__Secure-3PSID\tdef\n"
    if err := cookie.Validate(ok); err != nil { t.Fatalf("valid cookie rejected: %v", err) }
}
```
- [ ] **Step 2: Run — expect FAIL.**
- [ ] **Step 3: Implement** `cookie/netscape.go` (tab-split parser; collect domains + names; `Validate` enforces youtube domain + at least one session cookie name).
- [ ] **Step 4: Failing test — GET settings hides cookie body.** `settings_handlers_test`: after `PUT /api/settings/cookie` with a valid cookie, `GET /api/settings` JSON has `cookie_status="valid"` and **no** `cookie_text` field.
- [ ] **Step 5: Run — FAIL.**
- [ ] **Step 6: Implement** `settings/store.go` (+ `SetCookie` sets `cookie_status='valid'` after `cookie.Validate`, `cookie_updated_at=now`) and `settings_handlers.go` (serializer omits cookie body). Register routes.
- [ ] **Step 7: Run — expect PASS.**
- [ ] **Step 8: Commit** `feat: settings store, write-only cookie with netscape validation`.

---

## Task 7: yt-dlp wrapper — cookie gate, throttle, metadata, error taxonomy, self-update

**Files:**
- Create: `backend/internal/ytdlp/{ytdlp.go,errors.go,meta.go,url.go,selfupdate.go}`, tests `ytdlp/{ytdlp_test.go,url_test.go,errors_test.go}`, `backend/internal/ytdlp/testdata/fake-ytdlp.sh` (a stub `yt-dlp` for tests).
- Reference: `../tubearchivist/backend/download/src/yt_dlp_base.py` (`YtWrap`: cookie StringIO, bot-detection, base opts), `../tubearchivist/backend/common/src/helper.py` (`rand_sleep`), `../tubearchivist/backend/common/src/urlparser.py` (URL shapes).

**Interfaces:**
- Produces:
  - `ytdlp.Runner` with `New(cfg RunnerConfig) *Runner` where `RunnerConfig{ Bin string; CookieProvider func() (text string, status string); ThrottleBase time.Duration; Sleep func(time.Duration); MediaDir string }`.
  - `(*Runner) Metadata(ctx, url string) (*Meta, error)` — runs `<bin> -J --skip-download --no-playlist <url>`; parses title/channel/duration/thumbnail/publishedAt/availability.
  - `errors.go`: sentinel errors `ErrNoCookie`, `ErrCookieExpired`, `ErrBlocked`, and `TerminalError{Reason}` (deleted/private/members/age/geo); `Classify(stderr string, exitErr error) error`.
  - `url.Canonicalize(raw string) (watchURL, id string, kind string, err error)` — kind ∈ `video|shorts|live|playlist|unknown`.
  - `selfupdate.go`: `Version(ctx) (string, error)` (`<bin> --version`), `UpdateLatest(ctx, dir string) (newVersion string, err error)`.
- Consumes: settings cookie (`Task 6`).

- [ ] **Step 1: Failing test — cookie gate.** With a `CookieProvider` returning `("", "absent")`, `Metadata` returns `ErrNoCookie` **without invoking the binary** (use a fake bin that writes a marker file; assert the marker was NOT created).
```go
func TestMetadata_noCookie_doesNotCallBinary(t *testing.T){
    called := filepath.Join(t.TempDir(),"called")
    r := New(RunnerConfig{Bin: fakeBinTouching(called), CookieProvider: func()(string,string){return "",""}, Sleep: func(time.Duration){}})
    _, err := r.Metadata(context.Background(), "https://youtu.be/abc")
    if !errors.Is(err, ErrNoCookie) { t.Fatalf("want ErrNoCookie, got %v", err) }
    if _, e := os.Stat(called); e == nil { t.Fatal("binary must not run without cookie") }
}
```
- [ ] **Step 2: Run — FAIL.**
- [ ] **Step 3: Implement** the cookie gate + throttle in `ytdlp.go`: every run calls `CookieProvider()`; empty text → `ErrNoCookie`; else writes cookie to a `0600` temp file (guaranteed `defer os.Remove`), calls `Sleep(rand 0.5–1.5×ThrottleBase)`, execs the binary with `--cookies <tmp>`. Inject `Sleep`/`Bin` for tests.
- [ ] **Step 4: Failing test — error classification.** Feed known stderr strings to `Classify`: `"Sign in to confirm you're not a bot"` → `ErrBlocked`; `"Private video"` → `TerminalError`; `"This video is no longer available"`/`"Video unavailable"` → `TerminalError`; `"HTTP Error 429"` → retryable (returns a `*RetryableError` or nil-sentinel you define); cookie-invalid signature → `ErrCookieExpired`.
- [ ] **Step 5: Run — FAIL.** **Step 6: Implement** `errors.go` `Classify`.
- [ ] **Step 7: Failing test — URL canonicalization.**
```go
cases := map[string][2]string{ // input -> {watchURL, id}
 "https://youtu.be/dQw4w9WgXcQ":            {"https://www.youtube.com/watch?v=dQw4w9WgXcQ","dQw4w9WgXcQ"},
 "https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PL123": {"https://www.youtube.com/watch?v=dQw4w9WgXcQ","dQw4w9WgXcQ"},
 "https://www.youtube.com/shorts/abc12345678": {"https://www.youtube.com/watch?v=abc12345678","abc12345678"},
}
```
Also assert a bare `-`-leading id string is only accepted as part of a full URL (never returns a bare-id command).
- [ ] **Step 8: Run — FAIL. Step 9: Implement** `url.go` (parse host/path/query; strip `list`; map `/shorts/`,`/live/`,`youtu.be/` → `watch?v=`; classify kind).
- [ ] **Step 10: Failing test — Metadata parse** using `fake-ytdlp.sh` that prints a canned `-J` JSON; assert `Meta.Title/ChannelID/DurationSeconds` parsed.
- [ ] **Step 11: Run — FAIL. Step 12: Implement** `meta.go`.
- [ ] **Step 13: selfupdate** — `Version` parses `<bin> --version`; `UpdateLatest` downloads the latest release binary into `dir` (test with a fake that just writes a file and returns a version). Test both.
- [ ] **Step 14: Commit** `feat: yt-dlp wrapper (cookie gate, throttle, classify, url, selfupdate)`.

---

## Task 8: yt-dlp download — staging, args, progress, atomic placement, cleanup

**Files:**
- Create: `backend/internal/ytdlp/download.go`, `backend/internal/ytdlp/format.go`, tests `ytdlp/download_test.go`, `ytdlp/format_test.go`.
- Reference: `../tubearchivist/backend/download/src/yt_dlp_handler.py` (`_build_obs*`: format, ratelimit, outtmpl, continuedl, merge_output_format).

**Interfaces:**
- Produces:
  - `format.Presets` map (id → yt-dlp `-f` string) incl. `apple-1080p` = `bestvideo[height<=1080][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4`, `apple-720p`, `best-mp4`; `format.Resolve(preset, custom string) (string, error)`.
  - `(*Runner) Download(ctx, req DownloadReq, onProgress func(Progress)) (*Result, error)` where `DownloadReq{ URL, VideoID, Format, LimitRate string }`; downloads into `MediaDir/.staging/<id>/`, then atomically renames to `MediaDir/<channelID>/<id>/`. `Result{ MediaPath, ThumbnailPath, FilesizeBytes, FormatUsed, SponsorblockSegments []Segment }`.
  - Progress parsed from `--newline` stdout (`percent`, `speed`, `eta`).
- Consumes: cookie gate/throttle from Task 7.

- [ ] **Step 1: Failing test — format resolve.** `Resolve("apple-1080p","")` == the avc1 string; `Resolve("custom","bestvideo+bestaudio")` == `"bestvideo+bestaudio"`; `Resolve("custom","")` → error.
- [ ] **Step 2: FAIL → Step 3: Implement `format.go`.**
- [ ] **Step 4: Failing test — download args + staging + atomic placement** using `fake-ytdlp.sh` that: prints two `[download] 10.0%`/`100%` lines, writes a dummy mp4 + `.info.json` (with a `sponsorblock`/`chapters` stub) into the `-o` target dir, exits 0. Assert: (a) `onProgress` called with parsed percents; (b) final file exists under `MediaDir/<channel>/<id>/` (not `.staging`); (c) `.staging` cleaned; (d) args include `--limit-rate`, `--merge-output-format mp4`, `--sponsorblock-mark all`, `--write-info-json`, `--no-playlist`, `--cookies`.
- [ ] **Step 5: Run — FAIL.**
- [ ] **Step 6: Implement `download.go`:** build args from `format.Resolve`, `--limit-rate` (if set), `-o "<staging>/%(id)s.%(ext)s"`, `--merge-output-format mp4`, `--write-thumbnail`, `--write-info-json`, `--sponsorblock-mark all`, `--no-playlist`, `--newline`, `--socket-timeout 30`, `--continue`, `--cookies <tmp>`. Parse progress lines. On exit 0: read `<id>.info.json` for `sponsorblock`/`chapters`, atomically `os.Rename` staging dir → final. On any exit: leave staging for a resumed `--continue` only if retryable; on terminal/cancel, `RemoveAll(staging/<id>)`. Read SponsorBlock segments from the **info-json of this download** (not from a prior metadata call).
- [ ] **Step 7: Run — PASS. Step 8: Commit** `feat: yt-dlp download with staging, atomic placement, progress`.

---

## Task 9: download_jobs store + single-concurrency worker (retry, watchdog, pause)

**Files:**
- Create: `backend/internal/jobs/store.go`, `backend/internal/videos/store.go`, `backend/internal/download/worker.go`, tests `jobs/store_test.go`, `download/worker_test.go`.
- Reference: `../loom/backend/internal/httpapi/memory_worker.go` (ticker + `safely`/recover + boot reset pattern), `../tubearchivist/backend/download/src/yt_dlp_handler.py` (`run_queue` claim loop).

**Interfaces:**
- Produces:
  - `jobs.Store`: `Enqueue(videoID string, priority int) (id int64, err error)`, `ClaimNext() (*Job, error)` (oldest `pending` by `priority desc, enqueued asc`; sets `running`), `Finish(id, state, lastErr, logTail)`, `Bump(id int64, attempts int, lastErr string)` (back to `pending`), `Cancel(id)`, `ResetOrphans()` (running→pending on boot), `List() ([]Job, error)`.
  - `videos.Store`: `Upsert(v Video)`, `Get(id)`, `SetStatus(id,status,errMsg)`, `SetDownloaded(id, res)`, plus lifecycle methods used later.
  - `download.Worker` with `New(deps) *Worker` and `Run(ctx)`; and `Cancel(jobID int64)` (kills the running child).
- Consumes: `ytdlp.Runner` (Tasks 7–8), settings (`Task 6`).

- [ ] **Step 1: Failing test — claim ordering + retry classification.** Enqueue three jobs (priorities 0, 10, 0). `ClaimNext` returns the priority-10 one first, then FIFO. A `Bump` returns a job to `pending` with incremented `attempts`; when `attempts >= max_attempts` a `Finish(..,"failed",..)` leaves it `failed`.
- [ ] **Step 2: FAIL → Step 3: Implement `jobs/store.go`** (SQL with `UPDATE ... RETURNING` or a transaction for claim).
- [ ] **Step 4: Failing test — worker happy path + block pause.** Inject a fake `Runner` whose `Download`:
  - test A: succeeds → worker marks video `downloaded`, job `done`, `onProgress` streamed.
  - test B: returns `ErrBlocked` → worker **pauses** (sets a paused flag, does NOT burn `attempts`, leaves job `pending`), and stops claiming until `Resume()`/cookie re-validated.
  - test C: returns `TerminalError` → job `failed` immediately (no retry), video `error` with message.
  - test D: returns a retryable error → job back to `pending`, `attempts++`, backoff computed.
- [ ] **Step 5: FAIL → Step 6: Implement `worker.go`:** single goroutine; loop `ClaimNext`; per job wrap in `recover()`; run `Runner.Download` with a `context.WithTimeout` **watchdog** (kills the child if no progress for N seconds); classify result → `Finish`/`Bump`/pause. Boot calls `ResetOrphans()`. `Cancel(jobID)` cancels the running job's context (kills process group) and marks `canceled`. Pause on `ErrBlocked`/`ErrCookieExpired` + set `settings.cookie_status`.
- [ ] **Step 7: Run — PASS. Step 8: Commit** `feat: download queue store + single-concurrency worker with retry/pause/watchdog`.

---

## Task 10: Downloads API — add (canonicalize+metadata+enqueue), cancel, list, SSE

**Files:**
- Create: `backend/internal/httpapi/downloads_handlers.go`, test `httpapi/downloads_handlers_test.go`.
- Modify: `server.go` (routes), `main.go` (start the worker goroutine).

**Interfaces:**
- Produces routes: `POST /api/downloads` (body `{url}`), `GET /api/downloads` (queue list), `POST /api/downloads/{id}/cancel`, `GET /api/downloads/stream` (SSE progress). `POST` flow: `url.Canonicalize` → reject `playlist` kind with a clear 400 (`"Paste a single video link, not a playlist"`); reject `live`/premiere with a clear message; else `Runner.Metadata` (surfaces `ErrNoCookie` as a 409 `cookie required`) → `videos.Upsert(status=queued)` → `jobs.Enqueue(priority=10)`.
- Consumes: `ytdlp`, `videos`, `jobs`, worker, sse.

- [ ] **Step 1: Failing test — POST canonicalizes + enqueues; no-cookie → 409.** With a fake Runner (cookie present) and pasted `youtu.be/<id>?list=..`, assert a `videos` row `queued` + a `download_jobs` row `pending` priority 10. With cookie absent, assert `409`.
- [ ] **Step 2: FAIL → Step 3: Implement `downloads_handlers.go`.** Register routes; start worker in `main.go` via `go worker.Run(ctx)`; wire SSE feed from the worker's progress callback.
- [ ] **Step 4: Failing test — playlist rejected 400; cancel marks canceled.**
- [ ] **Step 5: FAIL → Step 6: Implement.**
- [ ] **Step 7: Run — PASS. Step 8: Commit** `feat: downloads API (add/cancel/list/SSE)`.

---

## Task 11: Videos API — list/filter, get, delete(tombstone), stream(range), favorite, watched, resume

**Files:**
- Create: `backend/internal/httpapi/videos_handlers.go`, test `httpapi/videos_handlers_test.go`; extend `videos/store.go` + `videos/store_test.go`.
- Reference: Go stdlib `http.ServeContent` (handles Range requests for the player).

**Interfaces:**
- Produces routes: `GET /api/videos?filter=all|unwatched|watched|favorites|downloading`, `GET /api/videos/{id}`, `DELETE /api/videos/{id}` (tombstone: unlink media/thumb, `media_path=NULL`, `status='tombstoned'`, keep row), `POST /api/videos/{id}/favorite` (toggle), `POST /api/videos/{id}/watched` (`{watched:bool}`; manual), `POST /api/videos/{id}/resume` (`{position:float}`; auto-marks `watched` when `position >= 0.9*duration`), `GET /api/videos/{id}/stream` (`http.ServeContent` on `media_path`, sandbox-checked).
- `videos.Store` additions: `SetFavorite`, `SetWatched`, `SetResume` (with the ≥90% auto-watched rule; **re-watch does not reset `watched_at`; un-watch clears `watched`+`watched_at`**), `Tombstone(id)`, `List(filter)`.

- [ ] **Step 1: Failing test — resume ≥90% auto-marks watched, and re-watch doesn't reset.**
```go
// duration 100s: SetResume(id,95) => watched=1, watched_at set (t0)
// SetResume(id,98) again later => watched_at unchanged (no life extension)
// SetWatched(id,false) => watched=0, watched_at cleared (rescued)
```
- [ ] **Step 2: FAIL → Step 3: Implement** the store rules + handlers.
- [ ] **Step 4: Failing test — delete tombstones (row kept, file gone).** Create a video with a temp media file → `DELETE` → file removed, row still present with `status='tombstoned'`, `media_path` null.
- [ ] **Step 5: FAIL → Step 6: Implement.** Add path-safety guard (reject `..`/abs/symlink escape) shared helper for `media_path`.
- [ ] **Step 7: Failing test — stream serves Range.** `httptest` GET with `Range: bytes=0-3` → `206` + `Content-Range`.
- [ ] **Step 8: FAIL → Step 9: Implement** via `http.ServeContent`.
- [ ] **Step 10: Run — PASS. Step 11: Commit** `feat: videos API (list/get/delete-tombstone/stream/favorite/watched/resume)`.

---

## Task 12: Retention sweeper + disk-space guard

**Files:**
- Create: `backend/internal/retention/sweeper.go`, test `retention/sweeper_test.go`.
- Modify: `download/worker.go` (disk-space precheck), `main.go` (start sweeper tick).

**Interfaces:**
- Produces: `retention.Sweeper` with `Run(ctx)` (ticks, e.g. hourly) calling `SweepOnce()`; deletes videos where `watched=1 AND favorite=0 AND watched_at < now-retention_days`, **excluding** the currently-playing video (a `NowPlayingGuard` interface: `IsActive(id) bool`, fed by a last-stream-access timestamp). Deletion = tombstone (reuse `videos.Tombstone`).
- Produces: `download.freeBytes(dir) (uint64, error)` (statfs); worker refuses to start a job when free space `< settings.min_free_gb`, sets a paused/banner state instead.

- [ ] **Step 1: Failing test — sweep respects favorite, unwatched, age, and now-playing.** Seed four videos: watched+old+not-fav (delete), watched+old+fav (keep), unwatched+old (keep), watched+old+not-fav-but-now-playing (keep). Assert only the first is tombstoned.
- [ ] **Step 2: FAIL → Step 3: Implement `sweeper.go`.**
- [ ] **Step 4: Failing test — worker pauses under min-free.** Inject a `freeBytes` stub returning below threshold → `ClaimNext` job is not started; worker exposes a `LowDisk` state.
- [ ] **Step 5: FAIL → Step 6: Implement** disk guard; wire sweeper + guard in `main.go`.
- [ ] **Step 7: Run — PASS. Step 8: Commit** `feat: retention sweep (tombstone) + disk-space guard`.

---

## Task 13: Frontend foundation — theme, fonts, icons, API client, shell

**Files:**
- Create: `ui/src/index.css` (music `@theme` tokens + `@font-face`), `ui/src/fonts/*` (copy `../music/ui/src/fonts/{SansWebVariable-TextRegular.woff2,SerifWebVariable-TextRegular.woff2}`), `ui/src/icons.tsx`, `ui/src/api/{http.ts,stream.ts,types.ts,videos.ts,downloads.ts,settings.ts,auth.ts,index.ts}`, `ui/src/shell/{Rail.tsx,TopBar.tsx,DownloadDock.tsx,CookieStatus.tsx}`, `ui/src/App.tsx`, tests `ui/src/api/http.test.ts`, `ui/src/shell/Rail.test.tsx`.
- Reference: `../music/ui/src/index.css` (`@theme`), `../music/ui/src/{Icon.tsx,Glyph.tsx}`, `../loom/ui/src/api/{http.ts,stream.ts}`.

**Interfaces:**
- Produces: design tokens exactly per the plan's design section (bg `#1F1F1E`, panel `#1B1B1A`, accent `#C6613F`/strong `#D97757`/fill `#C25F34`, kept `#D6A15A`, online `#5AA06A`, danger `#C14638`); `Icon`/`Glyph` wrapping `lucide-react` at `strokeWidth={1.9}`; `api.get/post/put` + `AuthExpiredError`; `streamSSE(path, onEvent)`; `Rail`, `TopBar`, `DownloadDock`, `CookieStatus` components.

- [ ] **Step 1: Failing test — API client 401 handling.** `http.test.ts`: mock `fetch` → 401 → `api.get` throws `AuthExpiredError`.
- [ ] **Step 2: FAIL → Step 3: Implement `http.ts` + `stream.ts`** (port loom).
- [ ] **Step 4: Failing test — Rail renders nav + reflects cookie status.** `Rail.test.tsx`: renders Library/Now playing/Add/New & pending/Settings; `CookieStatus` shows "active" for status `valid`, a warning for `absent`.
- [ ] **Step 5: FAIL → Step 6: Implement** `index.css` (copy music `@theme`, add `--kept`), `icons.tsx`, shell components, `App.tsx` shell (rail + routed main; manual view state like loom, no router lib).
- [ ] **Step 7: Run `vitest --run` — PASS. Step 8: Commit** `feat: ui foundation (music theme, fonts, lucide, api client, shell)`.

---

## Task 14: Frontend views — Library, Add, Player, Settings

**Files:**
- Create: `ui/src/views/{Library.tsx,Add.tsx,Player.tsx,Settings.tsx}`, `ui/src/components/{VideoCard.tsx,Scrubber.tsx}`, tests `ui/src/views/{Library.test.tsx,Player.test.tsx,Settings.test.tsx}`.
- Reference: the approved mockup (published Artifact) for exact layout/markup; port its structure into components.

**Interfaces:**
- Consumes: `api.videos`, `api.downloads`, `api.settings`, `streamSSE`.
- Produces: **Library** (filter chips + grid of `VideoCard`; card shows thumbnail+duration+resume bar, download ring for `downloading`, "NEW" tag, and the lifecycle line: "Kept forever" (favorite) / "Expires in N days" (watched, computed from `watched_at`+`retention_days`) / "Not watched yet"); **Add** (paste box → `POST /api/downloads`; shows cookie-required + format preset); **Player** (HTML5 `<video>` with `src=/api/videos/{id}/stream`, resume from `resume_position_seconds`, periodic `POST resume` on `timeupdate` throttled + a `visibilitychange`/`pagehide` flush, client-side SponsorBlock skip from `sponsorblock_segments`, favorite/watched/delete controls, **"Watch on YouTube"** external link to `video.url`, and Summary/Contents/Transcript panels rendered empty-with-"coming in a later update" until P3); **Settings** (cookie textarea → `PUT /api/settings/cookie` with client-side "looks valid" hint + server validation; format preset picker + custom; `limit_rate`; throttle base; retention slider; **yt-dlp version + Update button**).

- [ ] **Step 1: Failing test — VideoCard lifecycle line.** `Library.test.tsx`: a favorite video renders "Kept forever"; a watched non-favorite renders "Expires in N days"; a downloading video renders the progress ring.
- [ ] **Step 2: FAIL → Step 3: Implement `VideoCard.tsx` + `Library.tsx`.**
- [ ] **Step 4: Failing test — Player resumes + posts position.** `Player.test.tsx`: mounts with `resume_position_seconds=42` → sets `video.currentTime≈42`; firing `timeupdate` posts to `/api/videos/{id}/resume` (mock).
- [ ] **Step 5: FAIL → Step 6: Implement `Player.tsx` + `Scrubber.tsx`** (SponsorBlock overlay + auto-skip; "Watch on YouTube" anchor `target=_blank rel=noreferrer`).
- [ ] **Step 7: Failing test — Settings cookie save round-trips + never shows body.** `Settings.test.tsx`: saving posts to `/api/settings/cookie`; GET shows status, never the cookie text.
- [ ] **Step 8: FAIL → Step 9: Implement `Add.tsx` + `Settings.tsx`.**
- [ ] **Step 10: Run `vitest --run` — PASS. Step 11: Commit** `feat: ui views (library, add, player, settings)`.

---

## Task 15: End-to-end wire-up + verification

**Files:**
- Modify: `backend/cmd/vark/main.go` (ensure worker + sweeper + selfupdate tick started; SSE hub wired), `compose.yaml`, `compose.dev.yaml` (adapt loom's; add `VARK_MEDIA_DIR`, `VARK_YTDLP_DIR` volumes; runtime image has yt-dlp+ffmpeg), `README.md`.

- [ ] **Step 1: Full build + all tests.** Run: `make fe-build && make build && make test && make fe-test`. Expected: all PASS, `bin/vark` present.
- [ ] **Step 2: Live end-to-end (dev auth, real cookie).** Use the `verify` skill / drive the app:
  - `VARK_SESSION_SECRET=dev VARK_AUTH_MODE=dev VARK_ADDR=127.0.0.1:8080 VARK_DB_PATH=./data/vark.db VARK_MEDIA_DIR=./data/media VARK_YTDLP_DIR=./data/bin ./bin/vark`
  - Open `http://localhost:8080` (dev auto-login → admin session, empty Library).
  - Settings → paste a real YouTube cookie → cookie status turns "active". Confirm a download attempt with the cookie cleared is refused (409 / banner) — the hard invariant.
  - Add → paste a short real video URL → row appears `queued`, worker downloads it (SSE progress in the dock), file lands under `VARK_MEDIA_DIR/<channel>/<id>/`, status → `downloaded`, `--limit-rate` respected.
  - Open Player → plays; seek works (Range 206); close mid-video, reopen → resumes at saved position; SponsorBlock segments auto-skip.
  - Set retention to 0–1 day; mark the video watched → the sweep tombstones it; favorite a watched video → it survives; confirm the tombstone row keeps metadata and offers re-download.
  - Cancel a running download → the yt-dlp child is killed, partials cleaned, job `canceled`.
- [ ] **Step 3: Commit** `feat: wire workers + compose; phase-1 core loop complete`.
- [ ] **Step 4: Open PR** `feat/phase-1-...` → `master`; confirm CI green; merge; confirm the release image builds.

---

## Self-Review (author checklist — completed)

- **Spec coverage:** paste-link download (T7,8,10), throttled single-concurrency queue (T9), cookie-gate invariant (T7) + write-only storage (T6), format presets + `--limit-rate` (T8), player with resume (T11,14), watched(auto≥90%+manual)/favorite/tombstone lifecycle (T11), retention sweep + disk guard (T12), SponsorBlock-mark + client skip (T8,14), Watch-on-YouTube link (T14), yt-dlp self-update (T7,14), error taxonomy + cancel + watchdog (T7,9,10), staging/partial cleanup (T8), OIDC + dev auto-login single-user (T4,5), music design system + lucide + fonts (T13,14), repo + CI + dependabot + image cleanup (T1). Deferred correctly to P2/P3: channels/scan, subtitles/summaries/embeddings/search — not in any P1 task.
- **Placeholder scan:** loom/tubearchivist references are concrete source files to port, not TODOs; each task has real test code + exact commands.
- **Type consistency:** `Runner`/`RunnerConfig`/`Meta`/`DownloadReq`/`Result` (T7–8), `jobs.Store`/`Job`/`ClaimNext`/`Bump` (T9), `videos.Store.SetResume`/`Tombstone` (T11) referenced consistently across tasks.

## Notes carried forward (from the gap review, not yet full P1 tasks)
- SSE reconnect/backfill (reconnect gets current state) and an explicit "processing/merging" phase during ffmpeg mux — implement inside T10/T9 progress emission; add a focused test if time allows.
- Per-job `log_tail` (last N stderr lines) is stored (T9) and surfaced in the Settings/queue UI — minimal viewer in T14 acceptable; richer log view can slip to a P1.1 follow-up.
- DB backup (WAL + `.backup`/Litestream) — ops concern; document in README (T15), automate later.
