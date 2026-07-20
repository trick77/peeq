# peeq Backend Logging Overhaul — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make peeq's backend failures visible in the logs — starting with the OIDC callback error that is currently discarded — and bring the logging setup in line with the sibling `loom` backend.

**Architecture:** Mirror loom's proven shape: an explicitly-constructed `slog` text handler on stderr (so every line carries an RFC3339 timestamp and `BACKEND_LOG_LEVEL` actually works), a `serverError` helper that logs the internal error and returns a generic message to the client, and `logging` + `recovery` middleware wrapping the mux. A small redaction helper strips query strings from errors before they are logged, because the OIDC callback URL carries the auth `code` and `state`.

**Tech Stack:** Go, stdlib `log/slog`, `net/http` (stdlib mux), stdlib `testing`.

## Global Constraints

- **Error attribute key is `"err"`, never `"error"`.** This is peeq's existing convention across ~40 call sites — hold it. (`"error"` appears only as a JSON body key and as a DB status enum value; do not touch those.)
- **Message strings are short and lowercase**, with variables in structured attrs. No `fmt.Sprintf` into log messages.
- **Correlation attrs are `snake_case`**: `job_id`, `video_id`, `channel_id`, `method`, `path`, `status`, `dur`.
- **Never log a full URL, `r.URL.RequestURI()`, `r.URL.RawQuery`, or `r.URL.String()`.** The OIDC callback URL contains the live auth `code` and `state`. Log `r.URL.Path` only.
- **Never widen what reaches the client.** All new detail is server-side; client-facing messages and the `?auth_error=oidc_callback_failed` code stay byte-for-byte identical.
- Tests use stdlib `testing` with Given/When/Then comments, `t.Fatalf` on failure, and `New(testDeps(t))` to build the handler — match `backend/internal/httpapi/apitoken_handlers_test.go`.
- Run `make test` (which is `go test ./...`) from the repo root.

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `backend/internal/httpapi/log.go` *(new)* | `serverError` helper + `redactErr` redaction helper | 1, 3 |
| `backend/internal/httpapi/log_test.go` *(new)* | Tests for both helpers | 1, 3 |
| `backend/internal/httpapi/auth_handlers.go` | Log the swallowed OIDC callback error | 1 |
| `backend/internal/httpapi/auth_handlers_test.go` *(new)* | Assert the callback failure is logged | 1 |
| `backend/cmd/peeq/main.go` | Construct the slog handler; wire `BACKEND_LOG_LEVEL` | 2 |
| `backend/cmd/peeq/main_test.go` | Test `parseLogLevel` | 2 |
| `backend/internal/config/config.go` | Remove the dead `LogLevel` field | 2 |
| `backend/internal/httpapi/*_handlers.go` | Convert 500 paths to `serverError` | 3 |
| `backend/internal/httpapi/middleware.go` *(new)* | `logging` + `recovery` middleware | 4 |
| `backend/internal/httpapi/middleware_test.go` *(new)* | Middleware tests | 4 |
| `backend/internal/httpapi/server.go:239` | Wrap the returned mux | 4 |
| `AGENTS.md` | Document the logging conventions | 4 |

**Task order matters.** Task 1 delivers the fix that motivated this work and creates `log.go`, which Task 3 extends. Task 2 is independent. Task 4 depends on nothing but is riskiest, so it lands last.

---

### Task 1: Log the OIDC callback failure (+ redaction helper)

The bug that started this: a bad Authentik client secret produced `?auth_error=oidc_callback_failed` and **zero** server-side output. `HandleCallback` has five distinct failure modes (`backend/internal/auth/oidc.go:96-115`) that are indistinguishable from the outside.

**Files:**
- Create: `backend/internal/httpapi/log.go`
- Create: `backend/internal/httpapi/log_test.go`
- Create: `backend/internal/httpapi/auth_handlers_test.go`
- Modify: `backend/internal/httpapi/auth_handlers.go:33-40`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `func redactErr(err error) error` — returns `err` with any `*url.Error`'s query string and userinfo stripped; non-`*url.Error` values pass through unchanged. Used by Task 3.
  - `backend/internal/httpapi/log.go` as the home for `serverError` in Task 3.

