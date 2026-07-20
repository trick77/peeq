# peeq API Token Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give peeq a write-only API token that authenticates exactly one machine endpoint — the one that writes the YouTube cookie — so the Phase 4 Chrome extension can push cookies without an OIDC session.

**Architecture:** A new `internal/apitoken` package owns generation, hashing, and verification with no HTTP/DB/UI dependencies. The settings singleton stores only a SHA-256 hash. A new `auth.TokenMiddleware` gates one route, `PUT /api/machine/cookie`, which shares its handler body with the existing OIDC cookie route. The Settings UI gains a three-state section; plaintext exists only in the create response and in React state.

**Tech Stack:** Go 1.x (`crypto/rand`, `crypto/sha256`, `crypto/subtle`, `net/http` ServeMux patterns), SQLite via the existing `internal/store` migration runner, React + TypeScript + Vitest + Testing Library.

**Spec:** `docs/superpowers/specs/2026-07-20-peeq-api-token-design.md`

## Global Constraints

- Never commit to `master`. All work lands on branch `feat/api-token` via PR.
- `CGO_ENABLED=0`. Environment variable prefix is `BACKEND_`, never `PEEQ_`.
- All comments in English. Conventional commit messages.
- Backend CI runs `go build`, `go vet`, and `go test -race ./...`. It does **not** run the UI build.
- Tests use fakes only — never a real LLM, embeddings endpoint, or the real yt-dlp binary.
- The token is **write-only**: no endpoint may return the token after its creating response. `settings.Settings` must never gain a token field.
- Token format: 32 bytes from `crypto/rand`, base64url without padding (43 chars), prefixed `peeq_` = 48 chars total.
- Hash comparison must use `crypto/subtle.ConstantTimeCompare`. An empty stored hash must never authenticate.
- Migration is one more in-place edit of `backend/internal/store/migrations/0001_init.sql`. Do **not** create `0002_*.sql`.
- After any `npm run build` in `ui/`, restore the placeholder stub: `git checkout -- backend/web/dist/index.html`. Do not "fix" this; it is deliberate.
- Run backend tests from `backend/`; run UI tests from `ui/`.

## File Structure

**Create:**
- `backend/internal/apitoken/apitoken.go` — generate, hash, verify. No deps beyond stdlib crypto.
- `backend/internal/apitoken/apitoken_test.go`
- `backend/internal/auth/token_middleware.go` — `TokenMiddleware` / `RequireToken`.
- `backend/internal/auth/token_middleware_test.go`
- `backend/internal/httpapi/apitoken_handlers.go` — the two OIDC token routes.
- `backend/internal/httpapi/apitoken_handlers_test.go`
- `ui/src/views/Settings.apitoken.test.tsx` — the new section's tests.
- `ui/src/enumsync.test.ts` — Go↔TS category enum drift guard.

**Modify:**
- `backend/internal/store/migrations/0001_init.sql` — two new settings columns.
- `backend/internal/settings/store.go` — hash accessors.
- `backend/internal/settings/store_test.go`
- `backend/internal/httpapi/settings_handlers.go` — extract `applyCookie`.
- `backend/internal/httpapi/server.go` — Deps field, server field, `requireToken`, three routes.
- `backend/internal/httpapi/settings_handlers_test.go` — machine-route tests.
- `backend/cmd/peeq/main.go` — wire `TokenMiddleware`.
- `backend/internal/videos/store_test.go` — status+category combo coverage.
- `ui/src/api/types.ts`, `ui/src/api/settings.ts` — token client.
- `ui/src/views/Settings.tsx` — the new section.
- `ui/src/views/Library.test.tsx` — poller/redownload category assertions, unknown-category guard.

`apitoken` is its own package so generation is testable with an injected reader and carries no HTTP, DB, or UI dependency. `TokenMiddleware` is a separate type from `Middleware` so `NewMiddleware`'s existing signature and all its callers stay untouched.

---

### Task 1: Settings schema + hash accessors

**Files:**
- Modify: `backend/internal/store/migrations/0001_init.sql:2-18` (settings table)
- Modify: `backend/internal/settings/store.go`
- Test: `backend/internal/settings/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `func (s *Store) SetAPITokenHash(ctx context.Context, hash string) error`
  - `func (s *Store) APITokenHash(ctx context.Context) string` — `""` on error or unset
  - `func (s *Store) APITokenInfo(ctx context.Context) (present bool, createdAt string, err error)`

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/settings/store_test.go`:

```go
func TestAPIToken_roundTripsAndReportsPresence(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// Given: a fresh settings row, no token has ever been generated.
	present, createdAt, err := st.APITokenInfo(ctx)
	if err != nil {
		t.Fatalf("APITokenInfo: %v", err)
	}
	if present {
		t.Fatalf("present = true on a fresh row, want false")
	}
	if createdAt != "" {
		t.Fatalf("createdAt = %q on a fresh row, want empty", createdAt)
	}
	if got := st.APITokenHash(ctx); got != "" {
		t.Fatalf("APITokenHash = %q on a fresh row, want empty", got)
	}

	// When: a hash is stored.
	if err := st.SetAPITokenHash(ctx, "hash-one"); err != nil {
		t.Fatalf("SetAPITokenHash: %v", err)
	}

	// Then: it round-trips and is reported present with a timestamp.
	if got := st.APITokenHash(ctx); got != "hash-one" {
		t.Fatalf("APITokenHash = %q, want %q", got, "hash-one")
	}
	present, createdAt, err = st.APITokenInfo(ctx)
	if err != nil {
		t.Fatalf("APITokenInfo: %v", err)
	}
	if !present {
		t.Fatalf("present = false after storing a hash, want true")
	}
	if createdAt == "" {
		t.Fatalf("createdAt is empty after storing a hash, want a timestamp")
	}

	// When: a second hash replaces it (regeneration).
	if err := st.SetAPITokenHash(ctx, "hash-two"); err != nil {
		t.Fatalf("SetAPITokenHash (regenerate): %v", err)
	}

	// Then: the old hash is gone.
	if got := st.APITokenHash(ctx); got != "hash-two" {
		t.Fatalf("APITokenHash after regenerate = %q, want %q", got, "hash-two")
	}
}

func TestGet_neverCarriesTheAPIToken(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.SetAPITokenHash(ctx, "hash-one"); err != nil {
		t.Fatalf("SetAPITokenHash: %v", err)
	}

	got, err := st.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// The Settings struct is the JSON API's view. A token field here would
	// leak a credential into every GET /api/settings response.
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "hash-one") {
		t.Fatalf("Settings JSON contains the token hash: %s", blob)
	}
	if strings.Contains(strings.ToLower(string(blob)), "api_token") {
		t.Fatalf("Settings JSON has an api_token field: %s", blob)
	}
}
```

If `store_test.go` does not already import `encoding/json` and `strings`, add them. If the file's existing helper for building a store is not named `newTestStore`, use whatever it is named — check the top of the file first and match it.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/settings/ -run 'TestAPIToken|TestGet_never' -v`
Expected: FAIL — `st.APITokenInfo undefined`, `st.SetAPITokenHash undefined`, `st.APITokenHash undefined`.

- [ ] **Step 3: Add the schema columns**

In `backend/internal/store/migrations/0001_init.sql`, inside the `CREATE TABLE settings` block, add two columns immediately after `youtube_paused_at TEXT`:

