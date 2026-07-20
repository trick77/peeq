# peeq Companion Extension Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a Chrome MV3 extension that sends a YouTube sign-in from a dedicated browser profile to peeq in one click, plus three carried backend follow-ups.

**Architecture:** A new top-level `extension/` directory in the peeq monorepo, written as plain ES-module JavaScript with no bundler and no framework. Pure functions (Netscape serialization, session detection) live in `shared.js` and are unit-tested with Node's built-in test runner; the service worker, popup, and options page are thin shells over them. The extension reads only its own profile's cookie store and PUTs to `/api/machine/cookie`. Backend changes are three small corrections — no schema change, no migration.

**Tech Stack:** Chrome MV3 (manifest v3, ES modules, `chrome.cookies` / `chrome.storage.local` / `chrome.permissions`), Node ≥22 `node --test` for extension tests, Go 1.x + `ncruces/go-sqlite3` for the backend.

## Global Constraints

- **Design spec:** `docs/superpowers/specs/2026-07-20-peeq-phase-4-companion-extension-design.md`. Read it before Task 4.
- **Branch:** `feat/companion-extension`, already created off `master`. Never commit to `master`.
- **Commits:** Conventional Commits. Commit at the end of every task.
- **Env prefix:** `BACKEND_`, never `PEEQ_`.
- **Comments:** English.
- **Go:** `CGO_ENABLED=0` for builds; `go test -race` must pass.
- **Copy rule:** "cookie" is **singular** everywhere in UI text ("Send cookie to peeq"), plural **only** when literally counting ("Cookies sent: 14", "Sign-in cookies: 3 of 5 present").
- **Type:** `system-ui` stack only. **No serif. No font files. Never name "Anthropic Sans"** — it is self-hosted in `ui/src/fonts/` and does not exist outside peeq's bundle.
- **Design tokens:** copy hex values verbatim from `ui/src/index.css` (`--color-bg #1f1f1e`, `--color-panel #1b1b1a`, `--color-border #323230`, `--color-ink #faf9f5`, `--color-ink-dim #e4e1d8`, `--color-muted #9c9a92`, `--color-faint #6f6d66`, `--color-accent #c6613f`, `--color-accent-strong #d97757`, `--color-accent-fill #c25f34`, `--color-online #5aa06a`, `--color-danger #c14638`, `--color-kept #d6a15a`, `--color-elevated #363632`, `--color-elevated-border #454540`). Dark single-theme.
- **Auth header:** `Authorization: Bearer <token>`. **Not** `Token` (that is TubeArchivist's scheme and would 401 silently).
- **Extension invariants:** never persist a cookie; read only this profile's own store (never `getAllCookieStores()`); never send a jar with no session cookie.
- **Netscape format:** `domain \t includeSubdomains \t path \t secure \t expiry \t name \t value`. Column 4 is **secure**, not httpOnly. httpOnly is marked with a `#HttpOnly_` domain prefix.
- **Test fakes must emit what the real thing emits.** Chrome cookie stubs use real Chrome shapes: `expirationDate` as a **float**, session cookies with **no** `expirationDate` key at all, `domain` with a leading dot, `httpOnly` and `secure` as **distinct** booleans. A stub that pre-truncates expiry or collapses httpOnly/secure makes the code under test structurally untestable — this is the PR #15 lesson.
- **Do not touch** `ui/dist/index.html` (a deliberately tracked stub) or `backend/internal/store/migrations/0001_init.sql` (no schema change in this phase).

---

### Task 1: `SetAPITokenHash` returns `created_at`

Closes the lost-token window: `apitoken_handlers.go:58` currently re-reads `created_at` *after* the hash is stored, so a failed read returns 500 while the new token is already live — invalidating the old token and never showing the new one.

**Files:**
- Modify: `backend/internal/settings/store.go` (`SetAPITokenHash`)
- Modify: `backend/internal/httpapi/apitoken_handlers.go:54-63`
- Test: `backend/internal/settings/store_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func (s *Store) SetAPITokenHash(ctx context.Context, hash string) (string, error)` — returns the stamped `api_token_created_at`. **Signature change**: it previously returned only `error`.

- [ ] **Step 1: Write the failing test**

Add to `backend/internal/settings/store_test.go`. Follow the existing store-test setup helper in that file for creating a migrated store (do not invent a new one):

```go
func TestSetAPITokenHash_returnsStampedCreatedAt(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	createdAt, err := store.SetAPITokenHash(ctx, "hash-one")
	if err != nil {
		t.Fatalf("SetAPITokenHash: %v", err)
	}
	if createdAt == "" {
		t.Fatal("SetAPITokenHash returned an empty created_at")
	}

	// The returned value must be exactly what a subsequent read reports —
	// that equality is the whole point: it lets the handler skip the read.
	present, readCreatedAt, err := store.APITokenInfo(ctx)
	if err != nil {
		t.Fatalf("APITokenInfo: %v", err)
	}
	if !present {
		t.Fatal("APITokenInfo reports no token after SetAPITokenHash")
	}
	if readCreatedAt != createdAt {
		t.Fatalf("created_at mismatch: SetAPITokenHash=%q APITokenInfo=%q", createdAt, readCreatedAt)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/settings/ -run TestSetAPITokenHash_returnsStampedCreatedAt -v`
Expected: FAIL — compile error, `SetAPITokenHash(...) (string, error)` used as value / assignment mismatch (it currently returns only `error`).

- [ ] **Step 3: Change the store method**

Replace `SetAPITokenHash` in `backend/internal/settings/store.go` with:

```go
// SetAPITokenHash stores the token hash and stamps api_token_created_at,
// returning the stamp. Returning it from the same statement (rather than
// re-reading it) means a create can never half-succeed: previously a failed
// follow-up read left the new hash live while the caller got an error and
// never saw the plaintext, locking the user out of their own token.
func (s *Store) SetAPITokenHash(ctx context.Context, hash string) (string, error) {
	var createdAt string
	err := s.db.QueryRowContext(ctx, `
UPDATE settings
SET api_token_hash = ?, api_token_created_at = datetime('now')
WHERE id = 1
RETURNING api_token_created_at`, hash).Scan(&createdAt)
	if err != nil {
		return "", fmt.Errorf("set api token hash: %w", err)
	}
	return createdAt, nil
}
```

- [ ] **Step 4: Update the handler to drop the follow-up read**

In `backend/internal/httpapi/apitoken_handlers.go`, replace lines 54-63 (the `SetAPITokenHash` call through the `writeJSON`) with:

```go
	createdAt, err := s.settings.SetAPITokenHash(r.Context(), apitoken.Hash(token))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to store api token")
		return
	}
	writeJSON(w, apiTokenCreatedResponse{Token: token, CreatedAt: createdAt})
```

If `s.settings.APITokenInfo` is now unused in this file, leave it — it is still used by `handleGetAPIToken`. Do not remove imports that are still referenced.

- [ ] **Step 5: Run the full backend test suite**

Run: `cd backend && go build ./... && go vet ./... && CGO_ENABLED=1 go test -race ./...`
Expected: PASS, all packages. Any other caller of `SetAPITokenHash` must be updated for the new signature — search with `grep -rn "SetAPITokenHash" backend/` and fix compile errors before proceeding.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/settings/store.go backend/internal/settings/store_test.go backend/internal/httpapi/apitoken_handlers.go
git commit -m "fix(apitoken): stamp created_at in the write, closing the lost-token window"
```

---

### Task 2: Minimal ack on the machine cookie route

A token living in a browser extension is more exposed than one in a terminal, so the machine route must not return the full settings view. The session route is unchanged.

**Files:**
- Modify: `backend/internal/httpapi/settings_handlers.go` (`handleMachineCookie`, `handlePutSettingsCookie`, `applyCookie`)
- Test: `backend/internal/httpapi/apitoken_handlers_test.go`

**Interfaces:**
- Consumes: nothing from Task 1.
- Produces: `PUT /api/machine/cookie` responds `200 {"status":"valid","present":true}`. The extension in Task 8 depends on this shape.

- [ ] **Step 1: Write the failing tests**

Add to `backend/internal/httpapi/apitoken_handlers_test.go`, using the package's existing helpers — `testDeps(t)`, `New(deps)`, `loginAndGetCookie(t, h)`, `createToken(t, h, sessionCookie)`, and the `validYouTubeCookieBody` fixture. Do not add new helpers:

```go
func TestMachineCookie_returnsMinimalAckNotSettings(t *testing.T) {
	// Given: a generated token and a valid cookie body
	deps := testDeps(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)
	body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`

	// When: the machine route accepts the cookie
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: 200 with only status and present — no settings fields at all
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["status"] != "valid" {
		t.Fatalf(`status = %v, want "valid"`, got["status"])
	}
	if got["present"] != true {
		t.Fatalf("present = %v, want true", got["present"])
	}
	if len(got) != 2 {
		t.Fatalf("machine ack leaked extra fields: %v", got)
	}
	// Belt and braces: settings-only keys must never appear.
	for _, leaked := range []string{"format_preset", "limit_rate", "cookie_text", "api_token_hash"} {
		if _, ok := got[leaked]; ok {
			t.Fatalf("machine ack leaked %q", leaked)
		}
	}
}

func TestSessionCookieRoute_stillReturnsSettingsView(t *testing.T) {
	// Given: the session-authenticated cookie route (unchanged behaviour)
	deps := testDeps(t)
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`

	// When
	req := httptest.NewRequest(http.MethodPut, "/api/settings/cookie", strings.NewReader(body))
	req.AddCookie(sessionCookie)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: the full settings view, as before
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := got["format_preset"]; !ok {
		t.Fatalf("session route no longer returns the settings view: %v", got)
	}
}
```

Note: `newTokenTestServer`, `newSessionTestServer`, and `validCookieText` — use whatever the existing helpers/fixtures in this package are actually called. Read the file first and match them; do not add duplicates.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/httpapi/ -run "TestMachineCookie_returnsMinimalAck|TestSessionCookieRoute_stillReturns" -v`
Expected: `TestMachineCookie_returnsMinimalAckNotSettings` FAILS with "machine ack leaked extra fields" (it currently returns the whole settings view). `TestSessionCookieRoute_stillReturnsSettingsView` PASSES already — it is the regression guard.

- [ ] **Step 3: Split the response shape**

In `backend/internal/httpapi/settings_handlers.go`, change the two handlers and `applyCookie`:

```go
// machineCookieAck is the machine route's deliberately minimal response. A
// token holder can write a cookie and learn whether it took — nothing more.
type machineCookieAck struct {
	Status  string `json:"status"`
	Present bool   `json:"present"`
}

// handlePutSettingsCookie is the session-authenticated way the pasted cookie
// enters the system. On success it does not echo the cookie back — the
// response is the same cookie-body-free settings view as GET /api/settings.
func (s *server) handlePutSettingsCookie(w http.ResponseWriter, r *http.Request) {
	s.applyCookie(w, r, false)
}

// handleMachineCookie is the token-authenticated cookie-write path, used by
// the peeq browser extension. It is deliberately a separate route from
// handlePutSettingsCookie so that exactly one route in server.go bypasses
// OIDC, even though both share the write below. It answers with a minimal
// ack rather than the settings view: a token stored in a browser extension
// is more exposed than one in a terminal, so it should be able to read less.
func (s *server) handleMachineCookie(w http.ResponseWriter, r *http.Request) {
	s.applyCookie(w, r, true)
}
```

Then in `applyCookie`, change the signature to `func (s *server) applyCookie(w http.ResponseWriter, r *http.Request, minimalAck bool)` and replace **only** the final response block (the `got, err := s.settings.Get(...)` through `writeJSON(w, got)`) with:

```go
	if minimalAck {
		writeJSON(w, machineCookieAck{Status: "valid", Present: true})
		return
	}
	got, err := s.settings.Get(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load settings")
		return
	}
	writeJSON(w, got)
```

**Everything above that block — the nil check, body decode, empty check, `SetCookie`, and the `s.worker.Resume()` call — must remain byte-identical and shared by both callers.** The validation and resume path is the behaviour both routes exist to share.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd backend && go test ./internal/httpapi/ -v`
Expected: PASS, including both new tests and every pre-existing cookie test.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/httpapi/settings_handlers.go backend/internal/httpapi/apitoken_handlers_test.go
git commit -m "feat(machine): answer the machine cookie route with a minimal ack"
```

---

### Task 3: Machine-path worker-resume test

Guards the path the extension exercises on every send, and protects a future re-split of `applyCookie`.

**Files:**
- Test: `backend/internal/httpapi/apitoken_handlers_test.go`

**Interfaces:**
- Consumes: the `PUT /api/machine/cookie` route from Task 2.
- Produces: nothing.

- [ ] **Step 1: Write the failing test**

The package already has a `fakeWorker` with a `resumes()` counter in `downloads_handlers_test.go:28-45`, and `testDeps` accepts it via `deps.Worker` (see `downloads_handlers_test.go:72`). Reuse both — do **not** define a second fake.

```go
func TestMachineCookie_resumesWorker(t *testing.T) {
	// Given: a paused worker and a generated token
	deps := testDeps(t)
	worker := &fakeWorker{paused: true}
	deps.Worker = worker
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)
	before := worker.resumes()
	body := `{"cookie":` + strconv.Quote(validYouTubeCookieBody) + `}`

	// When: a machine-path cookie write succeeds
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: the download worker was un-wedged exactly once
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body)
	}
	if got := worker.resumes() - before; got != 1 {
		t.Fatalf("resumes = %d, want 1", got)
	}
}

func TestMachineCookie_doesNotResumeWorkerOnInvalidCookie(t *testing.T) {
	// Given: a wired worker and a body that fails cookie.Validate
	deps := testDeps(t)
	worker := &fakeWorker{paused: true}
	deps.Worker = worker
	h := New(deps)
	sessionCookie := loginAndGetCookie(t, h)
	token := createToken(t, h, sessionCookie)
	before := worker.resumes()

	// When
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie",
		strings.NewReader(`{"cookie":"this is not a netscape cookie file"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: rejected, and the worker was never resumed
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s)", rec.Code, rec.Body)
	}
	if got := worker.resumes() - before; got != 0 {
		t.Fatalf("resumes = %d, want 0 — resumed on an invalid cookie", got)
	}
}
```

Note `createToken` performs a login and its own requests, so read `resumes()` **after** setup (as `before` above) rather than assuming it starts at zero.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd backend && go test ./internal/httpapi/ -run "TestMachineCookie_resumesWorker|TestMachineCookie_doesNotResume" -v`
Expected: **PASS on the first run.** Both behaviours already work — these are coverage tests for an untested path, not a red-green cycle. That makes Step 3 the load-bearing step: a test that cannot fail is worthless, so prove it can.