- [ ] **Step 1: Write the failing test for `redactErr`**

Create `backend/internal/httpapi/log_test.go`:

```go
package httpapi

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestRedactErr_stripsQueryAndUserinfoFromURLError(t *testing.T) {
	// Given: a url.Error whose URL carries an OIDC code in the query string.
	given := &url.Error{
		Op:  "Post",
		URL: "https://auth.example.com/token?code=SECRET123&state=abc",
		Err: errors.New("connection refused"),
	}

	// When
	got := redactErr(given).Error()

	// Then
	if strings.Contains(got, "SECRET123") {
		t.Fatalf("redactErr() leaked the code: %s", got)
	}
	if !strings.Contains(got, "auth.example.com/token") {
		t.Fatalf("redactErr() dropped the useful part: %s", got)
	}
}

func TestRedactErr_stripsUserinfo(t *testing.T) {
	// Given
	given := &url.Error{Op: "Get", URL: "https://user:pw@example.com/x", Err: errors.New("boom")}

	// When
	got := redactErr(given).Error()

	// Then
	if strings.Contains(got, "pw") {
		t.Fatalf("redactErr() leaked userinfo: %s", got)
	}
}

func TestRedactErr_passesThroughPlainErrors(t *testing.T) {
	// Given
	given := errors.New("plain failure")

	// When
	got := redactErr(given)

	// Then
	if got.Error() != "plain failure" {
		t.Fatalf("redactErr() = %q, want %q", got.Error(), "plain failure")
	}
}

func TestRedactErr_nilIsNil(t *testing.T) {
	if redactErr(nil) != nil {
		t.Fatal("redactErr(nil) should be nil")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestRedactErr -v` (from `backend/`)
Expected: FAIL — `undefined: redactErr`

- [ ] **Step 3: Implement `redactErr`**

> **⚠️ CORRECTED 2026-07-20 after Task 1 review — the code block below is WRONG. Do not use it.**
>
> Mutating the `*url.Error` found via `errors.As` does nothing when the error has been wrapped, which is the normal case here: `HandleCallback` wraps with `fmt.Errorf("...: %w", err)` at `backend/internal/auth/oidc.go:106` and `:110`, and `fmt.Errorf` renders its message **eagerly** and freezes it. Mutating the inner struct afterward cannot change what `.Error()` returns, so redaction silently no-ops on exactly the two failure modes that can carry a secret.
>
> This was inherited from loom's `scrubURLError`, which is sound only because loom scrubs at the boundary *before* wrapping.
>
> **The correct implementation scrubs the rendered message** — immune to wrapping depth:
> ```go
> func redactErr(err error) error {
>     if err == nil { return nil }
>     msg := queryStringRe.ReplaceAllString(err.Error(), "$1[redacted]")
>     if msg == err.Error() { return err }   // nothing redacted: keep the full chain
>     return errors.New(msg)
> }
> ```
> Strip the whole query string, not an allowlist of parameter names. Keep the URL path visible. Returning `errors.New` breaks the `errors.Is`/`errors.As` chain — acceptable **only** because the output is used solely as a log attribute, never for control flow; say so in the doc comment.

Create `backend/internal/httpapi/log.go`:

