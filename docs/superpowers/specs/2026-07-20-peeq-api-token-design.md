# peeq — API token + machine cookie-write (design)

Date: 2026-07-20
Status: approved (brainstormed 2026-07-20)
Slice: the one after video categorization (PR #11, merge commit `2e6e9b0`)

## Goal

peeq issues a single API token. That token authenticates exactly one machine
endpoint, which writes the YouTube cookie without an OIDC session. This unblocks
the Phase 4 Chrome extension, which pushes the cookie automatically so the user
never hand-pastes Netscape cookie text.

Long-deferred item, carried in the `vark-phase3-4-api-token-cookie-extension`
memory since Phase 2.

## Scope

In scope:

- `api_token_hash` + `api_token_created_at` on the `settings` singleton.
- On-demand token generation and regeneration from Settings.
- `RequireToken` middleware.
- `PUT /api/machine/cookie` — the only token-gated route.
- Settings UI section with three states: no token, just-generated (one-time
  reveal), active.
- Four deferred test-coverage minors carried from the categorization branch
  (see "Folded-in test minors").

Out of scope:

- The Chrome extension itself (Phase 4).
- Any broader machine surface (downloads, status, library reads). Deliberate
  least-privilege: the extension only needs to push a cookie.
- The extension-pairing UI block (endpoint URL + setup steps). Rejected for this
  slice because it forces the backend to know its own public base URL — a
  `BACKEND_PUBLIC_URL` env var or Host-header derivation. Revisit in Phase 4
  when the extension's real configuration needs are known rather than guessed.

## Decisions

### The token is write-only, hashed at rest

This mirrors the existing `cookie_text` contract: the value goes in, and the API
has no path that returns it.

- The DB stores `api_token_hash` — the SHA-256 digest of the token. The
  plaintext is never persisted.
- SHA-256 is sufficient. bcrypt/argon2 exist to slow brute-force against
  low-entropy human passwords; this is 32 bytes of `crypto/rand`, so a fast hash
  costs nothing in security here and keeps per-request verification cheap.
- The plaintext exists in exactly one response body — the generate/regenerate
  call — and thereafter only in React state. Navigating away from Settings
  discards it permanently.

An earlier draft of this spec stored the token in plaintext to support a
masked-with-Show-button UI. That was reversed: the write-only treatment is
better and removes the reveal affordance entirely.

### Generation is on demand, not on first boot

No token exists until the user clicks **Generate token**. Rejected:
auto-generating at first startup (the original Phase 2 sketch), because a hashed
auto-generated token is real but permanently unreadable — the user's first action
would always have to be regenerating it, and "a token exists but cannot be known"
is a confusing thing to explain in a UI.

While no token is set, the machine route simply 401s, which the empty-hash check
below already handles.

### Token format

32 bytes from `crypto/rand`, base64url-encoded (no padding), prefixed `peeq_`.
32 bytes → 43 base64url chars, plus the 5-char prefix = 48 chars total. The
prefix makes the token greppable in logs and recognizable when pasted into the
wrong field.

### Regeneration

Overwrites the stored hash. The previous token stops working on the next
request. No revocation list, no expiry, no grace period — the single-value model
gives immediate invalidation for free.

### Auth: separate middleware, separate route

`RequireToken` sits alongside the existing `RequireAuth`. It reads
`Authorization: Bearer <token>`, hashes the presented value, and compares
digests with `crypto/subtle.ConstantTimeCompare`. It rejects when the stored
hash is empty, so an unconfigured peeq can never authenticate a request.

It does **not** put a `User` in the request context. A token request is not a
session; handlers behind it must not assume `auth.UserFromContext` works.

One route is token-gated: `PUT /api/machine/cookie`. The existing
`PUT /api/settings/cookie` keeps `RequireAuth` and is otherwise untouched.

Rejected: making `PUT /api/settings/cookie` accept either credential. One route
= one auth mode keeps the OIDC bypass surface greppable in `server.go`; a
dual-auth route sitting among session routes gets harder to audit as routes grow.

### API surface

Three routes. Only the second ever emits a secret.

| Route | Auth | Returns |
|---|---|---|
| `GET /api/settings/token` | OIDC | `{"present": bool, "created_at": "..."}` |
| `POST /api/settings/token` | OIDC | `{"token": "peeq_…", "created_at": "..."}` |
| `PUT /api/machine/cookie` | token | cookie-body-free settings view |

`POST /api/settings/token` both creates and regenerates — the operation is
identical, so it is one route rather than two.

The token is not added to `settings.Settings`. That struct exists precisely so
nothing secret can be serialized out of `GET /api/settings`; the status route
returns a boolean instead, which is safe even if it were ever folded in.

### Migration: one more in-place `0001` squash

`internal/store/migrate.go` is already a real sequenced runner —
`schema_migrations` version table, embedded `migrations/*.sql`, lexicographic
order, one transaction per file, skipping recorded versions. Append-only was
therefore never blocked by missing infrastructure, and a `0002_*.sql` would have
cost one file.

The user chose to squash into `0001` once more (fourth time). Accepted: there is
still no prod DB.

**Consequences, recorded deliberately:**

- Every existing dev DB must be recreated on upgrade. No backfill is needed —
  no token exists until the user generates one.
- The multi-migration upgrade path remains unexercised in CI. The first
  append-only migration will be the first time that code path runs for real.

## Components

| Unit | Responsibility | Depends on |
|---|---|---|
| `internal/apitoken` | Generate a token; hash it; verify a presented token | `crypto/rand`, `crypto/sha256`, `crypto/subtle` |
| `settings.Store` | Persist/read `api_token_hash`, `api_token_created_at` | DB |
| `auth.Middleware.RequireToken` | Bearer extraction, verification, 401 | `apitoken`, settings store |
| `httpapi` `applyCookie` helper | Shared cookie-write body | `cookie.Validate`, `settings.SetCookie`, worker resume |
| `httpapi` machine + token routes | Wire the above | above |
| `ui` Settings token section | Three-state UI | `api/settings` |

`apitoken` is deliberately its own package: generation is testable with an
injected `io.Reader`, and it has no HTTP, DB, or UI dependency.

## Data flow

**Generate**: user clicks Generate → `POST /api/settings/token` → 32 random
bytes → encode → hash → store hash + timestamp → return plaintext **once** → UI
shows the one-time reveal panel. Navigating away discards it.

**Extension pushes a cookie**:
`PUT /api/machine/cookie` + `Authorization: Bearer peeq_…`
→ `RequireToken` (hash, constant-time compare; 401 on mismatch or empty hash)
→ `applyCookie` → `cookie.Validate` (400 on malformed)
→ `settings.SetCookie` → worker un-wedge (the existing "re-paste and it resumes"
behavior)
→ 200, cookie-body-free settings view.

## UI

A new `.sect` between the YouTube cookie and Download format sections, using the
existing `.sect` / `.status-line` / `.btn` / `.field-row` / `.warnline` / `.lab`
classes — no new design tokens.

Three states:

1. **No token** — dashed empty state, primary **Generate token** button, and the
   line "You'll see the token once, right after it's created." Header chip reads
   *Not set up* in the muted idle treatment.
2. **Just generated** — the one-time reveal panel: monospace token, Copy button,
   and the heading "Copy this now — it won't be shown again." Panel uses the
   `--color-kept` accent rather than danger red; this is a keep-this moment, not
   an error. Footer explains that peeq stores only a hash, so it cannot show the
   token again. A **Done** button moves to state 3.
3. **Active (returning visit)** — status chip, creation timestamp, and
   **Generate a new token**. Nothing to reveal, because nothing is recoverable.

Details:

- Copy confirms **inline** (label flips to "Copied", `--color-online`, ~1.6s).
  peeq has no toast system and this slice does not introduce one.
- Regeneration is **two-step**: the button reveals a `.warnline` confirm row,
  because it silently breaks a working extension and has no undo.
- The action is worded *Generate a new token*, not *Regenerate* — what the user
  receives is a new secret they must go paste somewhere.
- Copy is user-facing, not system-facing: the section says the token "lets the
  peeq browser extension send your YouTube cookie automatically" and states that
  it cannot read the library.

Approved mockup: three-state interactive prototype built with the real tokens
from `ui/src/index.css`.

## Error handling

| Case | Result |
|---|---|
| Missing/malformed `Authorization` header | 401 `{"error":"unauthorized"}` |
| Token mismatch | 401, constant-time digest compare, no timing signal |
| Stored hash empty (no token generated) | 401 — an unconfigured peeq never authenticates |
| Malformed cookie body | 400 `invalid cookie: …` (same as the OIDC route) |
| Settings write failure | 500, existing shape |

Existing invariants are untouched by this slice: no YouTube call without a valid
cookie, the 20s throttle floor + jitter at the Runner's single exec choke point,
the `youtube_paused` kill-switch, and `SafeMediaPath`. This slice adds no
external-call path.

## Testing

Fakes only — no real LLM, embeddings endpoint, or yt-dlp binary. The full
authenticated e2e stays a manual operator step.

- `apitoken`: generation shape/prefix/length with an injected reader; the same
  token verifies against its own hash; a different token does not; an empty
  stored hash never verifies.
- `auth`: `RequireToken` accepts a valid bearer, rejects mismatch, malformed
  header, missing header, and empty stored hash.
- `httpapi`: machine route happy path writes the cookie; 401 without a token;
  400 on a malformed cookie; `POST /api/settings/token` returns plaintext once
  and `GET /api/settings/token` never returns a token field; `GET /api/settings`
  still contains no token field (regression guard for the "nothing secret in
  Settings" contract).
- `settings`: hash column round-trips; regeneration replaces the stored hash.
- UI: empty state offers Generate; the reveal panel renders the returned token
  and Copy calls the clipboard; the active state renders no token value;
  regeneration requires confirmation.

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
TubeArchivist relationship is that its yt-dlp invocation patterns were mined as a
reference when building `internal/ytdlp`; nothing imports a TA archive. If picked
up, it needs its own discovery first: what a TA export actually looks like
(SQLite? Elasticsearch index? a media directory + JSON sidecars?) and how that
maps onto peeq's `videos` / `channels` schema.

Still deferred, unchanged: auto-unsubscribe stale channels + stale filter;
Phase 5 conversational RAG-QA.
