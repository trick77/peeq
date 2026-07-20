# peeq Phase 4 — Companion browser extension (design)

Date: 2026-07-20
Status: approved for planning

## Problem

peeq refuses to call YouTube without a cookie. Today that cookie arrives by hand: export it
from a browser, paste the file into Settings. On 2026-07-20 the first real end-to-end run
failed, and the diagnosis reframed this whole phase.

`yt-dlp -v -F` against the stored cookie returned:

```
WARNING: [youtube] The provided YouTube account cookies are no longer valid.
         They have likely been rotated in the browser as a security measure.
```

The cookie file was structurally perfect — every session cookie present
(`SID`, `__Secure-1PSID`, `__Secure-3PSID`, `LOGIN_INFO`, `SAPISID`), nothing expired by
timestamp. It was dead server-side anyway. yt-dlp's own wiki names the cause:

> YouTube rotates account cookies frequently **on open YouTube browser tabs** as a security
> measure.

Rotation is *caused by browsing YouTube in the profile the cookie came from*. This is the
key fact of the phase, and it inverts the obvious design. An extension that continuously
mirrors the browser's live cookie does not defeat rotation — it subscribes to a session that
is rotating precisely because the user browses, leaving peeq one step behind and failing any
download that fires in the gap. The robust path is the opposite: a session that no browser
tab ever touches never rotates, and stays valid for months.

Not indicated by the probe: PO-token/SABR problems (`[pot] PO Token Providers: none`, no SABR
warning; extraction reached a real player API call and failed on `UNPLAYABLE` for a dead
session). The cookie invariant is sound. Phase 4's premise survives.

## What we are building

A Chrome extension, `peeq Companion`, that hands peeq a working YouTube sign-in in one click
and removes the file-export ceremony — without giving up the isolation that makes the
sign-in last.

It runs in a **dedicated Chrome profile** signed into a **dedicated YouTube account**. That
profile never browses YouTube, so nothing rotates the session. The extension reads the cookie
*store* rather than a page, so no YouTube tab is ever required.

The dedicated account also isolates blast radius: a rate-limit or flag lands on the throwaway
account, not the user's real one. (The diagnostic probe alone tripped an hour-long rate limit.)

### The routine

1. **Once:** create a Chrome profile, sign it into the dedicated YouTube account, navigate
   that tab to `youtube.com/robots.txt`, close the tab. (The `robots.txt` step is from
   yt-dlp's wiki: it ensures the last page loaded isn't a YouTube app page still rotating
   cookies.)
2. **Once:** install the extension in that profile; enter peeq's address and access token.
3. Open the profile → click the extension → **Send cookie to peeq** → close the profile.

Step 3 repeats only when peeq reports the cookie went bad, which in a non-browsing profile
should be rare.

## Explicitly out of scope

- **Auto re-push on cookie change.** Considered and rejected twice. It does not defeat
  rotation (see Problem), and in this deployment it is dead weight: the MV3 service worker
  only runs while that profile is open, and that profile is only open during a capture.
- **Firefox.** Chrome-only manifest. Code stays browser-agnostic (a `getBrowser()` shim, no
  Chrome-only APIs where a standard one exists) so adding Firefox later is a manifest file,
  not a rewrite.
- **A bundler or framework.** Plain MV3 JavaScript. This is a few hundred lines handling a
  Google session; unbundled source is easier to audit and needs no build step.
- **Bundling peeq's webfonts.** See Design system.
- **Incognito access, cookie-store pickers, `getAllCookieStores()`.** The dedicated-profile
  model makes the multi-store case not exist rather than handling it.

## Design system

Colors, spacing, and radii come verbatim from `ui/src/index.css` (Warm Editorial dark:
`--color-bg #1f1f1e`, `--color-accent-fill #c25f34`, `--color-online #5aa06a`,
`--color-kept #d6a15a` for warnings, `--color-danger #c14638`). Single-theme dark, matching
the product.