```go
package httpapi

import (
	"errors"
	"net/url"
)

// redactErr strips credentials from *url.Error values before they reach the
// logs. This matters most on the OIDC path: the callback URL carries a live
// auth code and state, and transport errors embed the URL verbatim. Errors
// that aren't *url.Error pass through untouched.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return err
	}
	if u, parseErr := url.Parse(urlErr.URL); parseErr == nil && u.Host != "" {
		u.RawQuery = ""
		u.User = nil
		urlErr.URL = u.String()
	}
	return err
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/httpapi/ -run TestRedactErr -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Write the failing test for callback logging**

Create `backend/internal/httpapi/auth_handlers_test.go`. This captures the log output by swapping the default logger, then asserts both that the failure is logged and that the client-facing redirect is unchanged.

```go
package httpapi

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureLogs redirects slog's default logger into a buffer for the duration
// of the test and restores it afterwards.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func TestAuthCallback_logsTheFailureAndStillRedirectsGenerically(t *testing.T) {
	// Given: a handler with OIDC configured, and a callback with no state cookie
	// (failure mode 1 of HandleCallback).
	logs := captureLogs(t)
	h := New(testDepsWithOIDC(t))

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=abc&state=xyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: the client still gets the generic code, unchanged.
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if got := rec.Header().Get("Location"); got != "/?auth_error=oidc_callback_failed" {
		t.Fatalf("Location = %q, want the unchanged generic redirect", got)
	}

	// Then: but the operator gets the reason.
	out := logs.String()
	if !strings.Contains(out, "oidc callback failed") {
		t.Fatalf("callback failure was not logged; got: %s", out)
	}
	if !strings.Contains(out, "err=") {
		t.Fatalf("log line carried no err attr; got: %s", out)
	}
}

func TestAuthCallback_neverLogsTheAuthCode(t *testing.T) {
	// Given
	logs := captureLogs(t)
	h := New(testDepsWithOIDC(t))

	// When: the callback URL carries a code that must never be logged.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=SUPERSECRETCODE&state=xyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then
	if strings.Contains(logs.String(), "SUPERSECRETCODE") {
		t.Fatalf("the auth code leaked into the logs: %s", logs.String())
	}
}
```

**`testDepsWithOIDC` does not exist yet — add it.** The existing `testDeps(t)` (`backend/internal/httpapi/server_test.go:31`) builds `auth.NewService(nil, sessions, users)` with a **nil** OIDC service, so `OIDCConfigured()` returns false and the callback returns 503 instead of the redirect — the test above would pass for the wrong reason.

The seam is fully exported (`auth.OIDCBackend` at `oidc.go:26`, `auth.NewOIDCService` at `oidc.go:56`, whose doc says "typically a fake in tests"), so build the stub locally in `httpapi` rather than reaching into `auth`'s unexported `fakeOIDCBackend`, which lives in a `_test.go` of another package and is not importable.

Add to `backend/internal/httpapi/server_test.go`:

```go
// stubOIDCBackend satisfies auth.OIDCBackend. The callback tests fail at the
// state-cookie check, before Exchange is ever reached, so these methods only
// need to exist.
type stubOIDCBackend struct{}

func (stubOIDCBackend) AuthCodeURL(state string, _ ...oauth2.AuthCodeOption) string {
	return "https://idp.example.com/authorize?state=" + state
}

func (stubOIDCBackend) Exchange(context.Context, string) (*oauth2.Token, error) {
	return nil, errors.New("stub: exchange not expected in this test")
}

func (stubOIDCBackend) VerifyClaims(context.Context, *oauth2.Token) (auth.VerifiedClaims, error) {
	return auth.VerifiedClaims{}, errors.New("stub: verify not expected in this test")
}

// testDepsWithOIDC is testDeps with a real (stub-backed) OIDC service, so the
// callback handler takes the redirect path rather than the 503 "not
// configured" path.
func testDepsWithOIDC(t *testing.T) Deps {
	t.Helper()
	d := testDeps(t)
	oidcSvc := auth.NewOIDCService(auth.OIDCServiceConfig{
		Issuer:       "https://idp.example.com",
		ClientID:     "peeq-test",
		ClientSecret: "test-secret",
		RedirectURL:  "https://peeq.example.com/api/auth/callback",
		Backend:      stubOIDCBackend{},
		SecureCookie: false,
	})
	d.AuthService = auth.NewService(oidcSvc, /* sessions, users as testDeps built them */)
	return d
}
```

**Implementer note:** `testDeps` constructs `sessions` and `users` as locals, so they aren't reachable for the `auth.NewService` call above. Either have `testDepsWithOIDC` build the whole `Deps` itself (copying the ~8 lines from `testDeps`), or refactor `testDeps` to return the stores alongside `Deps`. Prefer the small refactor over the copy. Imports needed: `context`, `errors`, `golang.org/x/oauth2`.

- [ ] **Step 6: Run the test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestAuthCallback -v`
Expected: FAIL — the assertion on `"oidc callback failed"` fails because nothing is logged.