- [ ] **Step 3: Prove the tests are not vacuous**

Temporarily comment out the `s.worker.Resume()` call in `applyCookie` and re-run. Expected: `TestMachineCookie_resumesWorker` FAILS with `resumes = 0, want 1`, while `TestMachineCookie_doesNotResumeWorkerOnInvalidCookie` still passes (it asserts the absence). Restore the line and confirm both pass. Do not commit the commented-out version.

- [ ] **Step 4: Run the package suite**

Run: `cd backend && go test ./internal/httpapi/ -v`
Expected: PASS, including every pre-existing test.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/httpapi/apitoken_handlers_test.go
git commit -m "test(machine): cover worker resume on the machine cookie path"
```

---

### Task 4: Extension scaffold and the Netscape serializer

**Files:**
- Create: `extension/package.json`
- Create: `extension/manifest.json`
- Create: `extension/shared.js`
- Test: `extension/shared.test.js`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `export function cookieLine(cookie): string`
  - `export function toNetscape(cookies): string`
  - `export function isYouTubeDomain(domain): boolean`

- [ ] **Step 1: Create `extension/package.json`**

```json
{
  "name": "peeq-companion",
  "private": true,
  "type": "module",
  "scripts": {
    "test": "node --test"
  }
}
```

No dependencies. Node's built-in test runner is the whole toolchain.

- [ ] **Step 2: Create `extension/manifest.json`**

```json
{
  "manifest_version": 3,
  "name": "peeq Companion",
  "description": "Send a YouTube sign-in to your peeq server.",
  "version": "0.1.0",
  "permissions": ["cookies", "storage"],
  "host_permissions": ["https://*.youtube.com/*"],
  "optional_host_permissions": ["https://*/*", "http://*/*"],
  "action": { "default_popup": "popup.html" },
  "options_page": "options.html",
  "background": { "service_worker": "background.js", "type": "module" }
}
```

`host_permissions` covers `chrome.cookies` on YouTube. `optional_host_permissions` exists because peeq's address is user-configured and cannot be static — Task 9 requests it at runtime. Without that grant the PUT is preflighted and peeq has no `OPTIONS` handler.

- [ ] **Step 3: Write the failing test**

Create `extension/shared.test.js`:

```js
import { test } from "node:test";
import assert from "node:assert/strict";
import { cookieLine, toNetscape, isYouTubeDomain } from "./shared.js";

