package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/logx"
	"github.com/trick77/ship/kit/auth"
)

// The transient cookies kit/auth's OIDC names from CookiePrefix "peeq_"
// (<prefix>oidc_<name>). Unexported there, so tests that need to satisfy the
// callback's cookie checks spell them here.
const (
	oidcStateCookieName = "peeq_oidc_state"
	oidcNonceCookieName = "peeq_oidc_nonce"
	oidcPKCECookieName  = "peeq_oidc_pkce"
)

// captureLogs redirects slog's default logger into a buffer for the duration
// of the test and restores it afterwards.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	// Wrapped like the process's logger (main): every err attr is redacted.
	slog.SetDefault(slog.New(logx.Handler(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

// The session is the cookie: /me answers the claims it carries, logout
// expires it, and a cookie whose signature does not check out is no session.
func TestAuthSession_meLogoutAndForgery(t *testing.T) {
	h := New(testDeps(t))
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/auth/me", nil)
	var me map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("/me = %d %s", rec.Code, rec.Body.String())
	}
	if me["sub"] != "dev-tester" || me["preferred_username"] != "dev" || me["email"] != "dev@example.local" {
		t.Fatalf("/me = %v, want the dev claims", me)
	}

	rec = doReq(t, h, cookie, http.MethodGet, "/api/auth/logout", nil)
	var cleared *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie {
			cleared = c
		}
	}
	if rec.Code != http.StatusOK || cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("logout = %d, cookie %+v; want the session cookie expired", rec.Code, cleared)
	}

	// A dead cookie (a pre-0043 opaque token) is no session, and logout still clears it.
	stale := &http.Cookie{Name: SessionCookie, Value: "opaque-token-from-the-sessions-table"}
	rec = doReq(t, h, stale, http.MethodGet, "/api/auth/logout", nil)
	if c := rec.Result().Cookies(); rec.Code != http.StatusOK || len(c) != 1 || c[0].MaxAge >= 0 {
		t.Fatalf("logout with a dead cookie = %d, cookies %v; want it cleared", rec.Code, c)
	}

	forged := &http.Cookie{Name: SessionCookie, Value: cookie.Value[:strings.LastIndex(cookie.Value, ".")] + ".forged"}
	if rec := doReq(t, h, forged, http.MethodGet, "/api/auth/me", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/me with a forged signature = %d, want 401", rec.Code)
	}
	// Another process's key (a rotated BACKEND_SESSION_SECRET) signs no session here.
	other := loginAndGetCookie(t, New(testDeps(t)))
	if rec := doReq(t, h, other, http.MethodGet, "/api/videos", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a cookie signed with another key = %d, want 401", rec.Code)
	}
}

// An OIDC login redirects to the provider with PKCE, and sets the three
// transient cookies the callback checks.
func TestAuthLogin_oidcStartsWithPKCE(t *testing.T) {
	h := New(testDepsWithOIDC(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/login", nil))
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "https://idp.example.com/authorize") {
		t.Fatalf("login = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if !strings.Contains(rec.Header().Get("Location"), "code_challenge_method=S256") {
		t.Fatalf("no PKCE challenge in %q", rec.Header().Get("Location"))
	}
	got := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		got[c.Name] = c.Value != "" && c.HttpOnly
	}
	for _, name := range []string{oidcStateCookieName, oidcNonceCookieName, oidcPKCECookieName} {
		if !got[name] {
			t.Errorf("cookie %s not set (HttpOnly)", name)
		}
	}
}

// A callback the provider vouches for ends in a session carrying its claims.
func TestAuthCallback_successIssuesASession(t *testing.T) {
	h := New(testDepsWithOIDCBackend(t, stubOIDCBackend{verified: &auth.Claims{Subject: "idp-sub", Email: "jan@example.com"}}))
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=c&state=xyz", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: "xyz"})
	req.AddCookie(&http.Cookie{Name: oidcNonceCookieName, Value: "nonce123"})
	req.AddCookie(&http.Cookie{Name: oidcPKCECookieName, Value: "verifier123"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback = %d %q, want a redirect to /", rec.Code, rec.Header().Get("Location"))
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookie && c.Value != "" {
			session = c
		}
	}
	if session == nil {
		t.Fatal("no session cookie issued")
	}
	me := doReq(t, h, session, http.MethodGet, "/api/auth/me", nil)
	if !strings.Contains(me.Body.String(), `"sub":"idp-sub"`) {
		t.Fatalf("/me = %s, want the provider's claims", me.Body.String())
	}
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
	// Given: OIDC configured with a backend whose Exchange fails with a
	// *url.Error carrying the auth code in its query string — the code
	// exchange failure is one of the two failure modes that embeds the
	// callback URL, and only reaches logx.RedactErr's redaction if the
	// request first clears the state/nonce/PKCE cookie checks.
	logs := captureLogs(t)
	exchangeErr := &url.Error{
		Op:  "Post",
		URL: "https://idp.example.com/token?code=SUPERSECRETCODE",
		Err: errors.New("exchange rejected"),
	}
	h := New(testDepsWithOIDCExchangeError(t, exchangeErr))

	// When: the callback carries a code that must never be logged, with
	// state/nonce/PKCE cookies set so the handler proceeds past the cookie
	// checks and into Exchange.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=SUPERSECRETCODE&state=xyz", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: "xyz"})
	req.AddCookie(&http.Cookie{Name: oidcNonceCookieName, Value: "nonce123"})
	req.AddCookie(&http.Cookie{Name: oidcPKCECookieName, Value: "verifier123"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Then: the secret never appears in the logs...
	out := logs.String()
	if strings.Contains(out, "SUPERSECRETCODE") {
		t.Fatalf("the auth code leaked into the logs: %s", out)
	}
	// ...but the diagnostic host/path do, so this is still debuggable.
	if !strings.Contains(out, "idp.example.com/token") {
		t.Fatalf("redaction dropped the useful diagnostic part; got: %s", out)
	}
}