- [ ] **Step 7: Log the error in the callback handler**

In `backend/internal/httpapi/auth_handlers.go`, add `"log/slog"` to the imports and change lines 33-40 from:

```go
	claims, err := s.authSvc.HandleCallback(r)
	s.authSvc.ClearOIDCCookies(w)
	if err != nil {
		http.Redirect(w, r, "/?auth_error=oidc_callback_failed", http.StatusFound)
		return
	}
```

to:

```go
	claims, err := s.authSvc.HandleCallback(r)
	s.authSvc.ClearOIDCCookies(w)
	if err != nil {
		// The browser only ever sees the generic code — the five distinct
		// failure modes in HandleCallback (bad state, bad nonce, code
		// exchange rejected, token verification failed) are otherwise
		// indistinguishable from the outside, which makes a misconfigured
		// provider undebuggable.
		slog.Warn("oidc callback failed", "err", redactErr(err))
		http.Redirect(w, r, "/?auth_error=oidc_callback_failed", http.StatusFound)
		return
	}
```

Level is Warn, not Error: a user abandoning the consent screen also lands here, so this is not necessarily a server fault.

- [ ] **Step 8: Run the tests to verify they pass**

Run: `go test ./internal/httpapi/ -v`
Expected: PASS, including both new `TestAuthCallback` tests and the pre-existing suite.

- [ ] **Step 9: Commit**

```bash
git add backend/internal/httpapi/log.go backend/internal/httpapi/log_test.go \
        backend/internal/httpapi/auth_handlers.go backend/internal/httpapi/auth_handlers_test.go \
        backend/internal/httpapi/server_test.go
git commit -m "fix(auth): log the OIDC callback failure instead of discarding it"
```

---

### Task 2: Make `BACKEND_LOG_LEVEL` real, with timestamps

`BACKEND_LOG_LEVEL` is currently dead config — verified: it appears only at `backend/internal/config/config.go:32` and `:91`, and is never read. There is no `slog.SetDefault` or `slog.New` anywhere in the backend, so peeq runs on Go's bare default handler. The knob advertised in `compose.yaml` does nothing.

**Files:**
- Modify: `backend/cmd/peeq/main.go:46-52`
- Modify: `backend/cmd/peeq/main_test.go`
- Modify: `backend/internal/config/config.go:32,91`

**Interfaces:**
- Consumes: nothing.
- Produces: `func parseLogLevel(raw string) slog.Level` and `func envDefault(key, def string) string` in `package main`.

**⚠️ Flagging a deletion, per the repo's change rules:** this removes the `LogLevel` field from `config.Config`. The reason is that the handler must be installed as the *first* statement in `main()` — before `config.Load()` runs — so that the existing `slog.Info("starting peeq", ...)` at `main.go:47` and any config-load failure are themselves timestamped and level-filtered. Reading the env twice would leave two sources of truth for one knob. If you'd rather keep the field, stop and raise it before deleting.

- [ ] **Step 1: Write the failing test**

Add to `backend/cmd/peeq/main_test.go`:

```go
func TestParseLogLevel_mapsNamesAndDefaultsToInfo(t *testing.T) {
	// Given / When / Then
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		" info ":  slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"":        slog.LevelInfo,
		"nonsense": slog.LevelInfo,
	}
	for raw, want := range cases {
		if got := parseLogLevel(raw); got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", raw, got, want)
		}
	}
}
```

Add `"log/slog"` and `"strings"` to that file's imports if absent.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/peeq/ -run TestParseLogLevel -v`
Expected: FAIL — `undefined: parseLogLevel`

