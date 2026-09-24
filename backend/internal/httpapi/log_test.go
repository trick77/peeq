package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