```sql
    youtube_paused_at      TEXT,
    -- api_token_hash: SHA-256 (hex) of the machine API token. The token
    -- itself is write-only and never persisted — see internal/apitoken.
    api_token_hash         TEXT NOT NULL DEFAULT '',
    api_token_created_at   TEXT
```

Take care that the line before the added columns ends with a comma and the last column line does not.

- [ ] **Step 4: Implement the accessors**

Append to `backend/internal/settings/store.go`:

```go
// SetAPITokenHash stores the SHA-256 hash of the machine API token and
// stamps api_token_created_at. The token plaintext is never persisted: it
// exists only in the response that creates it (see internal/apitoken).
// Calling this again replaces the previous hash, which is how regeneration
// invalidates the old token immediately.
func (s *Store) SetAPITokenHash(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE settings
SET api_token_hash = ?, api_token_created_at = datetime('now')
WHERE id = 1`, hash)
	if err != nil {
		return fmt.Errorf("set api token hash: %w", err)
	}
	return nil
}

// APITokenHash returns the stored token hash for the RequireToken
// middleware. Returns "" if unset or on read error, so an unconfigured or
// unreadable peeq fails safe: an empty hash never authenticates anything.
func (s *Store) APITokenHash(ctx context.Context) string {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT api_token_hash FROM settings WHERE id = 1`).Scan(&hash)
	if err != nil {
		return ""
	}
	return hash
}

// APITokenInfo reports whether a token exists and when it was created,
// without exposing the hash. This backs GET /api/settings/token, which must
// never return anything secret.
func (s *Store) APITokenInfo(ctx context.Context) (bool, string, error) {
	var hash string
	var createdAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT api_token_hash, api_token_created_at FROM settings WHERE id = 1`,
	).Scan(&hash, &createdAt)
	if err != nil {
		return false, "", fmt.Errorf("get api token info: %w", err)
	}
	if !createdAt.Valid {
		return hash != "", "", nil
	}
	return hash != "", createdAt.String, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd backend && go test ./internal/settings/ ./internal/store/ -v`
Expected: PASS. The `store` package tests must still pass — the migration changed.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/store/migrations/0001_init.sql backend/internal/settings/store.go backend/internal/settings/store_test.go
git commit -m "feat(settings): store the API token hash, never the token"
```

---

### Task 2: The apitoken package

**Files:**
- Create: `backend/internal/apitoken/apitoken.go`
- Test: `backend/internal/apitoken/apitoken_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `const Prefix = "peeq_"`
  - `func Generate(r io.Reader) (string, error)`
  - `func Hash(token string) string` — lowercase hex SHA-256
  - `func Verify(presented, storedHash string) bool`

- [ ] **Step 1: Write the failing test**

Create `backend/internal/apitoken/apitoken_test.go`:

```go
package apitoken

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerate_hasThePrefixAndExpectedLength(t *testing.T) {
	// Given: a deterministic reader with 32 bytes available.
	r := bytes.NewReader(bytes.Repeat([]byte{0xAB}, 32))

	// When
	got, err := Generate(r)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Then: peeq_ + 43 base64url chars (32 bytes, unpadded) = 48.
	if !strings.HasPrefix(got, Prefix) {
		t.Fatalf("token %q lacks prefix %q", got, Prefix)
	}
	if len(got) != 48 {
		t.Fatalf("len(token) = %d, want 48 (token=%q)", len(got), got)
	}
	if strings.Contains(got, "=") {
		t.Fatalf("token %q contains base64 padding, want unpadded", got)
	}
}

func TestGenerate_isDeterministicForAGivenReader(t *testing.T) {
	a, err := Generate(bytes.NewReader(bytes.Repeat([]byte{0x01}, 32)))
	if err != nil {
		t.Fatalf("Generate a: %v", err)
	}
	b, err := Generate(bytes.NewReader(bytes.Repeat([]byte{0x01}, 32)))
	if err != nil {
		t.Fatalf("Generate b: %v", err)
	}
	if a != b {
		t.Fatalf("same reader bytes gave different tokens: %q vs %q", a, b)
	}

	c, err := Generate(bytes.NewReader(bytes.Repeat([]byte{0x02}, 32)))
	if err != nil {
		t.Fatalf("Generate c: %v", err)
	}
	if a == c {
		t.Fatalf("different reader bytes gave the same token %q", a)
	}
}

func TestGenerate_errorsWhenTheReaderIsShort(t *testing.T) {
	// Given: only 8 bytes available where 32 are required.
	_, err := Generate(bytes.NewReader([]byte("12345678")))

	// Then: it must fail rather than silently produce a weak token.
	if err == nil {
		t.Fatalf("Generate with a short reader returned nil error")
	}
}

func TestVerify_acceptsOnlyTheMatchingToken(t *testing.T) {
	token, err := Generate(bytes.NewReader(bytes.Repeat([]byte{0x07}, 32)))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	hash := Hash(token)

	if !Verify(token, hash) {
		t.Fatalf("Verify rejected the token that produced the hash")
	}
	if Verify(token+"x", hash) {
		t.Fatalf("Verify accepted a token with a trailing character")
	}
	if Verify("peeq_totally-different-value-that-is-wrong", hash) {
		t.Fatalf("Verify accepted an unrelated token")
	}
}

func TestVerify_neverAcceptsAnEmptyStoredHash(t *testing.T) {
	// An unconfigured peeq must not authenticate anything, including a
	// request that presents the empty string.
	if Verify("", "") {
		t.Fatalf("Verify accepted empty token against empty stored hash")
	}
	if Verify("peeq_anything", "") {
		t.Fatalf("Verify accepted a token against an empty stored hash")
	}
}