- [ ] **Step 3: Install the handler and add the helpers**

In `backend/cmd/peeq/main.go`, replace lines 46-52:

```go
func main() {
	slog.Info("starting peeq", "version", version.Version)
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}
```

with:

```go
func main() {
	// Configure structured logging with an explicit handler so every line
	// carries an RFC3339 timestamp (the package default does not guarantee
	// one). Installed before anything else so the startup banner and any
	// config-load failure are timestamped too. Level: BACKEND_LOG_LEVEL.
	logLevel := parseLogLevel(envDefault("BACKEND_LOG_LEVEL", "info"))
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))
	slog.Info("starting peeq", "version", version.Version)
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func envDefault(key, def string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return def
}

// parseLogLevel maps a BACKEND_LOG_LEVEL string to a slog.Level, defaulting to
// Info for empty or unrecognized values.
func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
```

Add `"strings"` to the import block if it isn't already there.

- [ ] **Step 4: Remove the now-duplicated config field**

In `backend/internal/config/config.go`, delete line 32 (`LogLevel      string`) and line 91 (`LogLevel:      env("BACKEND_LOG_LEVEL", "info"),`).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `make test` (from repo root)
Expected: PASS across all packages. If a config test asserts on `LogLevel`, update it to drop the field.

- [ ] **Step 6: Verify the level knob actually works end to end**

```bash
cd backend && BACKEND_LOG_LEVEL=error go run ./cmd/peeq 2>&1 | head -5
```
Expected: the `starting peeq` INFO line is **suppressed** (it was printed before this change). Then:
```bash
cd backend && BACKEND_LOG_LEVEL=info go run ./cmd/peeq 2>&1 | head -5
```
Expected: `time=2026-07-20T..." level=INFO msg="starting peeq" version=...` — note the `time=` prefix, which is the timestamp that was previously absent.

(Both will fail to fully boot without a config/DB — that's fine, you're only checking the first lines.)

- [ ] **Step 7: Commit**

```bash
git add backend/cmd/peeq/main.go backend/cmd/peeq/main_test.go backend/internal/config/config.go
git commit -m "fix(logging): wire BACKEND_LOG_LEVEL and add timestamps to every line"
```

---

### Task 3: `serverError` helper for the silent 500s

Every 500 the API returns is currently server-side silent. `writeJSONError` (`backend/internal/httpapi/auth_handlers.go:89-93`) takes a `string`, so the `error` value is structurally incapable of reaching a log — the typical shape captures `err` and drops it:

```go
	all, err := s.videos.List(...)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list videos failed")   // err discarded
		return
	}
```

**Files:**
- Modify: `backend/internal/httpapi/log.go`
- Modify: `backend/internal/httpapi/log_test.go`
- Modify: every `*_handlers.go` in `backend/internal/httpapi` with a `StatusInternalServerError` site

**Interfaces:**
- Consumes: `redactErr` from Task 1.
- Produces: `func serverError(w http.ResponseWriter, r *http.Request, err error, clientMessage string)` — logs at Error with `method`, `path`, `client_message`, `err`; writes `writeJSONError(w, 500, clientMessage)`.

- [ ] **Step 1: Write the failing test**

Append to `backend/internal/httpapi/log_test.go` (it will need `net/http`, `net/http/httptest`, and the `captureLogs` helper from Task 1's `auth_handlers_test.go` — same package, so it's directly callable):

```go
func TestServerError_logsTheCauseAndReturnsTheGenericMessage(t *testing.T) {
	// Given
	logs := captureLogs(t)
	req := httptest.NewRequest(http.MethodGet, "/api/videos?filter=secretvalue", nil)
	rec := httptest.NewRecorder()

	// When
	serverError(rec, req, errors.New("sqlite: disk I/O error"), "list videos failed")

	// Then: the client sees only the generic message.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, "sqlite") {
		t.Fatalf("internal detail leaked to the client: %s", body)
	}
	if body := rec.Body.String(); !strings.Contains(body, "list videos failed") {
		t.Fatalf("client message missing: %s", body)
	}

	// Then: the operator sees the cause.
	out := logs.String()
	if !strings.Contains(out, "sqlite: disk I/O error") {
		t.Fatalf("cause not logged: %s", out)
	}
	if !strings.Contains(out, "/api/videos") {
		t.Fatalf("path not logged: %s", out)
	}
	// Then: but never the query string.
	if strings.Contains(out, "secretvalue") {
		t.Fatalf("query string leaked into the log: %s", out)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/httpapi/ -run TestServerError -v`
