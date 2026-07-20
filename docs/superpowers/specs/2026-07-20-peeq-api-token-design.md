# peeq — API token + machine cookie-write (design)

Date: 2026-07-20
Status: approved (brainstormed 2026-07-20)
Slice: the one after video categorization (PR #11, merge commit `2e6e9b0`)

## Goal

peeq auto-generates a single API token. That token authenticates exactly one
machine endpoint, which writes the YouTube cookie without an OIDC session. This
unblocks the Phase 4 Chrome extension, which pushes the cookie automatically so
the user never hand-pastes Netscape cookie text.

Long-deferred item, originally carried in the `vark-phase3-4-api-token-cookie-extension`
memory since Phase 2.

## Scope

In scope:

- `api_token` + `api_token_created_at` on the `settings` singleton.
- Token generation on startup; regeneration from Settings.
- `RequireToken` middleware.
- `PUT /api/machine/cookie` — the only token-gated route.
- Settings UI section: masked token, Show, Copy, Regenerate (with confirm).
- Four deferred test-coverage minors carried from the categorization branch
  (see "Folded-in test minors").

Out of scope:

- The Chrome extension itself (Phase 4).
- Any broader machine surface (downloads, status, library reads). Deliberate
  least-privilege: the extension only needs to push a cookie.
- The extension-pairing UI block (endpoint URL + setup steps). Rejected for
  this slice because it forces the backend to know its own public base URL —
  a `BACKEND_PUBLIC_URL` env var or Host-header derivation. Revisit in Phase 4
  when the extension's real configuration needs are known rather than guessed.

## Decisions

### Storage: settings singleton, plaintext

`api_token TEXT NOT NULL DEFAULT ''` on the settings row.

The token is stored plaintext at rest. This is a chosen trade-off, not an
oversight, and the reasoning is recorded here so it can be re-examined rather
than rediscovered:

- The Settings UI renders the token masked with Show and Copy buttons, which
  requires a recoverable value. Hashing forces a show-once-at-generation flow.
- The same row already stores `cookie_text` — a live YouTube session
  credential — in plaintext, so a hashed token would protect strictly less than
  the secret sitting next to it.
- Threat model: single-user, self-hosted.

If the Show/Copy affordance is dropped, hashing becomes the better choice and
this decision should be revisited.

Rejected: a dedicated `api_tokens` table with labels and multiple live tokens.
No second consumer exists; speculative for a single-user app.

### Token format

32 bytes from `crypto/rand`, base64url-encoded (no padding), prefixed `peeq_`.
32 bytes → 43 base64url chars, plus the 5-char prefix = 48 chars total. The prefix makes the token greppable in logs and recognizable
when pasted into the wrong field.

### Lifecycle

- **First boot**: after `Migrate`, `EnsureToken` fills the column if empty.
  Idempotent, so it runs unconditionally on every startup.
- **Regeneration**: overwrites the single stored value. The old token stops
  working on the next request. No revocation list, no expiry, no grace period —
  the single-value model gives immediate invalidation for free.

### Auth: separate middleware, separate route

`RequireToken` sits alongside the existing `RequireAuth`. It reads
`Authorization: Bearer <token>` and compares with
`crypto/subtle.ConstantTimeCompare`. It rejects when the stored token is empty,
so a blank column can never authenticate a request.

It does **not** put a `User` in the request context. A token request is not a
session; handlers behind it must not assume `auth.UserFromContext` works.

One route is token-gated: `PUT /api/machine/cookie`. The existing
`PUT /api/settings/cookie` keeps `RequireAuth` and is otherwise untouched.

Rejected: making `PUT /api/settings/cookie` accept either credential. One route
= one auth mode keeps the OIDC bypass surface greppable in `server.go`; a
dual-auth route sitting among session routes gets harder to audit as routes grow.

### API exposure: the token is not in the Settings struct

`settings.Settings` exists precisely so that nothing secret can be serialized
out of `GET /api/settings` — see its doc comment and the `cookie_text`
precedent. Adding the token to it would put a live credential in every settings
response.

Two dedicated OIDC-gated routes instead:

- `GET /api/settings/token` → `{"token": "...", "created_at": "..."}`
- `POST /api/settings/token/regenerate` → the same shape, new value

### Migration: one more in-place `0001` squash

`internal/store/migrate.go` is already a real sequenced runner —
`schema_migrations` version table, embedded `migrations/*.sql`, lexicographic
order, one transaction per file, skipping recorded versions. Append-only was
therefore never blocked by missing infrastructure, and a `0002_*.sql` would have
cost one file.

The user chose to squash into `0001` once more anyway (fourth time). Accepted:
there is still no prod DB.

**Consequences, recorded deliberately:**

- Every existing dev DB must be recreated on upgrade. There is no backfill —
  `EnsureToken` populates the column on the next startup regardless.
- The multi-migration upgrade path remains unexercised in CI. The first
  append-only migration will be the first time that code path runs for real.

## Components

| Unit | Responsibility | Depends on |
|---|---|---|
| `internal/apitoken` | Generate a token; `EnsureToken`; `Regenerate` | `crypto/rand`, settings store |
| `settings.Store` | Persist/read `api_token`, `api_token_created_at` | DB |
| `auth.Middleware.RequireToken` | Constant-time bearer check; 401 otherwise | settings store |
| `httpapi` `applyCookie` helper | Shared cookie-write body | `cookie.Validate`, `settings.SetCookie`, worker resume |
| `httpapi` machine + token routes | Wire the above | above |
| `ui` Settings token section | Mask/Show/Copy/Regenerate | `api/settings` |

`apitoken` is deliberately its own package so generation is testable with an
injected `io.Reader` and has no HTTP or UI dependency.

## Data flow

**Startup**: `Migrate` → `EnsureToken(ctx)` → column populated if empty.

**Extension pushes a cookie**:
`PUT /api/machine/cookie` + `Authorization: Bearer peeq_…`
→ `RequireToken` (constant-time compare; 401 on mismatch/empty)
→ `applyCookie` → `cookie.Validate` (400 on malformed)
→ `settings.SetCookie` → worker un-wedge (the existing "re-paste and it
resumes" behavior)
→ 200, cookie-body-free settings view.

**User views/regenerates**: OIDC session → `GET /api/settings/token` renders
masked; `POST /api/settings/token/regenerate` replaces it and the UI reveals the
new value immediately, since the user must copy it into the extension.

## UI

A new `.sect` between the YouTube cookie and Download format sections, using the
existing `.sect` / `.status-line` / `.btn` / `.field-row` / `.warnline` / `.lab`
classes — no new design tokens.

- Token field: masked (`••••`) by default, monospace, on the `--color-bg`
  surface that `textarea.cookiebox` already uses. Show/Hide and Copy sit in a
  right-hand action rail inside the field border.
- Copy confirms **inline** (label flips to "Copied", `--color-online`, ~1.6s).
  peeq has no toast system and this slice does not introduce one.
- Regenerate is **two-step**: the button reveals a `.warnline` confirm row,
  because regeneration silently breaks a working extension and has no undo.
- Copy wording is user-facing, not system-facing: the section explains the token
  "lets the peeq browser extension send your YouTube cookie automatically" and
  states that it cannot read the library.

Approved mockup (variant A): interactive, built with the real tokens from
`ui/src/index.css`.

## Error handling

| Case | Result |
|---|---|
| Missing/malformed `Authorization` header | 401 `{"error":"unauthorized"}` |
| Token mismatch | 401, constant-time compare, no timing signal |
| Stored token empty | 401 — a blank column never authenticates |
| Malformed cookie body | 400 `invalid cookie: …` (same as the OIDC route) |
| Settings write failure | 500, existing shape |

Existing invariants are untouched by this slice: no YouTube call without a valid
cookie, the 20s throttle floor + jitter at the Runner's single exec choke point,
the `youtube_paused` kill-switch, and `SafeMediaPath`. This slice adds no
external-call path.

## Testing

Fakes only — no real LLM, embeddings endpoint, or yt-dlp binary. The full
authenticated e2e stays a manual operator step.

- `apitoken`: generation shape/prefix/length with an injected reader; `EnsureToken`
  idempotence; `Regenerate` changes the value and the timestamp.
- `auth`: `RequireToken` accepts a valid bearer, rejects mismatch, malformed
  header, missing header, and empty stored token.
- `httpapi`: machine route happy path writes the cookie; 401 without a token;
  400 on a malformed cookie; `GET /api/settings` still contains no token field
  (regression guard for the "nothing secret in Settings" contract).
- `settings`: token column round-trips.
- UI: masked by default, Show reveals, Copy calls the clipboard, Regenerate
  requires confirmation and then reveals.

### Folded-in test minors

Carried from the categorization branch's SDD ledger, all pure test additions:

1. Store-level `status` + `category` combination filter coverage.
2. Go↔TS category enum **label**-drift detection — currently a label rename in
   `videos/category.go` fails no test. The only one of the four with real
   failure potential.
3. Poller / redownload paths assert they pass `category`.
4. Unknown/garbage category id renders nothing (guard).

## Backlog captured during this brainstorm

**TubeArchivist import** — new, not previously specced. The only existing
TubeArchivist relationship is that its yt-dlp invocation patterns were mined as
a reference when building `internal/ytdlp`; nothing imports a TA archive. If
picked up, it needs its own discovery first: what a TA export actually looks
like (SQLite? Elasticsearch index? a media directory + JSON sidecars?) and how
that maps onto peeq's `videos` / `channels` schema.

Still deferred, unchanged: auto-unsubscribe stale channels + stale filter;
Phase 5 conversational RAG-QA.