func TestHash_isStableAndNotThePlaintext(t *testing.T) {
	const token = "peeq_abc"
	h1 := Hash(token)
	h2 := Hash(token)
	if h1 != h2 {
		t.Fatalf("Hash is not stable: %q vs %q", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("len(Hash) = %d, want 64 hex chars", len(h1))
	}
	if strings.Contains(h1, token) {
		t.Fatalf("hash %q contains the plaintext token", h1)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/apitoken/ -v`
Expected: FAIL — the package does not exist yet (`no Go files in .../apitoken`).

- [ ] **Step 3: Write the implementation**

Create `backend/internal/apitoken/apitoken.go`:

```go
// Package apitoken generates and verifies peeq's single machine API token.
//
// The token is write-only: only its SHA-256 hash is ever persisted (see
// settings.SetAPITokenHash), and the plaintext exists solely in the HTTP
// response that creates it. There is deliberately no way to recover a token
// after that response — losing it means generating a new one.
//
// SHA-256 rather than bcrypt/argon2 is correct here: those exist to slow
// brute-force attacks against low-entropy human passwords, whereas this
// token is 32 bytes of crypto/rand. A fast hash costs nothing in security
// and keeps per-request verification cheap.
package apitoken

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
)

// Prefix marks a peeq API token, making it greppable in logs and
// recognizable when pasted into the wrong field.
const Prefix = "peeq_"

// tokenBytes is the raw entropy per token, before encoding.
const tokenBytes = 32

// Generate reads tokenBytes of entropy from r and returns a prefixed,
// base64url-encoded token. Callers pass crypto/rand.Reader in production and
// a deterministic reader in tests. A short read is an error: a truncated
// token would be a weak credential.
func Generate(r io.Reader) (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("read token entropy: %w", err)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Hash returns the lowercase hex SHA-256 of token. This is the only form of
// the token that is ever persisted.
func Hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Verify reports whether presented is the token behind storedHash, using a
// constant-time compare so a caller cannot recover the hash byte by byte
// from response timing.
//
// An empty storedHash always fails: peeq without a generated token must
// never authenticate a request, including one presenting the empty string.
func Verify(presented, storedHash string) bool {
	if storedHash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(Hash(presented)), []byte(storedHash)) == 1
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/apitoken/ -v`
Expected: PASS — all six tests.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/apitoken/
git commit -m "feat(apitoken): generate, hash, and verify the machine API token"
```

---

### Task 3: RequireToken middleware

**Files:**
- Create: `backend/internal/auth/token_middleware.go`
- Test: `backend/internal/auth/token_middleware_test.go`

**Interfaces:**
- Consumes: `apitoken.Verify` (Task 2).
- Produces:
  - `type TokenHashLookup interface { APITokenHash(context.Context) string }` — satisfied by `*settings.Store` (Task 1)
  - `func NewTokenMiddleware(tokens TokenHashLookup) *TokenMiddleware`
  - `func (m *TokenMiddleware) RequireToken(next http.Handler) http.Handler`

- [ ] **Step 1: Write the failing test**

Create `backend/internal/auth/token_middleware_test.go`:

```go
package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/peeq/internal/apitoken"
)

// fakeTokens is a TokenHashLookup returning a fixed hash.
type fakeTokens struct{ hash string }

func (f fakeTokens) APITokenHash(context.Context) string { return f.hash }

// okHandler records that the protected handler was reached.
func okHandler(reached *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireToken_acceptsAValidBearerToken(t *testing.T) {
	// Given
	const token = "peeq_valid-token-value"
	reached := false
	mw := NewTokenMiddleware(fakeTokens{hash: apitoken.Hash(token)})
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()

	// When
	mw.RequireToken(okHandler(&reached)).ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !reached {
		t.Fatalf("protected handler was not reached")
	}
}

func TestRequireToken_rejectsBadCredentials(t *testing.T) {
	const token = "peeq_valid-token-value"
	cases := []struct {
		name   string
		header string
		stored string
	}{
		{"wrong token", "Bearer peeq_wrong-token-value", apitoken.Hash(token)},
		{"missing header", "", apitoken.Hash(token)},
		{"empty bearer value", "Bearer ", apitoken.Hash(token)},
		{"wrong scheme", "Basic " + token, apitoken.Hash(token)},
		{"scheme only", "Bearer", apitoken.Hash(token)},
		{"no token configured", "Bearer " + token, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			reached := false
			mw := NewTokenMiddleware(fakeTokens{hash: tc.stored})
			req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()

			// When
			mw.RequireToken(okHandler(&reached)).ServeHTTP(rec, req)

			// Then
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if reached {
				t.Fatalf("protected handler was reached despite bad credentials")
			}
		})
	}
}

func TestRequireToken_doesNotPutAUserInTheContext(t *testing.T) {
	// A token request is not a session. Handlers behind RequireToken must
	// not be able to assume UserFromContext works.
	const token = "peeq_valid-token-value"
	var sawUser bool
	mw := NewTokenMiddleware(fakeTokens{hash: apitoken.Hash(token)})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	mw.RequireToken(h).ServeHTTP(httptest.NewRecorder(), req)

	if sawUser {
		t.Fatalf("a user was present in the context of a token request")
	}
}
```

Before running: confirm the accessor is named `UserFromContext` and returns `(User, bool)` — check `backend/internal/auth/middleware.go` and `model.go`. If the real name or shape differs, adapt this test to it rather than adding a new accessor.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/auth/ -run TestRequireToken -v`
Expected: FAIL — `undefined: NewTokenMiddleware`.

- [ ] **Step 3: Write the implementation**

Create `backend/internal/auth/token_middleware.go`:

```go
package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/trick77/peeq/internal/apitoken"
)

// TokenHashLookup returns the stored API token hash, or "" when no token has
// been generated. *settings.Store satisfies this.
type TokenHashLookup interface {
	APITokenHash(context.Context) string
}

// TokenMiddleware gates peeq's machine endpoints on the API token. It is
// deliberately separate from Middleware: token requests are not sessions, so
// they establish no user identity, and keeping the two apart means the OIDC
// bypass surface is a single greppable middleware in server.go.
type TokenMiddleware struct {
	tokens TokenHashLookup
}

// NewTokenMiddleware returns middleware gating routes on the API token.
func NewTokenMiddleware(tokens TokenHashLookup) *TokenMiddleware {
	return &TokenMiddleware{tokens: tokens}
}

// RequireToken rejects requests without a valid "Authorization: Bearer
// <token>" header. It puts no user in the request context — handlers behind
// it must not call UserFromContext.
func (m *TokenMiddleware) RequireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := bearerToken(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// A nil lookup or an empty stored hash both fail closed.
		stored := ""
		if m.tokens != nil {
			stored = m.tokens.APITokenHash(r.Context())
		}
		if !apitoken.Verify(presented, stored) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the credential from an Authorization header. The
// scheme match is case-insensitive per RFC 7235; the token itself is not.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/auth/ -v`
Expected: PASS — the new tests plus every existing auth test.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/auth/token_middleware.go backend/internal/auth/token_middleware_test.go
git commit -m "feat(auth): RequireToken middleware for machine endpoints"
```

---

### Task 4: Token routes + machine cookie route

**Files:**
- Create: `backend/internal/httpapi/apitoken_handlers.go`
- Create: `backend/internal/httpapi/apitoken_handlers_test.go`
- Modify: `backend/internal/httpapi/settings_handlers.go:97-131` (extract `applyCookie`)
- Modify: `backend/internal/httpapi/server.go` (Deps field, server field, `requireToken`, routes)
- Modify: `backend/internal/httpapi/settings_handlers_test.go` (machine-route tests)

**Interfaces:**
- Consumes: `settings.SetAPITokenHash` / `APITokenInfo` / `APITokenHash` (Task 1), `apitoken.Generate` / `Hash` (Task 2), `auth.NewTokenMiddleware` (Task 3).
- Produces: routes `GET /api/settings/token`, `POST /api/settings/token`, `PUT /api/machine/cookie`; `Deps.TokenMiddleware *auth.TokenMiddleware`.

- [ ] **Step 1: Write the failing test**

This uses the package's existing harness: `testDeps(t)` (`server_test.go:31`) builds `Deps` with a temp DB, dev-auth claims, and a real `settings.Store`; `loginAndGetCookie(t, h)` (`settings_handlers_test.go:20`) performs a dev login and returns the session cookie; `validYouTubeCookieBody` (`settings_handlers_test.go:14`) is a fixture that passes `cookie.Validate`. Do not invent new helpers.

First add `TokenMiddleware` to `testDeps` in `backend/internal/httpapi/server_test.go`, so every test in the package gets a wired token middleware. Replace the `return Deps{...}` with:

```go
	settingsStore := settings.New(db)
	return Deps{
		AuthService:     auth.NewService(nil, sessions, users),
		AuthMiddleware:  auth.NewMiddleware(sessions, users),
		Settings:        settingsStore,
		TokenMiddleware: auth.NewTokenMiddleware(settingsStore),
		DevAuthClaims: auth.Claims{
			Subject:           "dev-tester",
			PreferredUsername: "dev",
			Email:             "dev@example.local",
			Name:              "Dev Tester",
		},
	}
```

Then create `backend/internal/httpapi/apitoken_handlers_test.go`:

```go
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/apitoken"
)

// createToken generates a token over the API and returns its plaintext.
func createToken(t *testing.T, h http.Handler, sessionCookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/token", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/settings/token status = %d, body = %s", rec.Code, rec.Body)
	}
	var created struct {
		Token     string `json:"token"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created token: %v", err)
	}
	return created.Token
}

func TestGetAPIToken_reportsAbsentAndNeverReturnsAToken(t *testing.T) {
	// Given
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	// When: no token has been generated.
	req := httptest.NewRequest(http.MethodGet, "/api/settings/token", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body)
	}
	var status struct {
		Present   bool   `json:"present"`
		CreatedAt string `json:"created_at"`
		Token     string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if status.Present {
		t.Fatalf("present = true before any token was generated")
	}
	if status.Token != "" {
		t.Fatalf("GET returned a token field: %q", status.Token)
	}
}

func TestPostAPIToken_returnsThePlaintextExactlyOnce(t *testing.T) {
	// Given
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)

	// When
	token := createToken(t, h, sessionCookie)

	// Then: correctly shaped.
	if !strings.HasPrefix(token, apitoken.Prefix) {
		t.Fatalf("token %q lacks the peeq_ prefix", token)
	}
	if len(token) != 48 {
		t.Fatalf("len(token) = %d, want 48", len(token))
	}

	// And: a subsequent GET reports it present but never echoes it.
	getReq := httptest.NewRequest(http.MethodGet, "/api/settings/token", nil)
	getReq.AddCookie(sessionCookie)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if !strings.Contains(getRec.Body.String(), `"present":true`) {
		t.Fatalf("GET after create: present is not true (body=%s)", getRec.Body)
	}
	if strings.Contains(getRec.Body.String(), token) {
		t.Fatalf("GET echoed the token back: %s", getRec.Body)
	}
}

func TestPostAPIToken_regenerationInvalidatesThePreviousToken(t *testing.T) {
	// Given: a token exists.
	deps := testDeps(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	first := createToken(t, h, sessionCookie)

	// When: it is regenerated.
	second := createToken(t, h, sessionCookie)

	// Then: a new value is issued and the old one no longer verifies.
	if first == second {
		t.Fatalf("regeneration returned the same token %q", first)
	}
	storedHash := deps.Settings.APITokenHash(context.Background())
	if apitoken.Verify(first, storedHash) {
		t.Fatalf("the old token still verifies after regeneration")
	}
	if !apitoken.Verify(second, storedHash) {
		t.Fatalf("the new token does not verify")
	}
}

func TestGetSettings_neverCarriesTheAPIToken(t *testing.T) {
	// Regression guard for the "nothing secret in Settings" contract.
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)

	req := httptest.NewRequest(http.MethodGet, "/api/settings", nil)
	req.AddCookie(sessionCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(strings.ToLower(body), "api_token") {
		t.Fatalf("GET /api/settings carries an api_token field: %s", body)
	}
	if strings.Contains(body, token) {
		t.Fatalf("GET /api/settings carries the token: %s", body)
	}
}

func TestMachineCookie_writesTheCookieWithAValidToken(t *testing.T) {
	// Given: a generated token.
	deps := testDeps(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)

	// When: a machine pushes a cookie with a bearer token and NO session.
	body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "SID") {
		t.Fatalf("the machine route echoed the cookie back: %s", rec.Body)
	}
	if got := deps.Settings.CookieStatus(context.Background()); got != "valid" {
		t.Fatalf("cookie_status = %q, want valid", got)
	}
}

func TestMachineCookie_rejectsRequestsWithoutAValidToken(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"wrong token", "Bearer peeq_not-the-right-token-value-at-all-xx"},
		{"wrong scheme", "Basic something"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Given: a token exists, but the request does not present it.
			deps := testDeps(t)
			h := New(deps)
			sessionCookie := loginAndGetCookie(t, h)
			createToken(t, h, sessionCookie)

			// When
			body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`
			req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", strings.NewReader(body))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			// Then: rejected, and nothing was written.
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body)
			}
			if got := deps.Settings.CookieStatus(context.Background()); got == "valid" {
				t.Fatalf("an unauthorized request wrote the cookie")
			}
		})
	}
}