Expected: FAIL — `undefined: serverError`

- [ ] **Step 3: Implement `serverError`**

Append to `backend/internal/httpapi/log.go` (add `"log/slog"` and `"net/http"` to its imports):

```go
// serverError logs the underlying cause of a 5xx with request context and
// returns a generic JSON error to the client, so internal details never leak
// to the browser. Every 500 path should go through here so failures are never
// silent. Only r.URL.Path is logged — never the query string, which on the
// OIDC callback carries a live auth code.
func serverError(w http.ResponseWriter, r *http.Request, err error, clientMessage string) {
	slog.Error("request failed",
		"method", r.Method,
		"path", r.URL.Path,
		"client_message", clientMessage,
		"err", redactErr(err),
	)
	writeJSONError(w, http.StatusInternalServerError, clientMessage)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/httpapi/ -run TestServerError -v`
Expected: PASS

- [ ] **Step 5: Convert the call sites**

Find them:
```bash
cd backend && grep -rn "StatusInternalServerError" internal/httpapi/*.go | grep -v _test
```

For each site where an `err` variable is in scope, rewrite:
```go
		writeJSONError(w, http.StatusInternalServerError, "list videos failed")
```
to:
```go
		serverError(w, r, err, "list videos failed")
```

Keep the `clientMessage` string **byte-for-byte identical** — the frontend may match on these, and changing them is out of scope.

Leave alone: sites with no `err` in scope (pass `nil` only if the message alone is genuinely the whole story), and every non-500 status. `writeJSONError` stays as-is for 4xx — deliberately, matching loom: 4xx is client error and the request middleware from Task 4 will record it at WARN.

Known files with sites: `videos_handlers.go` (lines ~133, 168, 195, 220, 249, 289, 324), `settings_handlers.go`, `channels_handlers.go`, `downloads_handlers.go`, `subtitles_handlers.go`, `ytdlp_handlers.go`, `apitoken_handlers.go`, and `auth_handlers.go:51` (`"session create failed"`). Work file by file; do not trust this list to be exhaustive — the grep is the source of truth.

- [ ] **Step 6: Also fix the swallowed logout revocation**

`backend/internal/httpapi/auth_handlers.go:63-65` discards a revoke failure via `_ =`, telling the user they logged out while the session may still be live in the DB. Change:

```go
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		_ = s.authSvc.Revoke(r.Context(), cookie.Value)
	}
```
to:
```go
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		if err := s.authSvc.Revoke(r.Context(), cookie.Value); err != nil {
			// The cookie is cleared regardless, but a session left live in
			// the DB is security-relevant — don't let it vanish silently.
			slog.Error("session revoke failed", "err", err)
		}
	}
```

- [ ] **Step 7: Run the full suite**

