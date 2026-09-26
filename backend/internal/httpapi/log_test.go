package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestServerError_namesACancelledRequest(t *testing.T) {
	// A request whose client went away fails on whatever it was waiting on,
	// and that error hides the cancel: sqlite-vec reports an interrupt as
	// "SQL logic error: chunks iter error", which reads as a corrupt database.
	logs := captureLogs(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/search", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	serverError(rec, req, errors.New("retrieve: sqlite3: SQL logic error: chunks iter error"), "search failed")

	out := logs.String()
	for _, want := range []string{"WARN", "request cancelled", "/api/search", "context canceled", "chunks iter error"} {
		if !strings.Contains(out, want) {
			t.Errorf("log = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "ERROR") {
		t.Errorf("log = %q, want no error for a cancelled request", out)
	}
}

func TestLogAnswerFailed_namesACancelledAnswer(t *testing.T) {
	logs := captureLogs(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(nil)

	logAnswerFailed(ctx, errors.New("stream: context canceled"), 93*time.Second)

	out := logs.String()
	for _, want := range []string{"level=WARN", `msg="answer cancelled"`, `cause="context canceled"`,
		"step=answer", "took=1m33s"} {
		if !strings.Contains(out, want) {
			t.Errorf("log = %q, want %q", out, want)
		}
	}
}

func TestLogAnswerFailed_aFailureSaysHowLongItRan(t *testing.T) {
	logs := captureLogs(t)

	logAnswerFailed(context.Background(), errors.New("upstream 502"), 4*time.Second)

	out := logs.String()
	for _, want := range []string{"level=WARN", `msg="answer: chat failed"`, "upstream 502", "took=4s"} {
		if !strings.Contains(out, want) {
			t.Errorf("log = %q, want %q", out, want)
		}
	}
}