// Chrome's real cookie shape: expirationDate is a FLOAT, session cookies omit
// it entirely, domain carries a leading dot, and httpOnly/secure are distinct.
// Stubs that pre-truncate or collapse those fields would make the code under
// test structurally untestable (see PR #15).
const SID = {
  domain: ".youtube.com", path: "/", name: "SID", value: "abc123",
  secure: true, httpOnly: true, expirationDate: 1819099943.123456,
};
const PREF = {
  domain: ".youtube.com", path: "/", name: "PREF", value: "f6=40000000",
  secure: false, httpOnly: false, expirationDate: 1819099943.9,
};
const SESSION_ONLY = {
  domain: "www.youtube.com", path: "/", name: "YSC", value: "xyz",
  secure: true, httpOnly: true,
};

test("cookieLine writes secure in column 4, not httpOnly", () => {
  // PREF is httpOnly:false secure:false; SID is httpOnly:true secure:true.
  // A serializer that wrote httpOnly into column 4 would still pass on SID,
  // so the discriminating case is a cookie where the two differ.
  const mixed = { ...PREF, secure: true, httpOnly: false };
  const fields = cookieLine(mixed).split("\t");
  assert.equal(fields.length, 7);
  assert.equal(fields[3], "TRUE", "column 4 must be `secure`");
});

test("cookieLine marks httpOnly with the #HttpOnly_ domain prefix", () => {
  assert.ok(cookieLine(SID).startsWith("#HttpOnly_.youtube.com\t"));
  assert.ok(cookieLine(PREF).startsWith(".youtube.com\t"));
});

test("cookieLine sets includeSubdomains from the leading dot", () => {
  assert.equal(cookieLine(PREF).split("\t")[1], "TRUE");
  assert.equal(cookieLine(SESSION_ONLY).split("\t")[1], "FALSE");
});

test("cookieLine truncates the float expiry to an integer", () => {
  assert.equal(cookieLine(PREF).split("\t")[4], "1819099943");
});

test("cookieLine writes 0 for a session cookie with no expirationDate", () => {
  assert.equal(cookieLine(SESSION_ONLY).split("\t")[4], "0");
});

test("toNetscape emits a header and one line per cookie", () => {
  const out = toNetscape([SID, PREF]);
  const lines = out.split("\n");
  assert.ok(lines[0].startsWith("# Netscape HTTP Cookie File"));
  const data = lines.filter((l) => l.trim() !== "" && !l.startsWith("# "));
  assert.equal(data.length, 2);
  assert.ok(out.endsWith("\n"), "file must end with a newline");
});

test("toNetscape uses tabs, never spaces, as separators", () => {
  for (const line of toNetscape([SID, PREF]).split("\n")) {
    if (line.trim() === "" || line.startsWith("# ")) continue;
    assert.equal(line.split("\t").length, 7, `not 7 tab fields: ${line}`);
  }
});

test("isYouTubeDomain accepts youtube.com and subdomains, rejects lookalikes", () => {
  assert.equal(isYouTubeDomain(".youtube.com"), true);
  assert.equal(isYouTubeDomain("youtube.com"), true);
  assert.equal(isYouTubeDomain("www.youtube.com"), true);
  assert.equal(isYouTubeDomain("notyoutube.com"), false);
  assert.equal(isYouTubeDomain("youtube.com.evil.test"), false);
  assert.equal(isYouTubeDomain("google.com"), false);
});
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `cd extension && npm test`
Expected: FAIL — `Cannot find module './shared.js'`.

- [ ] **Step 5: Write `extension/shared.js`**

```js
// Pure helpers shared by the service worker, popup, and options page.
// Everything here is side-effect free and unit-tested; the chrome.* calls
// live in background.js so this file stays testable under plain Node.

// Netscape cookie-file field order:
//   domain \t includeSubdomains \t path \t secure \t expiry \t name \t value
// Column 4 is SECURE. TubeArchivist's extension writes httpOnly there; peeq's
// parser reads that column into Cookie.Secure, so copying them would store a
// lie. httpOnly is instead marked by the #HttpOnly_ domain prefix, which
// backend/internal/cookie/netscape.go strips before its comment check.
export function cookieLine(cookie) {
  const domain = cookie.httpOnly ? `#HttpOnly_${cookie.domain}` : cookie.domain;
  const includeSubdomains = cookie.domain.startsWith(".") ? "TRUE" : "FALSE";
  const secure = cookie.secure ? "TRUE" : "FALSE";
  // Chrome reports expirationDate as a float and omits it for session
  // cookies; the file format wants an integer, and 0 means "session".
  const expiry = Number.isFinite(cookie.expirationDate)
    ? Math.trunc(cookie.expirationDate)
    : 0;
  return [domain, includeSubdomains, cookie.path, secure, expiry, cookie.name, cookie.value].join("\t");
}

export function toNetscape(cookies) {
  const lines = [
    "# Netscape HTTP Cookie File",
    "# Generated by peeq Companion. Do not edit.",
    "",
  ];
  for (const cookie of cookies) {
    lines.push(cookieLine(cookie));
  }
  return lines.join("\n") + "\n";
}