func TestMachineCookie_rejectsWhenNoTokenHasBeenGenerated(t *testing.T) {
	// Given: peeq is unconfigured — no token exists at all.
	deps := testDeps(t)
	h := New(deps)

	// When: a request presents some token anyway.
	body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer peeq_anything-at-all-goes-here-for-this")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: an empty stored hash never authenticates.
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (body=%s)", rec.Code, rec.Body)
	}
	if got := deps.Settings.CookieStatus(context.Background()); got == "valid" {
		t.Fatalf("an unconfigured peeq accepted a cookie write")
	}
}

func TestMachineCookie_rejectsAMalformedCookieBody(t *testing.T) {
	// Given
	h := New(testDeps(t))
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)

	// When
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie",
		strings.NewReader(`{"cookie":"this is not a netscape cookie file"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body)
	}
}
```

Note `server_test.go` will need `settings` in its import block if it is not already there.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/httpapi/ -run 'APIToken|MachineCookie' -v`
Expected: FAIL — `unknown field TokenMiddleware in struct literal Deps`.

- [ ] **Step 3: Extract the shared cookie-write body**

In `backend/internal/httpapi/settings_handlers.go`, replace the body of `handlePutSettingsCookie` so both routes share it:

```go
// handlePutSettingsCookie is the session-authenticated way the pasted cookie
// enters the system. On success it does not echo the cookie back — the
// response is the same cookie-body-free settings view as GET /api/settings.
func (s *server) handlePutSettingsCookie(w http.ResponseWriter, r *http.Request) {
	s.applyCookie(w, r)
}

// handleMachineCookie is the token-authenticated cookie-write path, used by
// the peeq browser extension. It is deliberately a separate route from
// handlePutSettingsCookie so that exactly one route in server.go bypasses
// OIDC, even though both share the write below.
func (s *server) handleMachineCookie(w http.ResponseWriter, r *http.Request) {
	s.applyCookie(w, r)
}

// applyCookie validates and stores a pasted cookie, then un-wedges the
// download worker. Shared by the session and machine routes; it must never
// echo the cookie body back.
func (s *server) applyCookie(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings are not configured")
		return
	}
	var req cookiePutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Cookie == "" {
		writeJSONError(w, http.StatusBadRequest, "cookie is required")
		return
	}
	if err := s.settings.SetCookie(r.Context(), req.Cookie, "valid"); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid cookie: "+err.Error())
		return
	}
	// A valid cookie is now stored: un-wedge the download worker if it paused
	// on a blocked/expired/absent cookie (this is the "re-paste and it
	// resumes" promise the Settings UI makes). Nil-safe: no worker wired in a
	// test/deployment without one just skips this.
	if s.worker != nil {
		s.worker.Resume()
	}
	got, err := s.settings.Get(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load settings")
		return
	}
	writeJSON(w, got)
}
```

- [ ] **Step 4: Write the token handlers**

Create `backend/internal/httpapi/apitoken_handlers.go`:

```go
package httpapi

import (
	"crypto/rand"
	"net/http"

	"github.com/trick77/peeq/internal/apitoken"
)

// apiTokenStatusResponse reports whether a machine token exists, without
// exposing anything secret. Safe to return to any authenticated session.
type apiTokenStatusResponse struct {
	CreatedAt string `json:"created_at,omitempty"`
	Present   bool   `json:"present"`
}