Run: `make test`
Expected: PASS. Then `cd backend && go vet ./... && gofmt -l .` — expected: no output from either.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/httpapi/
git commit -m "feat(logging): add serverError helper so 500s are no longer silent"
```

---

### Task 4: Request logging + panic recovery middleware

peeq has no access log and no HTTP panic recovery — handler panics bypass slog entirely and land on stderr in Go's default `log` format. This is a direct port of `loom/backend/internal/httpapi/middleware.go`, with peeq's health path substituted.

**Files:**
- Create: `backend/internal/httpapi/middleware.go`
- Create: `backend/internal/httpapi/middleware_test.go`
- Modify: `backend/internal/httpapi/server.go:239`
- Modify: `AGENTS.md`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `func logging(next http.Handler) http.Handler`, `func recovery(next http.Handler) http.Handler`.

- [ ] **Step 1: Write the failing tests**

Create `backend/internal/httpapi/middleware_test.go`:

```go
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLogging_recordsMethodPathStatusAndDuration(t *testing.T) {
	// Given
	logs := captureLogs(t)
	h := logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	// When
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/videos", nil))

	// Then
	out := logs.String()
	for _, want := range []string{"msg=request", "method=GET", "path=/api/videos", "status=418", "dur="} {
		if !strings.Contains(out, want) {
			t.Fatalf("log line missing %q; got: %s", want, out)
		}
	}
}

func TestLogging_neverRecordsTheQueryString(t *testing.T) {
	// Given: the OIDC callback, whose query carries a live auth code.
	logs := captureLogs(t)
	h := logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	// When
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=SUPERSECRETCODE&state=xyz", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	// Then
	if strings.Contains(logs.String(), "SUPERSECRETCODE") {
		t.Fatalf("auth code leaked into the request log: %s", logs.String())
	}
}

func TestLogging_levelReflectsOutcome(t *testing.T) {
	// Given / When / Then
	cases := map[int]string{200: "level=INFO", 404: "level=WARN", 500: "level=ERROR"}
	for status, wantLevel := range cases {
		logs := captureLogs(t)
		h := logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
		if !strings.Contains(logs.String(), wantLevel) {
			t.Errorf("status %d logged at wrong level; want %s, got: %s", status, wantLevel, logs.String())
		}
	}
}

func TestLogging_skipsHealthz(t *testing.T) {
	// Given
	logs := captureLogs(t)
	h := logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	// When
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))

	// Then: the 60s healthcheck must not flood the log.
	if strings.Contains(logs.String(), "msg=request") {
		t.Fatalf("/healthz should not be logged; got: %s", logs.String())
	}
}

func TestRecovery_turnsPanicsInto500AndLogsThem(t *testing.T) {
	// Given
	logs := captureLogs(t)
	h := recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	rec := httptest.NewRecorder()

	// When
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/videos", nil))

	// Then
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	out := logs.String()
	if !strings.Contains(out, "panic recovered") || !strings.Contains(out, "boom") {
		t.Fatalf("panic not logged: %s", out)
	}
	if !strings.Contains(out, "stack=") {
		t.Fatalf("no stack trace captured: %s", out)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/httpapi/ -run "TestLogging|TestRecovery" -v`
Expected: FAIL — `undefined: logging`, `undefined: recovery`

- [ ] **Step 3: Implement the middleware**

Create `backend/internal/httpapi/middleware.go`:

```go
package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"
)

// recovery converts panics in downstream handlers into 500 responses. Without
// it, a handler panic bypasses slog entirely and lands on stderr in Go's
// default log format.
func recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered", "err", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder wraps http.ResponseWriter to capture the response status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(code int) {
	rec.status = code
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) Write(b []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.ResponseWriter.Write(b)
}

// Flush forwards flushes so the SSE handlers (/api/downloads/stream) keep
// working through the wrapper.
func (rec *statusRecorder) Flush() {
	if f, ok := rec.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying writer for http.ResponseController, which the
// video range-streaming handler relies on.
func (rec *statusRecorder) Unwrap() http.ResponseWriter {
	return rec.ResponseWriter
}

// logging logs each request with method, path, status, and duration.
// Deliberately logs r.URL.Path and never the query string: the OIDC callback
// URL carries a live auth code and state.
func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		// Level reflects the outcome so failures stand out instead of drowning in
		// the INFO request stream: 5xx -> ERROR, 4xx -> WARN, otherwise INFO.
		level := slog.LevelInfo
		switch {
		case rec.status >= 500:
			level = slog.LevelError
		case rec.status >= 400:
			level = slog.LevelWarn
		}
		slog.LogAttrs(r.Context(), level, "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.String("dur", time.Since(start).String()),
		)
	})
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/httpapi/ -run "TestLogging|TestRecovery" -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Wrap the mux**