**Type is `system-ui`, not peeq's Anthropic Sans.** That face is self-hosted in
`ui/src/fonts/` and does not exist outside peeq's bundle; naming it in the extension would be
a silent fallback. The extension ships **no font files**. Headings are carried by weight (600)
and slight negative tracking. No serif anywhere.

Approved mockup: `https://claude.ai/code/artifact/23b03105-50f3-4c4c-bf93-2361175ce35f`

### Copy rule

**"Cookie" is singular** — matching `CookieStatus.tsx` ("YouTube cookie · active") and
`cookie_status`. The user sends *one sign-in*; that it is carried by 14 cookie lines is an
implementation detail. Plural only when literally counting ("Cookies sent: 14",
"Sign-in cookies: 5 of 5 present").

## Architecture

```
extension/
  manifest.json      MV3; permissions: cookies, storage; host_permissions: https://*.youtube.com/*
  background.js      service worker: read store, serialize, PUT
  popup.html/.js     status + one button
  options.html/.js   peeq address + access token
  shared.js          browser shim, Netscape serializer, session-cookie check
```

Lives in the peeq monorepo alongside `backend/` and `ui/`. Rationale: the extension speaks
exactly one endpoint and one auth scheme, so contract changes stay atomic; version skew
between two repos would surface as a silently failing cookie push, the hardest failure to
notice. Publishing to the Chrome Web Store takes an uploaded zip and never sees the repo.
If it ever needs its own repo, `git subtree split` lifts it with history intact.

### Data flow

`chrome.cookies.getAll({ domain: ".youtube.com" })` on the extension's **own** store
→ filter to YouTube domains → serialize to Netscape → `PUT /api/machine/cookie`
with `Authorization: Bearer <token>` → report peeq's verdict.

The service worker performs the fetch, so MV3 `host_permissions` bypass CORS and no preflight
handling is needed. (A content-script fetch would require `OPTIONS` support on the route.)

### Invariants

1. **The extension never stores a cookie.** Read → serialize → PUT → forget. The only
   persisted values are peeq's address and the access token, in `chrome.storage.local`.
2. **One cookie store, never merged.** Only the extension's own profile store is read.
3. **Never send a session-less jar.** If no session cookie is present, send nothing — an
   anonymous jar would overwrite peeq's good cookie with a worthless one.
4. **The token is write-only at peeq's end** and gates exactly one route.

### Netscape serialization

Two deliberate corrections to TubeArchivist's implementation, which was used as reference:

| Field | TA | peeq |
|---|---|---|
| Column 4 | writes `httpOnly` | writes `secure` — column 4 *is* secure per spec, and `netscape.go:17` parses it into `Cookie.Secure` |
| httpOnly | not emitted | `#HttpOnly_` domain prefix, which `netscape.go:43-47` already strips |
| Stores | merges all via `getAllCookieStores()` | this profile's store only |

TA's merge is not cosmetic: with two logged-in sessions it emits two different
`__Secure-1PSID` values into one jar, letting a dead session shadow a live one — precisely
the failure this phase exists to fix.

Line format: `domain \t includeSubdomains \t path \t secure \t expiry \t name \t value`,
with `includeSubdomains = TRUE` iff the domain starts with `.`, and
`expiry = Math.trunc(expirationDate) || 0`.

### Client-side precheck

Before enabling the button, the extension applies the same rule as `cookie.Validate`: at
least one `.youtube.com` entry, and at least one of `SID`, `__Secure-1PSID`,
`__Secure-3PSID`. This drives the popup's "Sign-in cookies: N of 5 present" line, so a failed
send tells the user something they did not already know.

## Popup states

| State | Look | Meaning |
|---|---|---|
| Ready | green | Session present; button enabled |
| Sending | pulsing accent | Request in flight; button disabled (no double-send) |
| Sent | green | peeq accepted **and validated**; tells the user to close the profile |
| Can't reach peeq | amber | Network/address failure; names the address tried, since a typo looks identical to a server being down |
| Token rejected | red | 401; names the exact fix (copy a fresh token) |
| No sign-in found | idle | No session cookies; **button removed, not disabled** |
| Not set up | idle | Post-install; one button to setup |