// apiTokenCreatedResponse carries the plaintext token. This is the only
// response in peeq that ever contains it: the token is stored as a hash, so
// it cannot be shown again afterwards.
type apiTokenCreatedResponse struct {
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}

// handleGetAPIToken reports whether a machine token has been generated, so
// the Settings UI can pick between its empty and active states.
func (s *server) handleGetAPIToken(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings are not configured")
		return
	}
	present, createdAt, err := s.settings.APITokenInfo(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load api token")
		return
	}
	writeJSON(w, apiTokenStatusResponse{Present: present, CreatedAt: createdAt})
}

// handlePostAPIToken generates a machine token, stores only its hash, and
// returns the plaintext once. It serves both first-time creation and
// regeneration: the operation is identical, and overwriting the stored hash
// is what invalidates any previous token immediately.
func (s *server) handlePostAPIToken(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings are not configured")
		return
	}
	token, err := apitoken.Generate(rand.Reader)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to generate api token")
		return
	}
	if err := s.settings.SetAPITokenHash(r.Context(), apitoken.Hash(token)); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to store api token")
		return
	}
	_, createdAt, err := s.settings.APITokenInfo(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load api token")
		return
	}
	writeJSON(w, apiTokenCreatedResponse{Token: token, CreatedAt: createdAt})
}
```

- [ ] **Step 5: Wire the routes**

In `backend/internal/httpapi/server.go`:

1. Add to the `Deps` struct, after the `AuthMiddleware` field:

```go
	// TokenMiddleware gates the machine endpoints on the API token. Optional:
	// when nil, PUT /api/machine/cookie returns 401 rather than being open.
	TokenMiddleware *auth.TokenMiddleware
```

2. Add to the `server` struct, after `authMW`:

```go
	tokenMW *auth.TokenMiddleware
```

3. Add to the `New` struct literal, after `authMW: d.AuthMiddleware,`:

```go
		tokenMW: d.TokenMiddleware,
```

4. Add a `requireToken` helper next to `requireAuth` (around line 243):

```go
// requireToken gates a machine route on the API token. Mirrors requireAuth's
// nil-safety: an unwired middleware rejects rather than opens the route.
func (s *server) requireToken(next http.Handler) http.Handler {
	if s.tokenMW == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		})
	}
	return s.tokenMW.RequireToken(next)
}
```

5. Register three routes. Put the two OIDC ones immediately after the existing `GET /api/cookie/health` line, and the machine one after them with its comment:

```go
	mux.Handle("GET /api/settings/token", s.requireAuth(http.HandlerFunc(s.handleGetAPIToken)))
	mux.Handle("POST /api/settings/token", s.requireAuth(http.HandlerFunc(s.handlePostAPIToken)))
	// The only route in peeq that bypasses OIDC. Token-gated, cookie-write
	// only — deliberately not a general machine surface.
	mux.Handle("PUT /api/machine/cookie", s.requireToken(http.HandlerFunc(s.handleMachineCookie)))
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd backend && go test ./internal/httpapi/ -v`
Expected: PASS — the new tests plus every existing httpapi test (the `applyCookie` extraction must not change `PUT /api/settings/cookie` behavior).

- [ ] **Step 7: Wire main.go and verify the whole backend**

In `backend/cmd/peeq/main.go`, find where `settingsStore` is created (~line 138) and where the `httpapi.New(httpapi.Deps{...})` literal is built. Add to that literal:

```go
		TokenMiddleware: auth.NewTokenMiddleware(settingsStore),
```

Run: `cd backend && go build ./... && go vet ./... && go test -race ./...`
Expected: build clean, vet clean, all packages PASS.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/httpapi/ backend/cmd/peeq/main.go
git commit -m "feat(api): token routes and the token-gated machine cookie endpoint"
```

---

### Task 5: UI token API client

**Files:**
- Modify: `ui/src/api/types.ts`
- Modify: `ui/src/api/settings.ts`

**Interfaces:**
- Consumes: routes from Task 4.
- Produces:
  - `type APITokenStatus = { present: boolean; created_at?: string }`
  - `type APITokenCreated = { token: string; created_at: string }`
  - `function getAPITokenStatus(): Promise<APITokenStatus>`
  - `function createAPIToken(): Promise<APITokenCreated>`

- [ ] **Step 1: Add the types**

Append to `ui/src/api/types.ts`:

```ts
// APITokenStatus is the non-secret view of the machine API token. The token
// itself is write-only: it is never returned after the response that
// creates it, so this type deliberately has no token field.
export type APITokenStatus = {
  present: boolean;
  created_at?: string;
};

// APITokenCreated is the one and only shape that carries the plaintext
// token, returned by createAPIToken. Hold it in component state only — it
// cannot be fetched again.
export type APITokenCreated = {
  token: string;
  created_at: string;
};
```

- [ ] **Step 2: Add the client functions**

Append to `ui/src/api/settings.ts`, and add `APITokenStatus` and `APITokenCreated` to the existing `import type { ... } from "./types";` line:

```ts
// getAPITokenStatus reports whether a machine token exists. It never returns
// the token — see createAPIToken for the only response that does.
export async function getAPITokenStatus(): Promise<APITokenStatus> {
  return api.get<APITokenStatus>("/api/settings/token", "failed to load api token");
}

// createAPIToken generates a token (or replaces the existing one) and
// returns the plaintext exactly once. peeq stores only a hash, so a lost
// token cannot be recovered — only replaced.
export async function createAPIToken(): Promise<APITokenCreated> {
  return api.post<APITokenCreated>("/api/settings/token", undefined, "failed to create api token");
}
```

- [ ] **Step 3: Verify it typechecks**

Run: `cd ui && npx tsc --noEmit`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add ui/src/api/types.ts ui/src/api/settings.ts
git commit -m "feat(ui): API token client"
```

---

### Task 6: Settings API token section

**Files:**
- Modify: `ui/src/views/Settings.tsx`
- Test: `ui/src/views/Settings.apitoken.test.tsx`

**Interfaces:**
- Consumes: `getAPITokenStatus`, `createAPIToken` (Task 5).
- Produces: the rendered section. No exports.

- [ ] **Step 1: Write the failing test**

Create `ui/src/views/Settings.apitoken.test.tsx`. Open the existing `ui/src/views/Settings.test.tsx` first and copy its mock setup and `baseSettings` fixture exactly — this file must mock the same module surface, plus the two new functions:

```tsx
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import Settings from "./Settings";
import { getSettings, getAPITokenStatus, createAPIToken } from "../api/settings";

// Mirror Settings.test.tsx's mock of ../api/settings, extended with the two
// token functions.
vi.mock("../api/settings", () => ({
  getSettings: vi.fn(),
  updateSettings: vi.fn(),
  putCookie: vi.fn(),
  cookieHealth: vi.fn(),
  getAPITokenStatus: vi.fn(),
  createAPIToken: vi.fn(),
}));

const baseSettings = {
  cookie_status: "valid",
  cookie_updated_at: "2026-07-18T10:00:00Z",
  format_preset: "apple-1080p",
  format_custom: "",
  limit_rate: "",
  throttle_base_seconds: 20,
  retention_days: 14,
  min_free_gb: 5,
  min_video_duration_seconds: 180,
  ytdlp_version: "2026.07.01",
  youtube_paused: false,
  youtube_pause_reason: "",
};