// Mirrors backend/internal/cookie/netscape.go Validate: a domain counts as
// YouTube when, with any leading dot removed, it is youtube.com or a
// subdomain of it. Suffix matching alone would accept youtube.com.evil.test.
export function isYouTubeDomain(domain) {
  const bare = domain.startsWith(".") ? domain.slice(1) : domain;
  return bare === "youtube.com" || bare.endsWith(".youtube.com");
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd extension && npm test`
Expected: PASS, 8 tests.

- [ ] **Step 7: Commit**

```bash
git add extension/package.json extension/manifest.json extension/shared.js extension/shared.test.js
git commit -m "feat(extension): scaffold MV3 manifest and the Netscape serializer"
```

---

### Task 5: Session-cookie gate and display set

**Files:**
- Modify: `extension/shared.js`
- Test: `extension/shared.test.js`

**Interfaces:**
- Consumes: `isYouTubeDomain` from Task 4.
- Produces:
  - `export const GATE_COOKIE_NAMES` (3 names)
  - `export const DISPLAY_COOKIE_NAMES` (5 names)
  - `export function selectYouTubeCookies(cookies): Cookie[]`
  - `export function hasSessionCookie(cookies): boolean`
  - `export function countDisplayCookies(cookies): number`

- [ ] **Step 1: Write the failing test**

Append to `extension/shared.test.js`:

```js
import {
  GATE_COOKIE_NAMES, DISPLAY_COOKIE_NAMES,
  selectYouTubeCookies, hasSessionCookie, countDisplayCookies,
} from "./shared.js";

const yt = (name) => ({
  domain: ".youtube.com", path: "/", name, value: "v",
  secure: true, httpOnly: true, expirationDate: 1819099943.5,
});

test("the gate is exactly Validate's trio", () => {
  assert.deepEqual([...GATE_COOKIE_NAMES].sort(),
    ["SID", "__Secure-1PSID", "__Secure-3PSID"].sort());
});

test("the display set is the five reported names", () => {
  assert.equal(DISPLAY_COOKIE_NAMES.length, 5);
  for (const gated of GATE_COOKIE_NAMES) {
    assert.ok(DISPLAY_COOKIE_NAMES.includes(gated),
      `${gated} gates the button so it must also be displayed`);
  }
});

test("selectYouTubeCookies keeps only YouTube entries", () => {
  const mixed = [yt("SID"), { ...yt("SID"), domain: ".google.com" }];
  const kept = selectYouTubeCookies(mixed);
  assert.equal(kept.length, 1);
  assert.equal(kept[0].domain, ".youtube.com");
});

test("hasSessionCookie is true for any one of the trio", () => {
  for (const name of GATE_COOKIE_NAMES) {
    assert.equal(hasSessionCookie([yt(name)]), true, `${name} should gate open`);
  }
});

test("hasSessionCookie is false for a jar with no session cookie", () => {
  // The anonymous-jar case: sending this would overwrite peeq's good cookie.
  assert.equal(hasSessionCookie([yt("PREF"), yt("VISITOR_INFO1_LIVE")]), false);
  assert.equal(hasSessionCookie([]), false);
});

test("hasSessionCookie ignores a session cookie on a non-YouTube domain", () => {
  assert.equal(hasSessionCookie([{ ...yt("SID"), domain: ".google.com" }]), false);
});

test("countDisplayCookies counts only display-set members, without double counting", () => {
  assert.equal(countDisplayCookies([yt("SID"), yt("SAPISID"), yt("PREF")]), 2);
  assert.equal(countDisplayCookies([yt("SID"), yt("SID")]), 1, "duplicates count once");
  assert.equal(countDisplayCookies([]), 0);
});

test("a 3-of-5 jar still passes the gate", () => {
  // The display set is informational; only the trio gates. A jar missing
  // SAPISID/LOGIN_INFO is perfectly valid and must not disable the button.
  const jar = [yt("SID"), yt("__Secure-1PSID"), yt("__Secure-3PSID")];
  assert.equal(countDisplayCookies(jar), 3);
  assert.equal(hasSessionCookie(jar), true);
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd extension && npm test`
Expected: FAIL — `GATE_COOKIE_NAMES` is not exported.

- [ ] **Step 3: Implement in `extension/shared.js`**

Append:

```js
// The gate: backend/internal/cookie/netscape.go Validate accepts a jar when
// at least ONE of these is present on a YouTube domain. This and only this
// decides whether the send button is enabled.
export const GATE_COOKIE_NAMES = ["SID", "__Secure-1PSID", "__Secure-3PSID"];

// The display set is INFORMATIONAL ONLY — the "N of 5" readout. It is a
// superset of the gate. A 3-of-5 jar is valid and must never be blocked.
export const DISPLAY_COOKIE_NAMES = [
  "SID", "__Secure-1PSID", "__Secure-3PSID", "SAPISID", "LOGIN_INFO",
];

export function selectYouTubeCookies(cookies) {
  return cookies.filter((c) => isYouTubeDomain(c.domain));
}

export function hasSessionCookie(cookies) {
  return selectYouTubeCookies(cookies).some((c) => GATE_COOKIE_NAMES.includes(c.name));
}

export function countDisplayCookies(cookies) {
  const present = new Set(
    selectYouTubeCookies(cookies)
      .map((c) => c.name)
      .filter((name) => DISPLAY_COOKIE_NAMES.includes(name)),
  );
  return present.size;
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd extension && npm test`
Expected: PASS, 16 tests.

- [ ] **Step 5: Commit**

```bash
git add extension/shared.js extension/shared.test.js
git commit -m "feat(extension): add the session-cookie gate and display set"
```

---

### Task 6: Lock the JS serializer to the Go parser

The JS serializer and Go parser are two implementations of one format in two languages, with no compiler linking them. This task pins them together the way `TestAvailabilities_allAcceptedByDBCheck` pins the Go enum to the SQL CHECK.

**Files:**
- Create: `extension/testdata/generate_fixture.js`
- Create: `backend/internal/cookie/testdata/extension_output.txt`
- Test: `backend/internal/cookie/netscape_test.go`

**Interfaces:**
- Consumes: `toNetscape`, `selectYouTubeCookies` from Tasks 4-5.
- Produces: a committed fixture that both sides assert against.

- [ ] **Step 1: Write the fixture generator**

Create `extension/testdata/generate_fixture.js`:

```js
// Regenerates backend/internal/cookie/testdata/extension_output.txt — the
// fixture the Go parser test reads. Run after any serializer change:
//   node testdata/generate_fixture.js > ../backend/internal/cookie/testdata/extension_output.txt
// The cookie shapes here mirror what chrome.cookies.getAll really returns.
import { toNetscape, selectYouTubeCookies } from "../shared.js";

const cookies = [
  { domain: ".youtube.com", path: "/", name: "SID", value: "g.a000abc",
    secure: true, httpOnly: true, expirationDate: 1819099943.123456 },
  { domain: ".youtube.com", path: "/", name: "__Secure-1PSID", value: "g.a000def",
    secure: true, httpOnly: true, expirationDate: 1819099943.987654 },
  { domain: ".youtube.com", path: "/", name: "__Secure-3PSID", value: "g.a000ghi",
    secure: true, httpOnly: true, expirationDate: 1819099943.5 },
  { domain: ".youtube.com", path: "/", name: "SAPISID", value: "sapi123",
    secure: true, httpOnly: false, expirationDate: 1819099943.0 },
  { domain: ".youtube.com", path: "/", name: "LOGIN_INFO", value: "AFmmF2s",
    secure: true, httpOnly: true, expirationDate: 1819099943.25 },
  { domain: ".youtube.com", path: "/", name: "PREF", value: "f6=40000000",
    secure: false, httpOnly: false, expirationDate: 1819099943.75 },
  // Session cookie: no expirationDate at all.
  { domain: "www.youtube.com", path: "/", name: "YSC", value: "sessionval",
    secure: true, httpOnly: true },
  // Non-YouTube: must be filtered out before serialization.
  { domain: ".google.com", path: "/", name: "SID", value: "should-not-appear",
    secure: true, httpOnly: true, expirationDate: 1819099943.0 },
];

process.stdout.write(toNetscape(selectYouTubeCookies(cookies)));
```

- [ ] **Step 2: Generate the fixture**

```bash
cd extension && mkdir -p ../backend/internal/cookie/testdata \
  && node testdata/generate_fixture.js > ../backend/internal/cookie/testdata/extension_output.txt
```

Then inspect it: `cat ../backend/internal/cookie/testdata/extension_output.txt`. Confirm by eye that there are 7 data lines, that the `.google.com` entry is absent, that `YSC` has expiry `0`, and that httpOnly lines carry the `#HttpOnly_` prefix.

- [ ] **Step 3: Write the failing Go test**

Add to `backend/internal/cookie/netscape_test.go`:

```go
// TestParse_extensionOutput locks the peeq Companion extension's JavaScript
// serializer to this Go parser. They are two implementations of one file
// format in two languages with nothing linking them at compile time, so the
// fixture is the contract. Regenerate it with:
//   cd extension && node testdata/generate_fixture.js > ../backend/internal/cookie/testdata/extension_output.txt
func TestParse_extensionOutput(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "extension_output.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	text := string(raw)

	// The whole point: extension output must satisfy the real validator.
	if err := Validate(text); err != nil {
		t.Fatalf("Validate rejected extension output: %v", err)
	}

	cookies, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(cookies) != 7 {
		t.Fatalf("parsed %d cookies, want 7", len(cookies))
	}

	byName := make(map[string]Cookie, len(cookies))
	for _, c := range cookies {
		byName[c.Name] = c
		if c.Domain == ".google.com" {
			t.Fatal("non-YouTube cookie leaked into the serialized jar")
		}
	}

	// PREF is secure:false httpOnly:false; SAPISID is secure:true
	// httpOnly:false. Together they prove column 4 carries `secure` and not
	// `httpOnly` — the exact TubeArchivist bug this guards against.
	if byName["PREF"].Secure {
		t.Error("PREF: Secure = true, want false (column 4 is not httpOnly)")
	}
	if !byName["SAPISID"].Secure {
		t.Error("SAPISID: Secure = false, want true")
	}
	// Float expiry must arrive truncated, not rounded or mangled.
	if got := byName["PREF"].Expiry; got != 1819099943 {
		t.Errorf("PREF: Expiry = %d, want 1819099943", got)
	}
	// A session cookie carries expiry 0.
	if got := byName["YSC"].Expiry; got != 0 {
		t.Errorf("YSC: Expiry = %d, want 0", got)
	}
	// The #HttpOnly_ prefix must be stripped, leaving a clean domain.
	if got := byName["SID"].Domain; got != ".youtube.com" {
		t.Errorf("SID: Domain = %q, want %q", got, ".youtube.com")
	}
}
```

Add `os` and `path/filepath` to the test file's imports if not already present.

- [ ] **Step 4: Run the test**

Run: `cd backend && go test ./internal/cookie/ -run TestParse_extensionOutput -v`
Expected: PASS. If it fails, the serializer is wrong — fix `extension/shared.js` and regenerate, never hand-edit the fixture.

- [ ] **Step 5: Prove the test is not vacuous**

In `extension/shared.js`, temporarily swap `cookie.secure` for `cookie.httpOnly` in `cookieLine`'s `secure` computation (reintroducing TA's bug), regenerate the fixture, and re-run the Go test. Expected: FAIL on `PREF: Secure = true, want false`. Then revert both the code and the fixture and confirm PASS.

- [ ] **Step 6: Commit**

```bash
git add extension/testdata/generate_fixture.js backend/internal/cookie/testdata/extension_output.txt backend/internal/cookie/netscape_test.go
git commit -m "test(cookie): lock the extension serializer to the Go parser"
```

---

### Task 7: Config storage

**Files:**
- Create: `extension/config.js`
- Test: `extension/config.test.js`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `export function normalizeBaseUrl(input): string` — trims, strips trailing slashes, throws `Error` on an unparseable or non-http(s) URL
  - `export function originOf(baseUrl): string` — `"https://peeq.home.lan/*"` pattern for `chrome.permissions`
  - `export function cookieEndpoint(baseUrl): string`
  - `export async function loadConfig(storage): {baseUrl, token}`
  - `export async function saveConfig(storage, {baseUrl, token}): void`

`storage` is injected (defaulting to `chrome.storage.local` in the browser) so tests can pass a plain fake.

- [ ] **Step 1: Write the failing test**

Create `extension/config.test.js`:

```js
import { test } from "node:test";
import assert from "node:assert/strict";
import { normalizeBaseUrl, originOf, cookieEndpoint, loadConfig, saveConfig } from "./config.js";

// A minimal stand-in for chrome.storage.local: same promise-returning
// get/set shape the real API has under MV3.
function fakeStorage(initial = {}) {
  let data = { ...initial };
  return {
    async get(keys) {
      if (Array.isArray(keys)) {
        return Object.fromEntries(keys.filter((k) => k in data).map((k) => [k, data[k]]));
      }
      return { ...data };
    },
    async set(items) { data = { ...data, ...items }; },
    peek() { return { ...data }; },
  };
}

test("normalizeBaseUrl strips trailing slashes and whitespace", () => {
  assert.equal(normalizeBaseUrl("  https://peeq.home.lan/  "), "https://peeq.home.lan");
  assert.equal(normalizeBaseUrl("https://peeq.home.lan///"), "https://peeq.home.lan");
  assert.equal(normalizeBaseUrl("https://peeq.home.lan:8080"), "https://peeq.home.lan:8080");
});

test("normalizeBaseUrl keeps a path prefix", () => {
  assert.equal(normalizeBaseUrl("https://host.test/peeq/"), "https://host.test/peeq");
});

test("normalizeBaseUrl rejects junk and non-http schemes", () => {
  assert.throws(() => normalizeBaseUrl(""), /address/i);
  assert.throws(() => normalizeBaseUrl("not a url"), /address/i);
  assert.throws(() => normalizeBaseUrl("ftp://host.test"), /http/i);
  assert.throws(() => normalizeBaseUrl("javascript:alert(1)"), /http/i);
});

test("originOf produces a chrome.permissions match pattern", () => {
  assert.equal(originOf("https://peeq.home.lan"), "https://peeq.home.lan/*");
  assert.equal(originOf("https://peeq.home.lan:8080"), "https://peeq.home.lan:8080/*");
  // A path prefix must not leak into the origin pattern.
  assert.equal(originOf("https://host.test/peeq"), "https://host.test/*");
});

test("cookieEndpoint appends the machine route", () => {
  assert.equal(cookieEndpoint("https://peeq.home.lan"), "https://peeq.home.lan/api/machine/cookie");
  assert.equal(cookieEndpoint("https://host.test/peeq"), "https://host.test/peeq/api/machine/cookie");
});

test("loadConfig returns empty strings when nothing is stored", async () => {
  const cfg = await loadConfig(fakeStorage());
  assert.deepEqual(cfg, { baseUrl: "", token: "" });
});

test("saveConfig then loadConfig round-trips a normalized address", async () => {
  const storage = fakeStorage();
  await saveConfig(storage, { baseUrl: "https://peeq.home.lan/", token: "pq_secret" });
  assert.deepEqual(await loadConfig(storage), {
    baseUrl: "https://peeq.home.lan",
    token: "pq_secret",
  });
});

test("saveConfig never writes a cookie or any other key", async () => {
  const storage = fakeStorage();
  await saveConfig(storage, { baseUrl: "https://peeq.home.lan", token: "pq_secret" });
  assert.deepEqual(Object.keys(storage.peek()).sort(), ["baseUrl", "token"]);
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd extension && npm test`
Expected: FAIL — `Cannot find module './config.js'`.

- [ ] **Step 3: Write `extension/config.js`**

```js
// Persisted settings. The extension stores peeq's address and the API token
// and NOTHING else — never a cookie, which is read fresh on every send and
// discarded immediately after the request.

const KEYS = ["baseUrl", "token"];

export function normalizeBaseUrl(input) {
  const trimmed = String(input ?? "").trim();
  if (trimmed === "") {
    throw new Error("Enter peeq's address, for example https://peeq.home.lan");
  }
  let url;
  try {
    url = new URL(trimmed);
  } catch {
    throw new Error("That doesn't look like an address. Try https://peeq.home.lan");
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    throw new Error("peeq's address must start with http:// or https://");
  }
  return (url.origin + url.pathname).replace(/\/+$/, "");
}

// chrome.permissions wants an origin match pattern; a configured path prefix
// is not part of the origin and must not appear here.
export function originOf(baseUrl) {
  return new URL(baseUrl).origin + "/*";
}

export function cookieEndpoint(baseUrl) {
  return `${baseUrl}/api/machine/cookie`;
}

export async function loadConfig(storage) {
  const stored = await storage.get(KEYS);
  return { baseUrl: stored.baseUrl ?? "", token: stored.token ?? "" };
}

export async function saveConfig(storage, { baseUrl, token }) {
  await storage.set({ baseUrl: normalizeBaseUrl(baseUrl), token });
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd extension && npm test`
Expected: PASS, 25 tests.

- [ ] **Step 5: Commit**

```bash
git add extension/config.js extension/config.test.js
git commit -m "feat(extension): add config storage and address normalization"
```

---

### Task 8: The send pipeline

**Files:**
- Create: `extension/send.js`
- Create: `extension/background.js`
- Test: `extension/send.test.js`

**Interfaces:**
- Consumes: `toNetscape`, `selectYouTubeCookies`, `hasSessionCookie`, `countDisplayCookies` (Tasks 4-5); `cookieEndpoint` (Task 7).
- Produces: `export async function sendCookie(deps): Result` where `Result` is `{ ok: boolean, state: string, detail?: string, count?: number }` and `state` is one of `"sent" | "no-session" | "unreachable" | "rejected" | "permission-denied" | "not-configured" | "server-error"`. The popup in Task 10 switches on `state`.

- [ ] **Step 1: Write the failing test**

Create `extension/send.test.js`:

```js
import { test } from "node:test";
import assert from "node:assert/strict";
import { sendCookie } from "./send.js";

const ytCookie = (name, extra = {}) => ({
  domain: ".youtube.com", path: "/", name, value: "v",
  secure: true, httpOnly: true, expirationDate: 1819099943.5, ...extra,
});

const SIGNED_IN = [ytCookie("SID"), ytCookie("__Secure-1PSID"), ytCookie("PREF")];

function deps({
  config = { baseUrl: "https://peeq.home.lan", token: "pq_tok" },
  cookies = SIGNED_IN,
  hasPermission = true,
  fetchImpl = async () => new Response(JSON.stringify({ status: "valid", present: true }), { status: 200 }),
} = {}) {
  const calls = [];
  return {
    calls,
    loadConfig: async () => config,
    getCookies: async () => cookies,
    hasPermission: async () => hasPermission,
    fetch: async (url, init) => { calls.push({ url, init }); return fetchImpl(url, init); },
  };
}

test("a signed-in jar is serialized and PUT with a Bearer token", async () => {
  const d = deps();
  const result = await sendCookie(d);

  assert.equal(result.ok, true);
  assert.equal(result.state, "sent");
  assert.equal(result.count, 3);
  assert.equal(d.calls.length, 1);

  const { url, init } = d.calls[0];
  assert.equal(url, "https://peeq.home.lan/api/machine/cookie");
  assert.equal(init.method, "PUT");
  // Bearer, NOT "Token" — TubeArchivist uses Token and it would 401 silently.
  assert.equal(init.headers.Authorization, "Bearer pq_tok");
  assert.equal(init.headers["Content-Type"], "application/json");

  const body = JSON.parse(init.body);
  assert.ok(body.cookie.includes("#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t1819099943\tSID\tv"));
  assert.ok(!("cookies" in body), "the API field is `cookie`, singular");
});

test("a jar with no session cookie is never sent", async () => {
  // Sending an anonymous jar would overwrite peeq's good cookie.
  const d = deps({ cookies: [ytCookie("PREF"), ytCookie("VISITOR_INFO1_LIVE")] });
  const result = await sendCookie(d);

  assert.equal(result.ok, false);
  assert.equal(result.state, "no-session");
  assert.equal(d.calls.length, 0, "no request may be made without a session cookie");
});

test("missing configuration short-circuits before any request", async () => {
  const d = deps({ config: { baseUrl: "", token: "" } });
  const result = await sendCookie(d);

  assert.equal(result.state, "not-configured");
  assert.equal(d.calls.length, 0);
});

test("a missing host permission is reported distinctly from unreachable", async () => {
  // Both surface as a failed fetch but need opposite fixes, so they must not
  // be collapsed into one state.
  const d = deps({ hasPermission: false });
  const result = await sendCookie(d);

  assert.equal(result.state, "permission-denied");
  assert.equal(d.calls.length, 0, "must not attempt a fetch without permission");
});

test("a network failure reports unreachable and names the address", async () => {
  const d = deps({ fetchImpl: async () => { throw new TypeError("Failed to fetch"); } });
  const result = await sendCookie(d);

  assert.equal(result.ok, false);
  assert.equal(result.state, "unreachable");
  assert.ok(result.detail.includes("peeq.home.lan"),
    "a typo in the address looks identical to a server being down, so name it");
});

test("401 reports a rejected token", async () => {
  const d = deps({ fetchImpl: async () => new Response("unauthorized", { status: 401 }) });
  const result = await sendCookie(d);

  assert.equal(result.ok, false);
  assert.equal(result.state, "rejected");
});

test("a 400 from cookie validation is a server-error carrying peeq's reason", async () => {
  // peeq accepted the request but refused the cookie — the user needs the why.
  const d = deps({
    fetchImpl: async () => new Response(JSON.stringify({ error: "invalid cookie: no session" }), { status: 400 }),
  });
  const result = await sendCookie(d);

  assert.equal(result.ok, false);
  assert.equal(result.state, "server-error");
  assert.ok(result.detail.includes("no session"));
});

test("the cookie body is not retained on the result", async () => {
  const result = await sendCookie(deps());
  assert.ok(!JSON.stringify(result).includes("__Secure-1PSID"),
    "the extension must never hold on to cookie material");
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd extension && npm test`
Expected: FAIL — `Cannot find module './send.js'`.

- [ ] **Step 3: Write `extension/send.js`**

```js
// The send pipeline, with every chrome.* dependency injected so it runs
// under plain Node in tests. background.js supplies the real ones.
import { toNetscape, selectYouTubeCookies, hasSessionCookie, countDisplayCookies } from "./shared.js";
import { cookieEndpoint } from "./config.js";

export async function sendCookie({ loadConfig, getCookies, hasPermission, fetch }) {
  const { baseUrl, token } = await loadConfig();
  if (!baseUrl || !token) {
    return { ok: false, state: "not-configured" };
  }

  const all = await getCookies();
  const cookies = selectYouTubeCookies(all);
  // Guard: an anonymous jar would overwrite peeq's good cookie with a
  // worthless one, so send nothing at all.
  if (!hasSessionCookie(cookies)) {
    return { ok: false, state: "no-session", count: countDisplayCookies(cookies) };
  }

  // peeq's origin is user-configured, so it can't be a static host_permission.
  // Without the runtime grant the PUT would be preflighted and peeq answers
  // no OPTIONS — so check first and report it as its own state.
  if (!(await hasPermission())) {
    return { ok: false, state: "permission-denied" };
  }

  let response;
  try {
    response = await fetch(cookieEndpoint(baseUrl), {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        // Bearer: auth/token_middleware.go compares with EqualFold. Note
        // TubeArchivist sends "Token <key>" — copying that 401s silently.
        Authorization: `Bearer ${token}`,
      },
      body: JSON.stringify({ cookie: toNetscape(cookies) }),
    });
  } catch (err) {
    return { ok: false, state: "unreachable", detail: `Couldn't reach ${baseUrl}` };
  }

  if (response.status === 401) {
    return { ok: false, state: "rejected" };
  }
  if (!response.ok) {
    let detail = `peeq answered ${response.status}`;
    try {
      const body = await response.json();
      if (body && body.error) detail = body.error;
    } catch {
      // A non-JSON error body is not itself an error; keep the status line.
    }
    return { ok: false, state: "server-error", detail };
  }

  return { ok: true, state: "sent", count: countDisplayCookies(cookies) };
}
```

- [ ] **Step 4: Write `extension/background.js`**

```js
// Service worker: wires the real chrome.* APIs into the send pipeline and
// answers messages from the popup. It holds no state of its own.
import { sendCookie } from "./send.js";
import { loadConfig, originOf } from "./config.js";
import { selectYouTubeCookies, hasSessionCookie, countDisplayCookies, DISPLAY_COOKIE_NAMES } from "./shared.js";

const api = globalThis.browser ?? globalThis.chrome;

// Reads only THIS profile's store. Never getAllCookieStores(): merging stores
// can put two different sessions' __Secure-1PSID into one jar and let a dead
// session shadow a live one.
const getCookies = () => api.cookies.getAll({ domain: ".youtube.com" });

async function realDeps() {
  const config = await loadConfig(api.storage.local);
  return {
    loadConfig: async () => config,
    getCookies,
    hasPermission: async () =>
      config.baseUrl ? api.permissions.contains({ origins: [originOf(config.baseUrl)] }) : false,
    fetch: (url, init) => fetch(url, init),
  };
}

async function status() {
  const config = await loadConfig(api.storage.local);
  if (!config.baseUrl || !config.token) return { state: "not-configured" };
  const cookies = selectYouTubeCookies(await getCookies());
  return {
    state: hasSessionCookie(cookies) ? "ready" : "no-session",
    baseUrl: config.baseUrl,
    count: countDisplayCookies(cookies),
    total: DISPLAY_COOKIE_NAMES.length,
  };
}

api.runtime.onMessage.addListener((message, _sender, sendResponse) => {
  const handler = message?.type === "send" ? async () => sendCookie(await realDeps())
    : message?.type === "status" ? status
    : null;
  if (!handler) return false;
  handler().then(sendResponse, (err) => sendResponse({ ok: false, state: "server-error", detail: String(err) }));
  return true; // keep the message channel open for the async response
});
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd extension && npm test`
Expected: PASS, 33 tests.

- [ ] **Step 6: Commit**

```bash
git add extension/send.js extension/send.test.js extension/background.js
git commit -m "feat(extension): add the cookie send pipeline and service worker"
```

---

### Task 9: Options page with the runtime permission request

**Files:**
- Create: `extension/options.html`
- Create: `extension/options.js`
- Create: `extension/styles.css`

**Interfaces:**
- Consumes: `loadConfig`, `saveConfig`, `normalizeBaseUrl`, `originOf` (Task 7).
- Produces: `extension/styles.css`, shared by the popup in Task 10.

- [ ] **Step 1: Create `extension/styles.css`**

Tokens copied verbatim from `ui/src/index.css`. No font files, no serif.

```css
/* peeq Companion — Warm Editorial dark, tokens copied from ui/src/index.css.
   system-ui deliberately: peeq's Anthropic Sans is self-hosted in the app
   bundle and does not exist here, so naming it would be a silent fallback. */
:root {
  --bg: #1f1f1e;
  --panel: #1b1b1a;
  --active: #2c2c2a;
  --border: #323230;
  --border-soft: #2a2a28;
  --ink: #faf9f5;
  --ink-dim: #e4e1d8;
  --muted: #9c9a92;
  --faint: #6f6d66;
  --accent: #c6613f;
  --accent-strong: #d97757;
  --accent-fill: #c25f34;
  --online: #5aa06a;
  --danger: #c14638;
  --kept: #d6a15a;
  --elevated: #363632;
  --elevated-border: #454540;
  --sans: system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  --mono: ui-monospace, "SF Mono", Menlo, monospace;
}

* { box-sizing: border-box; }

body {
  margin: 0;
  background: var(--bg);
  color: var(--ink);
  font-family: var(--sans);
  font-size: 15px;
  line-height: 1.55;
  -webkit-font-smoothing: antialiased;
}

h1 { font-size: 20px; font-weight: 600; letter-spacing: -0.01em; margin: 0 0 4px; }
.lede { color: var(--muted); font-size: 13px; margin: 0 0 24px; max-width: 52ch; }

.field { display: grid; gap: 6px; margin-bottom: 16px; }
.field label { font-size: 13px; color: var(--ink-dim); }
.field .hint { font-size: 11px; color: var(--faint); }
.field input {
  font-family: var(--mono);
  font-size: 13px;
  color: var(--ink);
  background: #191917;
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 9px 12px;
  width: 100%;
}
.field input:focus {
  outline: none;
  border-color: var(--accent);
  box-shadow: 0 0 0 3px rgba(198, 97, 63, 0.16);
}

.btn {
  font-family: inherit;
  font-size: 13px;
  padding: 8px 14px;
  border-radius: 8px;
  border: 1px solid var(--border);
  background: var(--active);
  color: var(--ink-dim);
  cursor: pointer;
}
.btn:hover { background: var(--elevated); border-color: var(--elevated-border); }
.btn:focus-visible { outline: 2px solid var(--accent-strong); outline-offset: 2px; }
.btn.primary { background: var(--accent-fill); border-color: var(--accent-fill); color: #fff; font-weight: 500; }
.btn.primary:hover { background: var(--accent-strong); border-color: var(--accent-strong); }
.btn.ghost { background: transparent; border-color: transparent; color: var(--muted); padding-inline: 6px; }
.btn[disabled] { opacity: 0.5; cursor: default; }

.led { width: 9px; height: 9px; border-radius: 999px; background: var(--faint); flex: 0 0 auto; }
.led.ok { background: var(--online); box-shadow: 0 0 0 4px rgba(90, 160, 106, 0.14); }
.led.warn { background: var(--kept); box-shadow: 0 0 0 4px rgba(214, 161, 90, 0.14); }
.led.bad { background: var(--danger); box-shadow: 0 0 0 4px rgba(193, 70, 56, 0.14); }
.led.busy { background: var(--accent-strong); animation: pulse 1.4s ease-in-out infinite; }
@keyframes pulse { 0%, 100% { opacity: 1; } 50% { opacity: 0.35; } }
@media (prefers-reduced-motion: reduce) { * { animation: none !important; transition: none !important; } }

.note {
  margin-top: 24px;
  padding: 12px 16px;
  border-left: 2px solid var(--accent);
  background: rgba(198, 97, 63, 0.06);
  border-radius: 0 8px 8px 0;
  font-size: 13px;
  color: var(--ink-dim);
}
.row { display: flex; align-items: center; gap: 12px; margin-top: 24px; }
.verdict { font-size: 13px; color: var(--muted); display: flex; align-items: center; gap: 8px; }
```

- [ ] **Step 2: Create `extension/options.html`**

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>peeq Companion — settings</title>
  <link rel="stylesheet" href="styles.css">
  <style> body { padding: 32px; max-width: 560px; } </style>
</head>
<body>
  <h1>Connect to peeq</h1>
  <p class="lede">peeq needs a YouTube sign-in to download videos. This extension sends it from this Chrome profile.</p>

  <div class="field">
    <label for="baseUrl">peeq address</label>
    <input id="baseUrl" type="text" spellcheck="false" placeholder="https://peeq.home.lan">
    <span class="hint">Where you open peeq in your browser.</span>
  </div>

  <div class="field">
    <label for="token">Access token</label>
    <input id="token" type="password" spellcheck="false" placeholder="Paste the token from peeq">
    <span class="hint">peeq &rarr; Settings &rarr; Access token &rarr; Copy. Shown once, when you create it.</span>
  </div>

  <div class="note">
    When you save, Chrome will ask permission for the extension to talk to peeq's address. That grant is what lets it send the cookie, so choose Allow.
  </div>

  <div class="row">
    <button class="btn primary" id="save">Save and connect</button>
    <span class="verdict" id="verdict" hidden><span class="led" id="led"></span><span id="verdictText"></span></span>
  </div>

  <script type="module" src="options.js"></script>
</body>
</html>
```

- [ ] **Step 3: Create `extension/options.js`**

```js
import { loadConfig, saveConfig, normalizeBaseUrl, originOf } from "./config.js";

const api = globalThis.browser ?? globalThis.chrome;
const $ = (id) => document.getElementById(id);

function verdict(kind, text) {
  $("verdict").hidden = false;
  $("led").className = `led ${kind}`;
  $("verdictText").textContent = text;
}

async function init() {
  const { baseUrl, token } = await loadConfig(api.storage.local);
  $("baseUrl").value = baseUrl;
  $("token").value = token;
}

$("save").addEventListener("click", async () => {
  let normalized;
  try {
    normalized = normalizeBaseUrl($("baseUrl").value);
  } catch (err) {
    verdict("bad", err.message);
    return;
  }
  if (!$("token").value.trim()) {
    verdict("bad", "Paste the access token from peeq's Settings page.");
    return;
  }

  // Must be called from the click itself: chrome.permissions.request requires
  // a user gesture, so it cannot be moved after an await of something slow.
  let granted;
  try {
    granted = await api.permissions.request({ origins: [originOf(normalized)] });
  } catch (err) {
    verdict("bad", `Chrome refused the permission request: ${err.message}`);
    return;
  }
  if (!granted) {
    verdict("bad", "Chrome permission denied. Without it the extension can't reach peeq.");
    return;
  }

  await saveConfig(api.storage.local, { baseUrl: normalized, token: $("token").value.trim() });
  verdict("ok", "Connected. Open the extension to send the cookie.");
});

init();
```

- [ ] **Step 4: Verify the extension loads**

Run: `cd extension && npm test` — expected PASS (33 tests; this task adds no unit tests, as it is DOM glue over already-tested functions).

Then load unpacked in Chrome (`chrome://extensions` → Developer mode → Load unpacked → select `extension/`). Confirm: no manifest errors, the options page renders in peeq's dark palette with no serif anywhere, saving a bad address shows an inline error, and saving a good one raises Chrome's permission prompt.

- [ ] **Step 5: Commit**

```bash
git add extension/options.html extension/options.js extension/styles.css
git commit -m "feat(extension): add the options page with a runtime permission grant"
```

---

### Task 10: Popup

**Files:**
- Create: `extension/popup.html`
- Create: `extension/popup.js`

**Interfaces:**
- Consumes: the `status` and `send` messages from Task 8's service worker; `styles.css` from Task 9.
- Produces: nothing.

- [ ] **Step 1: Create `extension/popup.html`**

```html
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <title>peeq Companion</title>
  <link rel="stylesheet" href="styles.css">
  <style>
    body { width: 340px; }
    .hd { display: flex; align-items: center; gap: 8px; padding: 12px 16px; border-bottom: 1px solid var(--border-soft); }
    .wordmark { font-size: 15px; letter-spacing: -0.005em; }
    .host { margin-left: auto; font-family: var(--mono); font-size: 11px; color: var(--faint); }
    .body { padding: 16px; }
    .status { display: flex; gap: 12px; align-items: flex-start; }
    .status .led { margin-top: 7px; }
    .status h2 { margin: 0; font-size: 15px; font-weight: 600; line-height: 1.35; }
    .status p { margin: 4px 0 0; font-size: 13px; color: var(--muted); }
    .facts { margin: 16px 0 0; padding-top: 12px; border-top: 1px solid var(--border-soft); display: grid; gap: 8px; }
    .fact { display: flex; gap: 12px; font-size: 13px; }
    .fact dt { color: var(--faint); flex: 0 0 104px; margin: 0; }
    .fact dd { margin: 0; color: var(--ink-dim); font-variant-numeric: tabular-nums; }
    .actions { margin-top: 16px; display: flex; gap: 8px; }
  </style>
</head>
<body>
  <div class="hd">
    <span class="wordmark"><b>peeq</b> Companion</span>
    <span class="host" id="host"></span>
  </div>
  <div class="body">
    <div class="status">
      <span class="led" id="led"></span>
      <div>
        <h2 id="headline">Checking…</h2>
        <p id="detail"></p>
      </div>
    </div>
    <dl class="facts" id="facts" hidden></dl>
    <div class="actions" id="actions"></div>
  </div>
  <script type="module" src="popup.js"></script>
</body>
</html>
```

- [ ] **Step 2: Create `extension/popup.js`**

Copy strings exactly — they follow the singular-"cookie" rule and each error names its own fix.

```js
const api = globalThis.browser ?? globalThis.chrome;
const $ = (id) => document.getElementById(id);

// Amber, not red, for unreachable: it usually means you're on another network
// and nothing is broken. Red stays reserved for errors you must act on.
const VIEWS = {
  ready: { led: "ok", headline: "Ready to send", detail: "A YouTube sign-in is present in this profile." },
  sending: { led: "busy", headline: "Sending…", detail: "Handing the sign-in to peeq." },
  sent: { led: "ok", headline: "peeq has the sign-in", detail: "Accepted and validated. You can close this profile." },
  unreachable: { led: "warn", headline: "Can't reach peeq", detail: "Check that peeq is running and the address is right. Nothing was sent." },
  rejected: { led: "bad", headline: "peeq rejected the token", detail: "The access token is wrong, or was regenerated. Copy a fresh one from peeq's Settings." },
  "permission-denied": { led: "bad", headline: "Chrome permission missing", detail: "The extension needs permission to talk to peeq's address. Grant it in settings." },
  "server-error": { led: "bad", headline: "peeq refused the cookie", detail: "" },
  "no-session": { led: "idle", headline: "No YouTube sign-in in this profile", detail: "Sign in to YouTube in this Chrome profile, then come back." },
  "not-configured": { led: "idle", headline: "Connect to peeq", detail: "Add peeq's address and an access token to get started." },
};

function render(state, extra = {}) {
  const view = VIEWS[state] ?? VIEWS["server-error"];
  $("led").className = `led ${view.led === "idle" ? "" : view.led}`;
  $("headline").textContent = view.headline;
  $("detail").textContent = extra.detail || view.detail;
  $("host").textContent = extra.baseUrl ?? "";

  const facts = $("facts");
  facts.replaceChildren();
  // "Cookies sent" is only true after a successful send. Every other state
  // carrying a count is reporting what is PRESENT, so label it that way —
  // a no-session result must never read as though something was sent.
  const rows = [];
  if (state === "sent" && extra.count !== undefined) {
    rows.push(["Cookies sent", String(extra.count)]);
  } else if (extra.count !== undefined) {
    rows.push(["Sign-in cookies", `${extra.count} of ${extra.total ?? 5} present`]);
  }
  facts.hidden = rows.length === 0;
  for (const [key, value] of rows) {
    const row = document.createElement("div");
    row.className = "fact";
    const dt = document.createElement("dt");
    dt.textContent = key;
    const dd = document.createElement("dd");
    dd.textContent = value;
    row.append(dt, dd);
    facts.append(row);
  }

  const actions = $("actions");
  actions.replaceChildren();
  // No send button without a session: an anonymous jar would overwrite
  // peeq's good cookie, so the action is removed rather than disabled.
  if (state === "ready" || state === "sent" || state === "unreachable" || state === "server-error") {
    actions.append(button(state === "sent" ? "Send again" : "Send cookie to peeq", "btn primary", send));
  }
  if (state !== "sending") {
    actions.append(button(state === "not-configured" ? "Get started" : "Settings", "btn ghost", () => api.runtime.openOptionsPage()));
  }
}

function button(label, className, onClick) {
  const el = document.createElement("button");
  el.className = className;
  el.textContent = label;
  el.addEventListener("click", onClick);
  return el;
}

async function send() {
  render("sending");
  const result = await api.runtime.sendMessage({ type: "send" });
  render(result.state, { detail: result.detail, count: result.count });
}

async function init() {
  const status = await api.runtime.sendMessage({ type: "status" });
  render(status.state, status);
}

init();
```

- [ ] **Step 3: Verify in the browser**

Reload the unpacked extension. With no config, the popup must show "Connect to peeq". After configuring, it must show "Ready to send" with a "Sign-in cookies: N of 5 present" line when signed in, and "No YouTube sign-in in this profile" **with no send button** when signed out.

- [ ] **Step 4: Run the full suite**

Run: `cd extension && npm test` — expected PASS, 33 tests.

- [ ] **Step 5: Commit**

```bash
git add extension/popup.html extension/popup.js
git commit -m "feat(extension): add the popup with its send and status states"
```

---

### Task 11: CI, docs, and full verification

**Files:**
- Modify: `.github/workflows/ci.yaml`
- Modify: `README.md`
- Modify: `AGENTS.md`

**Interfaces:**
- Consumes: everything.
- Produces: a green branch ready for PR.

- [ ] **Step 1: Add the extension CI job**

Append to `.github/workflows/ci.yaml`, matching the existing jobs' style:

```yaml
  extension:
    name: Extension (test)
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: extension
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-node@v7
        with:
          node-version: "22"
      - run: npm test
      # The committed Netscape fixture is generated from shared.js and asserted
      # by a Go test. Nothing links them at compile time, so a serializer change
      # with a forgotten regeneration would leave the Go test passing against a
      # stale-but-valid fixture — exactly the drift this phase exists to
      # prevent. Fail loudly instead.
      - name: Fixture is up to date with the serializer
        run: |
          node testdata/generate_fixture.js > /tmp/fixture-fresh.txt
          diff -u ../backend/internal/cookie/testdata/extension_output.txt /tmp/fixture-fresh.txt \
            || { echo "::error::extension_output.txt is stale. Regenerate with: cd extension && node testdata/generate_fixture.js > ../backend/internal/cookie/testdata/extension_output.txt"; exit 1; }
```

No `npm ci` and no cache key: the extension has zero dependencies and no lockfile by design.

- [ ] **Step 2: Document the routine in `README.md`**

Add a section. Keep the ordering — the `robots.txt` step is load-bearing:

```markdown
## Browser extension (peeq Companion)

peeq needs a YouTube sign-in to download videos. YouTube rotates account
cookies on open YouTube browser tabs, so a cookie exported from the profile
you browse with dies quickly. The fix is a profile that never browses.

Once:

1. Create a dedicated Chrome profile and sign it into a dedicated YouTube
   account (not your personal one — this isolates any rate-limit or block).
2. In that tab, navigate to `https://www.youtube.com/robots.txt`, then close
   the tab. This stops a YouTube app page from rotating the cookie underneath
   you.
3. In peeq: Settings → Access token → create one and copy it.
4. Load `extension/` at `chrome://extensions` (Developer mode → Load
   unpacked) in that profile, open its options, paste peeq's address and the
   token, and allow the permission prompt.

Whenever peeq reports the cookie is no longer valid:

1. Open the dedicated profile.
2. Click the extension → **Send cookie to peeq**.
3. Close the profile.

Do not browse YouTube in that profile — that is what starts rotation.
```

- [ ] **Step 3: Add the extension rules to `AGENTS.md`**

Keep it to steering rules only (the file has a lean-content policy):

```markdown
## Extension (`extension/`)

- Plain MV3 ES modules. No bundler, no framework, no dependencies. Tests are
  `node --test`.
- Never `getAllCookieStores()` — read only this profile's store. Merging
  stores lets a dead session shadow a live one.
- Never persist a cookie; only `baseUrl` and `token` go in storage.
- Never send a jar without a gate cookie (`SID`, `__Secure-1PSID`,
  `__Secure-3PSID`) — an anonymous jar overwrites peeq's good cookie.
- Netscape column 4 is `secure`, not `httpOnly`. After changing the
  serializer, regenerate the cross-language fixture:
  `cd extension && node testdata/generate_fixture.js > ../backend/internal/cookie/testdata/extension_output.txt`
- `Authorization: Bearer`, never `Token`.
- UI: `system-ui` only, no serif, no font files. "cookie" is singular except
  when counting.
```

- [ ] **Step 4: Full verification**

Run each and confirm:

```bash
cd backend && go build ./... && go vet ./... && CGO_ENABLED=1 go test -race ./... && cd ..
cd extension && npm test && cd ..
cd ui && npm ci && npm run build && npm test -- --run && cd ..
git status --porcelain
```

Expected: all green. `git status --porcelain` must be empty — if `ui/dist/index.html` is dirty from the UI build, restore it (`git checkout ui/dist/index.html`); it is a deliberately tracked stub, not a bug to fix.

- [ ] **Step 5: Manual end-to-end (the step that proves the phase)**

This is the only check that would have caught the PR #15 class of bug. In the dedicated profile: click **Send cookie to peeq**, confirm the popup reports "peeq has the sign-in", confirm peeq's rail shows **YouTube cookie · active**, then **add a real video and confirm the download completes**. A cookie that is accepted but does not actually work is exactly the failure this phase exists to fix.

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/ci.yaml README.md AGENTS.md
git commit -m "ci(extension): run extension tests and document the capture routine"
```
