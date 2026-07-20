package httpapi

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// oidcStateCookieName and oidcNonceCookieName mirror the unexported cookie
// names auth.OIDCService uses internally (auth/oidc.go:16-17). They aren't
// exported, so tests that need to satisfy the cookie checks in
// HandleCallback duplicate the literal values here.
const (
	oidcStateCookieName = "peeq_oidc_state"
	oidcNonceCookieName = "peeq_oidc_nonce"
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
	// Given: OIDC configured with a backend whose Exchange fails with a
	// *url.Error carrying the auth code in its query string — the code
	// exchange failure (oidc.go:104-107) is one of the two failure modes
	// that embeds the callback URL, and only reaches redactErr's redaction
	// logic if the request first clears the state/nonce cookie checks.
	logs := captureLogs(t)
	exchangeErr := &url.Error{
		Op:  "Post",
		URL: "https://idp.example.com/token?code=SUPERSECRETCODE",
		Err: errors.New("exchange rejected"),
	}
	h := New(testDepsWithOIDCExchangeError(t, exchangeErr))

	// When: the callback carries a code that must never be logged, with
	// state/nonce cookies set so the handler proceeds past the cookie
	// checks and into Exchange.
	req := httptest.NewRequest(http.MethodGet, "/api/auth/callback?code=SUPERSECRETCODE&state=xyz", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookieName, Value: "xyz"})
	req.AddCookie(&http.Cookie{Name: oidcNonceCookieName, Value: "nonce123"})
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