"Sent" reports peeq's verdict rather than merely a 200, because a cookie that arrives and
then fails validation is exactly the failure this phase exists to fix. Amber for
unreachability is deliberate: nothing is broken or lost, and reserving red for actionable
errors keeps red meaningful.

## Backend changes

**No schema change; no migration.** Only `0001_init.sql` exists and it stays untouched, so
the populated dev DB with real downloaded media survives. `PUT /api/machine/cookie`,
`applyCookie`, `SetCookie`, worker resume, and the rail indicator all already exist and are
behaviourally unchanged.

Three carried follow-ups from the API-token slice are folded in, all directly relevant:

1. **Lost-token window.** `apitoken_handlers.go:58` does a follow-up `APITokenInfo` read
   *after* the hash is stored. If that read fails, the response is 500 while the new token is
   already live — the old token is invalidated and the new one is never shown. Fix:
   `SetAPITokenHash` returns `created_at` via `RETURNING`, dropping the second read. This is
   hit during extension setup, which is when the user is copying a token.
2. **Minimal ack on the machine route.** `applyCookie` currently returns the full settings
   view, so a token holder can read settings. A token living in a browser extension is more
   exposed than one in a terminal, so the machine route should return a minimal ack
   (`{status, present}`) instead. The session route keeps the settings view — the shared
   helper gains a response-shape parameter; its validation and worker-resume path must remain
   byte-identical for both callers.
3. **Machine-path worker-resume test.** Guards the path the extension exercises on every send
   and protects a future re-split of `applyCookie`.

## Testing

Backend: table-driven Go tests as today; `go test -race`. The machine-path resume test and a
`RETURNING`-based `SetAPITokenHash` test are the new backend coverage.

Extension: unit tests for the pure functions — Netscape serializer and session-cookie
precheck — with the `chrome.*` API stubbed. No network, no real browser, no real cookies.

**Fakes must emit what the real thing emits.** The availability bug (PR #15) went undetected
because `fake-ytdlp.sh` emitted no `availability` field and the fake runner hardcoded the
already-normalized `"available"`, making normalization structurally untestable. Applied here:
stubbed `chrome.cookies.getAll` returns objects in Chrome's real shape — `expirationDate` as
a **float**, `domain` with a leading dot, `httpOnly`/`secure` as distinct booleans, and
session cookies with no `expirationDate` at all. A stub emitting pre-truncated integers would
make the `Math.trunc` step untestable, and one collapsing `httpOnly`/`secure` would hide
exactly the TA bug we are correcting.

At least one test must serialize a realistic jar and assert the output parses cleanly through
the real `cookie.Parse`/`cookie.Validate` — locking the JS serializer to the Go parser the
same way `TestAvailabilities_allAcceptedByDBCheck` locks the Go enum to the SQL CHECK.

## Risks

- **The profile is not as inert as a closed private window.** Opening YouTube in it starts
  rotation and the cookie goes stale. Mitigation is documentation plus peeq's existing
  status indicator; auto-refresh is explicitly *not* the mitigation, for the reasons above.
- **Recovery path.** When the cookie does die, peeq flips to `blocked`/`expired`, the rail
  shows it, and the user repeats step 3. No new machinery.
- **Token exposure.** A token in extension storage is readable by anyone with the profile.
  Bounded by follow-up 2 (minimal ack) and by the token gating exactly one write-only route.

## Verification

CI stays green: backend `build` + `vet` + `go test -race`; UI `build` + `test`. Backend CI
does not build the UI. A lint/test job for `extension/` is added alongside them.

Manual end-to-end, which is the only way to prove the phase: load unpacked in the dedicated
profile, send, confirm peeq's rail shows **YouTube cookie · active**, then confirm a real
download succeeds — the step that would have caught this morning's bug.
