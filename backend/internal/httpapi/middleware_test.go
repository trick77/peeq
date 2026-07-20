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
	out := logs.String()
	if strings.Contains(out, "SUPERSECRETCODE") {
		t.Fatalf("auth code leaked into the request log: %s", out)
	}
	// Guard against a vacuous pass: the request must actually have been
	// logged, otherwise the absence of the secret proves nothing.
	if !strings.Contains(out, "path=/api/auth/callback") {
		t.Fatalf("request was not logged at all; redaction check is vacuous: %s", out)
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

// flushRecorder wraps httptest.NewRecorder and counts Flush calls, so tests
// can prove a Flush() reaches the underlying writer through statusRecorder
// without spinning up a real server. This matters for /api/downloads/stream
// (SSE): if Flush() were swallowed by the wrapper, events would buffer until
// the connection closes instead of arriving live.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushRecorder) Flush() { f.flushes++ }

func TestStatusRecorder_flushReachesTheUnderlyingWriter(t *testing.T) {
	// Given: a statusRecorder wrapping a ResponseWriter that tracks flushes,
	// simulating how logging() wraps the mux in front of the SSE handler.
	underlying := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	rec := &statusRecorder{ResponseWriter: underlying}

	// When: the handler flushes, as sseHub does after every event.
	rec.Flush()
	rec.Flush()

	// Then: the flush reached the underlying writer, not just statusRecorder.
	if underlying.flushes != 2 {
		t.Fatalf("flushes = %d, want 2 (Flush() did not reach the underlying writer)", underlying.flushes)
	}
}

func TestStatusRecorder_unwrapReturnsTheUnderlyingWriter(t *testing.T) {
	// Given: a statusRecorder wrapping an httptest recorder, simulating how
	// logging() wraps the mux in front of the range-streaming video handler.
	// Note: statusRecorder itself implements http.Flusher, so
	// http.ResponseController.Flush() would short-circuit there and never
	// exercise Unwrap — asserting on Unwrap() directly is the only way to
	// prove it returns the writer http.ResponseController actually needs for
	// capabilities statusRecorder doesn't forward itself (e.g. http.Hijacker).
	underlying := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: underlying}

	// When
	got := rec.Unwrap()

	// Then
	if got != underlying {
		t.Fatalf("Unwrap() did not return the underlying writer")
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