function tokenSection(): HTMLElement {
  return screen.getByRole("heading", { name: /API token/i }).closest("section") as HTMLElement;
}

describe("Settings — API token", () => {
  beforeEach(() => {
    vi.mocked(getSettings).mockResolvedValue(baseSettings as never);
    vi.mocked(getAPITokenStatus).mockReset();
    vi.mocked(createAPIToken).mockReset();
  });

  it("offers to generate a token when none exists", async () => {
    // Given
    vi.mocked(getAPITokenStatus).mockResolvedValue({ present: false });

    // When
    render(<Settings />);

    // Then
    const section = await waitFor(() => tokenSection());
    expect(within(section).getByRole("button", { name: /Generate token/i })).toBeInTheDocument();
  });

  it("shows the token once after generating it", async () => {
    // Given
    vi.mocked(getAPITokenStatus).mockResolvedValue({ present: false });
    vi.mocked(createAPIToken).mockResolvedValue({
      token: "peeq_7Kd2mQx9vRtY4nLpZbA6sWfE8hJcU3iO1gTaXkPqRmN",
      created_at: "2026-07-20T09:12:00Z",
    });
    const user = userEvent.setup();
    render(<Settings />);
    const section = await waitFor(() => tokenSection());

    // When
    await user.click(within(section).getByRole("button", { name: /Generate token/i }));

    // Then: the plaintext is rendered, with the copy-it-now warning.
    await waitFor(() => {
      expect(
        within(tokenSection()).getByText("peeq_7Kd2mQx9vRtY4nLpZbA6sWfE8hJcU3iO1gTaXkPqRmN"),
      ).toBeInTheDocument();
    });
    expect(within(tokenSection()).getByText(/won't be shown again/i)).toBeInTheDocument();
  });

  it("never renders a token value on a returning visit", async () => {
    // Given: a token exists but, being hashed, cannot be fetched.
    vi.mocked(getAPITokenStatus).mockResolvedValue({
      present: true,
      created_at: "2026-07-20T09:12:00Z",
    });

    // When
    render(<Settings />);

    // Then: only a regenerate affordance, no secret.
    const section = await waitFor(() => tokenSection());
    expect(within(section).getByRole("button", { name: /Generate a new token/i })).toBeInTheDocument();
    expect(section.textContent).not.toMatch(/peeq_/);
  });

  it("requires confirmation before replacing an existing token", async () => {
    // Given
    vi.mocked(getAPITokenStatus).mockResolvedValue({
      present: true,
      created_at: "2026-07-20T09:12:00Z",
    });
    vi.mocked(createAPIToken).mockResolvedValue({
      token: "peeq_NEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEWNEW",
      created_at: "2026-07-20T10:00:00Z",
    });
    const user = userEvent.setup();
    render(<Settings />);
    const section = await waitFor(() => tokenSection());

    // When: the first click only opens the confirm row.
    await user.click(within(section).getByRole("button", { name: /Generate a new token/i }));

    // Then: nothing has been created yet.
    expect(createAPIToken).not.toHaveBeenCalled();
    expect(within(tokenSection()).getByText(/stop sending cookies/i)).toBeInTheDocument();

    // When: the confirm is clicked.
    await user.click(within(tokenSection()).getByRole("button", { name: /^Generate$/ }));

    // Then
    await waitFor(() => expect(createAPIToken).toHaveBeenCalledTimes(1));
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd ui && npx vitest run src/views/Settings.apitoken.test.tsx`
Expected: FAIL — no "API token" heading is rendered.

- [ ] **Step 3: Implement the section**

In `ui/src/views/Settings.tsx`:

1. Extend the settings API import to include the two new functions:

```tsx
import { getSettings, updateSettings, putCookie, getAPITokenStatus, createAPIToken } from "../api/settings";
```

2. Add state near the other `useState` declarations (around line 44):

```tsx
  const [tokenPresent, setTokenPresent] = useState(false);
  const [tokenCreatedAt, setTokenCreatedAt] = useState("");
  // freshToken holds the plaintext returned by createAPIToken. It lives only
  // here: peeq stores a hash, so leaving this page loses it for good.
  const [freshToken, setFreshToken] = useState<string | null>(null);
  const [tokenConfirming, setTokenConfirming] = useState(false);
  const [tokenBusy, setTokenBusy] = useState(false);
  const [tokenCopied, setTokenCopied] = useState(false);
  const [tokenError, setTokenError] = useState<string | null>(null);
```

3. Load the status alongside the existing settings load. Add this `useEffect` next to the existing one:

```tsx
  useEffect(() => {
    getAPITokenStatus()
      .then((status) => {
        setTokenPresent(status.present);
        setTokenCreatedAt(status.created_at ?? "");
      })
      .catch(() => {
        // A failed status read must not blank the whole Settings page; the
        // section falls back to its empty state.
        setTokenPresent(false);
      });
  }, []);
```

4. Add the handlers next to the other `handle*` functions:

```tsx
  async function handleCreateToken() {
    setTokenBusy(true);
    setTokenError(null);
    try {
      const created = await createAPIToken();
      setFreshToken(created.token);
      setTokenPresent(true);
      setTokenCreatedAt(created.created_at);
      setTokenConfirming(false);
    } catch (err) {
      setTokenError((err as Error).message ?? "Failed to create the API token.");
    } finally {
      setTokenBusy(false);
    }
  }

  async function handleCopyToken() {
    if (!freshToken) return;
    try {
      await navigator.clipboard.writeText(freshToken);
    } catch {
      // Clipboard access can be denied; the token is selectable on screen.
    }
    setTokenCopied(true);
    setTimeout(() => setTokenCopied(false), 1600);
  }
```

5. Insert the section between the YouTube cookie `</section>` and the `Download format` `<section className="sect">` (around line 264):

```tsx
      <section className="sect">
        <h2>
          API token
          {tokenPresent ? (
            <span className="status-line">
              <span className="led" />
              Active
            </span>
          ) : (
            <span className="status-line idle">
              <span className="led" />
              Not set up
            </span>
          )}
        </h2>
        <p className="desc">
          Lets the peeq browser extension send your YouTube cookie automatically, so you never paste it
          by hand. The token can only write the cookie — it cannot read your library.
        </p>

        {freshToken ? (
          <div className="reveal">
            <div className="rhead">
              <Icon name="warning" size="15px" />
              Copy this now — it won't be shown again
            </div>
            <div className="tokenfield">
              <code>{freshToken}</code>
              <div className="acts">
                <button type="button" className={`iconbtn${tokenCopied ? " ok" : ""}`} onClick={handleCopyToken}>
                  {tokenCopied ? "Copied" : "Copy"}
                </button>
              </div>
            </div>
            <p className="rfoot">
              peeq stores only a hash of this token, so it can't show it to you again. If you lose it,
              generate a new one — the old one stops working.
            </p>
            <div className="field-row">
              <button type="button" className="btn ghost" onClick={() => setFreshToken(null)}>
                Done
              </button>
            </div>
          </div>
        ) : tokenPresent ? (
          <>
            <p className="meta">
              {tokenCreatedAt ? `Created ${new Date(tokenCreatedAt).toLocaleString()}` : "Token is set up."}
            </p>
            <div className="field-row">
              <button
                type="button"
                className="btn ghost"
                disabled={tokenConfirming || tokenBusy}
                onClick={() => setTokenConfirming(true)}
              >
                Generate a new token
              </button>
              <span className="meta">The current token stops working immediately.</span>
            </div>
            {tokenConfirming ? (
              <div className="warnline">
                <Icon name="warning" size="16px" style={{ color: "var(--color-danger)" }} />
                <span style={{ flex: 1 }}>
                  Generate a new token? Your extension will stop sending cookies until you paste the new
                  one.
                </span>
                <button type="button" className="btn danger sm" disabled={tokenBusy} onClick={handleCreateToken}>
                  Generate
                </button>
                <button type="button" className="btn ghost sm" onClick={() => setTokenConfirming(false)}>
                  Cancel
                </button>
              </div>
            ) : null}
          </>
        ) : (
          <>
            <div className="empty">No API token yet.</div>
            <div className="field-row">
              <button type="button" className="btn primary" disabled={tokenBusy} onClick={handleCreateToken}>
                {tokenBusy ? "Generating…" : "Generate token"}
              </button>
              <span className="meta">You'll see the token once, right after it's created.</span>
            </div>
          </>
        )}
        {tokenError ? <div className="errline">{tokenError}</div> : null}
      </section>
```

Check that `Icon` is already imported in `Settings.tsx` and that a `warning` icon name exists in `ui/src/icons.tsx`. If `warning` is not a valid name, use whichever name the cookie section's `<Icon name="warning" .../>` already uses — it is used there, so it exists.

- [ ] **Step 4: Add the styles**

Append to `ui/src/index.css`, after the `.status-line .led` rule:

```css
/* API token section. The reveal panel is the only place the plaintext token
   is ever rendered; it uses --color-kept (not danger red) because this is a
   "keep this" moment, not an error. */
.status-line.idle {
  color: var(--color-faint);
  background: transparent;
  border-color: var(--color-border);
}
.status-line.idle .led {
  box-shadow: none;
}
.reveal {
  margin-top: 4px;
  border: 1px solid color-mix(in srgb, var(--color-kept) 40%, transparent);
  background: color-mix(in srgb, var(--color-kept) 7%, transparent);
  border-radius: var(--radius-ui);
  padding: 16px 17px;
}
.reveal .rhead {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 12.5px;
  font-weight: 700;
  color: var(--color-kept);
  letter-spacing: 0.02em;
  margin-bottom: 11px;
}
.reveal .rfoot {
  margin: 11px 0 0;
  font-size: 12.5px;
  color: var(--color-muted);
  line-height: 1.5;
}
.tokenfield {
  display: flex;
  align-items: stretch;
  background: var(--color-bg);
  border: 1px solid var(--color-border);
  border-radius: 10px;
  overflow: hidden;
}
.tokenfield code {
  flex: 1;
  min-width: 0;
  padding: 13px 14px;
  font-family: var(--font-mono);
  font-size: 12.5px;
  line-height: 1.5;
  color: var(--color-ink-dim);
  overflow-x: auto;
  white-space: nowrap;
  letter-spacing: 0.02em;
}
.tokenfield .acts {
  display: flex;
  align-items: center;
  padding: 0 6px;
  border-left: 1px solid var(--color-border);
  background: var(--color-panel);
}
.iconbtn {
  background: transparent;
  border: none;
  color: var(--color-muted);
  display: inline-flex;
  align-items: center;
  gap: 7px;
  padding: 8px 11px;
  border-radius: 8px;
  font-size: 12.5px;
  font-weight: 600;
  font-family: inherit;
  cursor: pointer;
}
.iconbtn:hover {
  background: var(--color-active);
  color: var(--color-ink-dim);
}
.iconbtn:focus-visible {
  outline: 2px solid var(--color-accent);
  outline-offset: 1px;
}
.iconbtn.ok {
  color: var(--color-online);
}
.btn.danger {
  background: transparent;
  color: var(--color-danger);
  border: 1px solid color-mix(in srgb, var(--color-danger) 35%, transparent);
}
.btn.danger:hover:not(:disabled) {
  background: color-mix(in srgb, var(--color-danger) 10%, transparent);
}
.btn.sm {
  min-height: 38px;
  padding: 0 15px;
  font-size: 13.5px;
}
.empty {
  border: 1px dashed var(--color-border);
  border-radius: var(--radius-ui);
  padding: 20px;
  text-align: center;
  color: var(--color-faint);
  font-size: 13px;
}
```

If `.errline` or `.meta` are not already defined in `index.css`, check how the cookie section renders its error (`<div className="errline">`) — `errline` is already used there, so it exists. `.meta` is new: if it is absent, add `.meta { font-size: 12.5px; color: var(--color-faint); margin-top: 12px; }`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd ui && npx vitest run`
Expected: PASS — the four new tests plus all existing UI tests. The existing `Settings.test.tsx` must still pass; it now renders a component that calls `getAPITokenStatus`, so if that file's `vi.mock` of `../api/settings` does not include `getAPITokenStatus` and `createAPIToken`, add them there as `vi.fn()` and give `getAPITokenStatus` a default `mockResolvedValue({ present: false })` in its `beforeEach`.

- [ ] **Step 6: Verify the build, then restore the stub**

```bash
cd ui && npm run build
cd .. && git checkout -- backend/web/dist/index.html
```

Expected: build succeeds. The `git checkout` is mandatory — see the plan's Global Constraints.

- [ ] **Step 7: Commit**

```bash
git add ui/src/views/Settings.tsx ui/src/views/Settings.apitoken.test.tsx ui/src/views/Settings.test.tsx ui/src/index.css
git commit -m "feat(ui): Settings API token section with one-time reveal"
```

---

### Task 7: Backend test minors — status+category combo

**Files:**
- Modify: `backend/internal/videos/store_test.go`

**Interfaces:**
- Consumes: `videos.Store.List(filter, category)` — read its exact current signature before writing the test.
- Produces: nothing.

This closes deferred minor 1 from the categorization branch's SDD ledger.

- [ ] **Step 1: Write the test**

`List` is `func (s *Store) List(filter, category string) ([]Video, error)` (`store.go:197`) — no context parameter. The existing list test builds rows with `s.Upsert(Video{...})` plus `SetDownloaded` / `SetWatched` / `SetStatus`, and `SetCategory(id, category)` sets the category. Match that style; check how the file constructs its store (`openTestDB(t)` at `store_test.go:11`) and mirror the neighboring test's opening lines.

Append to `backend/internal/videos/store_test.go`:

```go
func TestList_statusAndCategoryAreAnded(t *testing.T) {
	s := New(openTestDB(t))

	// Given: four videos spanning both axes — watched × category.
	seed := []struct {
		id       string
		watched  bool
		category string
	}{
		{"a", false, "ai"},
		{"b", false, "gaming"},
		{"c", true, "ai"},
		{"d", true, "gaming"},
	}
	for _, v := range seed {
		if err := s.Upsert(Video{ID: v.id, URL: "u", DurationSeconds: 100}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetDownloaded(v.id, DownloadedResult{MediaPath: "/m/" + v.id + ".mp4"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetCategory(v.id, v.category); err != nil {
			t.Fatal(err)
		}
		if v.watched {
			if err := s.SetWatched(v.id, true); err != nil {
				t.Fatal(err)
			}
		}
	}

	// When: both filters are applied together.
	got, err := s.List("unwatched", "ai")
	if err != nil {
		t.Fatalf("list unwatched+ai: %v", err)
	}

	// Then: only the row matching BOTH comes back — not the union of each.
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("list unwatched+ai = %+v, want [a]", got)
	}

	// And: each filter alone still returns its own two rows, proving the
	// combination narrowed the result rather than one filter winning.
	unwatched, err := s.List("unwatched", "")
	if err != nil {
		t.Fatalf("list unwatched: %v", err)
	}
	if len(unwatched) != 2 {
		t.Fatalf("list unwatched = %d rows, want 2", len(unwatched))
	}
	ai, err := s.List("all", "ai")
	if err != nil {
		t.Fatalf("list ai: %v", err)
	}
	if len(ai) != 2 {
		t.Fatalf("list ai = %d rows, want 2", len(ai))
	}
}
```

If the store constructor is not `New(openTestDB(t))`, copy the exact construction from the neighboring list test.

- [ ] **Step 2: Run the test**

Run: `cd backend && go test ./internal/videos/ -run TestList_statusAndCategory -v`
Expected: PASS immediately — this is coverage for behavior that already works, not a bug fix. If it FAILS, stop: you have found a real defect in `List`. Report it rather than editing the test to match.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/videos/store_test.go
git commit -m "test(videos): status and category filters are ANDed"
```

---

### Task 8: UI test minors — enum drift, poller category, unknown category

**Files:**
- Create: `ui/src/enumsync.test.ts`
- Modify: `ui/src/views/Library.test.tsx`

**Interfaces:**
- Consumes: `CATEGORIES` from `ui/src/categories.ts`; reads `backend/internal/videos/category.go` from disk.
- Produces: nothing.

This closes deferred minors 2, 3, and 4. Minor 2 is the one with real failure potential: today a label rename in the Go enum fails no test.

- [ ] **Step 1: Write the enum drift test**

Create `ui/src/enumsync.test.ts`:

```ts
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { CATEGORIES } from "./categories";

// The Go enum in backend/internal/videos/category.go is the authority;
// categories.ts mirrors it. Until this test existed, a label rename on
// either side drifted silently — the id/count checks could not see it.
const GO_CATEGORY_FILE = fileURLToPath(
  new URL("../../backend/internal/videos/category.go", import.meta.url),
);

function goCategories(): Array<{ id: string; label: string }> {
  const source = readFileSync(GO_CATEGORY_FILE, "utf8");
  const block = source.match(/var Categories = \[\]Category\{([\s\S]*?)\n\}/);
  if (!block) {
    throw new Error("could not find the Categories block in category.go");
  }
  return [...block[1].matchAll(/\{"([^"]+)",\s*"([^"]+)"\}/g)].map((m) => ({
    id: m[1],
    label: m[2],
  }));
}

describe("category enum sync", () => {
  it("matches the Go enum entry for entry, in order", () => {
    // Given
    const go = goCategories();

    // Then: ids, labels, and order must all agree.
    expect(go.length).toBeGreaterThan(0);
    expect(CATEGORIES.map((c) => ({ id: c.id, label: c.label }))).toEqual(go);
  });
});
```

- [ ] **Step 2: Run it to verify it passes, then verify it actually detects drift**

Run: `cd ui && npx vitest run src/enumsync.test.ts`
Expected: PASS.

Now prove the guard works. Temporarily change one label in `ui/src/categories.ts` (e.g. `"Gaming"` → `"Games"`), re-run the same command, and confirm it FAILS. Then revert the edit and confirm it passes again. A guard that cannot fail is worse than no guard.

- [ ] **Step 3: Write the poller and unknown-category tests**

`ui/src/views/Library.test.tsx` already has two fixtures: `baseVideo(overrides)` for the `VideoCard lifecycle line` describe block, and `categoryVideo(overrides)` for the `Library category chips` block. `Library` is rendered as `<Library onOpenVideo={() => {}} />`, and `Library.tsx:163` polls every 3000ms calling `listVideos(filter, category)`.

Add the first test inside the existing `describe("Library category chips", ...)` block:

```tsx
  it("keeps the selected category when the 3s poller refreshes", async () => {
    // Given: a category filter is active.
    const aiVideo = categoryVideo({ id: "v1", title: "ai video title", category: "ai" });
    vi.mocked(listVideos).mockImplementation(async (_filter, category) => {
      if (category === "ai") return [aiVideo];
      return [aiVideo];
    });
    vi.useFakeTimers({ shouldAdvanceTime: true });

    render(<Library onOpenVideo={() => {}} />);
    const aiChip = await screen.findByRole("button", { name: /AI/ });
    fireEvent.click(aiChip);
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith("all", "ai");
    });
    vi.mocked(listVideos).mockClear();

    // When: the 3s poller fires.
    await vi.advanceTimersByTimeAsync(3000);

    // Then: the refresh still carries the category, not just the status.
    await waitFor(() => {
      expect(listVideos).toHaveBeenCalledWith("all", "ai");
    });
    vi.useRealTimers();
  });
```

If the initial call is not `("all", "ai")`, read the default filter from `Library.tsx` and use the real value rather than loosening the assertion.

Add the second test inside the existing `describe("VideoCard lifecycle line", ...)` block, since the category pill is rendered by `VideoCard`:

```tsx
  it("renders no category pill for an unknown category id", () => {
    // Given: a video carrying a category the UI has never heard of — e.g.
    // written by a newer backend enum than this build's mirror.
    render(
      <VideoCard
        video={baseVideo({ category: "not-a-real-category" })}
        retentionDays={14}
        onOpen={noop}
        onToggleFavorite={noop}
        onToggleWatched={noop}
      />,
    );

    // Then: the card still renders, and the raw id never leaks into the UI.
    expect(screen.getByText("A Test Video")).toBeInTheDocument();
    expect(screen.queryByText("not-a-real-category")).not.toBeInTheDocument();
  });
```

If `VideoCard` takes additional required props, copy the exact prop set from the neighboring test in that block.

- [ ] **Step 4: Run the full UI suite**

Run: `cd ui && npx vitest run`
Expected: PASS — everything, including all pre-existing tests.

- [ ] **Step 5: Commit**

```bash
git add ui/src/enumsync.test.ts ui/src/views/Library.test.tsx
git commit -m "test(ui): enum drift guard, poller category, unknown category"
```

---

### Task 9: Whole-branch verification

**Files:** none modified unless a check fails.

- [ ] **Step 1: Backend**

```bash
cd backend && go build ./... && go vet ./... && go test -race ./...
```
Expected: build clean, vet clean, every package PASS.

- [ ] **Step 2: UI**

```bash
cd ui && npx tsc --noEmit && npx vitest run && npm run build
cd .. && git checkout -- backend/web/dist/index.html
```
Expected: no type errors, all tests PASS, build succeeds, tree clean afterwards.

- [ ] **Step 3: Confirm the tree is clean**

```bash
git status --porcelain
```
Expected: empty output. If `backend/web/dist/index.html` appears, run the `git checkout` above.

- [ ] **Step 4: Confirm exactly one route bypasses OIDC**

```bash
grep -n "requireToken" backend/internal/httpapi/server.go
```
Expected: exactly two lines — the `requireToken` helper definition and the single `PUT /api/machine/cookie` registration. More than one registration means the least-privilege boundary was widened; stop and report.

- [ ] **Step 5: Confirm the token never leaks into the settings payload**

```bash
grep -rn "api_token" backend/internal/settings/store.go
```
Expected: hits only inside `SetAPITokenHash`, `APITokenHash`, and `APITokenInfo` — never inside `Get`'s SELECT and never in the `Settings` struct.