`New` already returns `http.Handler` (`backend/internal/httpapi/server.go:160`), so this is a one-line change at line 239:

```go
	return mux
```
becomes:
```go
	// recovery outermost so it also catches panics raised inside logging.
	return recovery(logging(mux))
}
```

- [ ] **Step 6: Verify streaming still works through the wrapper**

The two streaming routes are the real risk: `GET /api/videos/{id}/stream` (range requests via `http.ServeContent`) and `GET /api/downloads/stream` (SSE). Run the full suite first:

Run: `make test`
Expected: PASS.

Then confirm SSE still flushes incrementally rather than buffering — start the app (`make dev`) and:
```bash
curl -N -s http://localhost:8080/api/downloads/stream --cookie "<session cookie>" | head -3
```
Expected: event lines appear promptly, not all at once on close. If they buffer, `Flush()` is not being reached — check that no intermediate wrapper is swallowing it.

- [ ] **Step 7: Document the conventions in AGENTS.md**

`AGENTS.md` currently says nothing about logging (`grep -i log AGENTS.md` returns nothing), so the `"err"` convention is undocumented and held up by habit alone. Add to the `## Working conventions` section:

```markdown
## Logging
- Structured `slog` only. Error attr key is **`err`**, never `error`.
- Short lowercase messages; variables go in attrs (`snake_case`: `job_id`, `video_id`, `path`).
- Every 500 goes through `serverError(w, r, err, "client message")` — it logs the cause and returns only the generic message. 4xx uses `writeJSONError` and is captured by the request middleware.
- **Never log a full URL, `RequestURI()`, or a query string.** The OIDC callback carries a live auth `code`. Log `r.URL.Path`. Wrap errors that may embed a URL in `redactErr()`.
- Level via `BACKEND_LOG_LEVEL` (debug/info/warn/error), read in `main()` before anything else.
```

- [ ] **Step 8: Commit**

```bash
git add backend/internal/httpapi/middleware.go backend/internal/httpapi/middleware_test.go \
        backend/internal/httpapi/server.go AGENTS.md
git commit -m "feat(logging): add request logging and panic recovery middleware"
```

---

## Verification

End-to-end, after all four tasks:

1. **Full suite:** `make test` → PASS. `cd backend && go vet ./... && gofmt -l .` → no output.
2. **Timestamps present:** `cd backend && go run ./cmd/peeq 2>&1 | head -2` → lines start with `time=2026-...`.
3. **Level knob works:** `BACKEND_LOG_LEVEL=error go run ./cmd/peeq 2>&1 | head -2` → the `starting peeq` INFO line is gone.
4. **Request log:** hit any API route → one `msg=request method=GET path=/api/videos status=200 dur=...` line. Hit `/healthz` → no line.
5. **The original bug:** with a deliberately wrong `BACKEND_OIDC_CLIENT_SECRET`, complete a login. Expected: browser still lands on `/?auth_error=oidc_callback_failed` (unchanged), and the log now contains `level=WARN msg="oidc callback failed" err="exchange oidc code: ..."` naming the provider's rejection.
6. **No secret leak — the important one:** grep the full log from step 5 for the auth code that appeared in the callback URL. Expected: absent. Also confirm no `code=` or `state=` appears anywhere in the output.

## Notes on scope

- **Loom is not a clean reference for the OIDC fix.** `loom/backend/internal/httpapi/auth_handlers.go:30-34` has the identical swallowed-error bug. Task 1 fixes peeq; loom is only the reference for Tasks 2-4. Consider porting Task 1 back to loom separately.
- **Not in scope:** the `_ =` discarded status writes in `summarize/worker.go` (~10 sites) and `download/worker.go:302`, which can strand a job in a stale state invisibly. Worth a follow-up, but they're a different subsystem with a different failure model.
- **Not in scope:** request/correlation IDs. Neither repo has them; adding them is a larger design question.
