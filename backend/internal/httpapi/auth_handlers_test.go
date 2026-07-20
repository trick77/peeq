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
