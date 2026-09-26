package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/trick77/llmwire"
)

// The rephrased error keeps the runbook's text AND llmwire's chain: a caller
// can still ask errors.Is for the rate-limit class and errors.As for the
// Retry-After. A plain fmt.Errorf without %w used to cut that, silently.
func TestComplete_statusErrorsKeepLlmwiresChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"slow down","code":"1302"}}`)
	}))
	defer srv.Close()

	_, err := mustClient(t, fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client()).
		Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got != "chat failed with status 429: slow down" {
		t.Errorf("err = %q, the phrasing changed", got)
	}
	if !errors.Is(err, llmwire.ErrRateLimited) {
		t.Errorf("errors.Is(err, ErrRateLimited) = false; the chain to llmwire is cut")
	}
	var rl *llmwire.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("errors.As(*RateLimitError) = false on %v", err)
	}
	if rl.RetryAfter <= 0 {
		t.Errorf("RetryAfter = %v, want the header's 7s", rl.RetryAfter)
	}
}

// A context-window cut is as deterministic as a cut at our own cap: the same prompt
// hits the same wall on every attempt, so FailOnEarlyFinish must not turn it
// into a retry. A filter or refusal still is one.
func TestComplete_failOnEarlyFinishToleratesAContextWindowCut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, sseFinish("partial", "model_context_window_exceeded"))
	}))
	defer srv.Close()
	c := mustClient(t, Config{BaseURL: srv.URL, Logger: discardLogger()}, srv.Client())
	out, err := c.Complete(FailOnEarlyFinish(context.Background()), []Message{{Role: "user", Content: "hi"}})
	if err != nil || out != "partial" {
		t.Fatalf("context cut under the flag: out=%q err=%v, want partial/nil", out, err)
	}
}
