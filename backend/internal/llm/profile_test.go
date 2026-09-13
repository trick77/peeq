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

// The effort consts are strings this package chose, and the endpoint accepts
// exactly three. llmwire's profile is where that set is measured and kept; this
// pins the consts to it so a profile change (a new tier, a renamed default)
// fails here rather than as a code 1210 on the first summary after a deploy.
func TestReasoningEffortConstsMatchTheProfile(t *testing.T) {
	r := chatProfile.Reasoning
	for _, e := range []string{lowReasoningEffort, highReasoningEffort, maxReasoningEffort} {
		if !r.Accepts(e) {
			t.Errorf("reasoning effort %q is not in %s's profile (%v)", e, model, r.EffortValues)
		}
	}
	if reasoningEffort != r.DefaultEffort {
		t.Errorf("default effort = %q, profile's default is %q", reasoningEffort, r.DefaultEffort)
	}
	if shortGateModel != model {
		// Both ids go through chatRequestFor; if they ever split again the gate
		// deployment needs a profile of its own.
		if _, err := llmwire.Default().Lookup(shortGateModel); err != nil {
			t.Errorf("shortGateModel: %v", err)
		}
	}
	if MaxOutputTokens() <= 0 {
		t.Errorf("MaxOutputTokens() = %d, profile carries no output limit", MaxOutputTokens())
	}
}

// The rephrased error keeps the runbook's text AND llmwire's chain: a caller
// can still ask errors.Is for the rate-limit class and errors.As for the
// Retry-After. A plain fmt.Errorf without %w used to cut that, silently.
func TestComplete_statusErrorsKeepLlmwiresChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// A context-window cut is as deterministic as a max_tokens cut: the same prompt
// hits the same wall on every attempt, so FailOnEarlyFinish must not turn it
// into a retry. A filter or refusal still is one.
func TestComplete_failOnEarlyFinishToleratesAContextWindowCut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, sseFinish("partial", "model_context_window_exceeded"))
	}))
	defer srv.Close()
	c := mustClient(t, Config{BaseURL: srv.URL, Logger: discardLogger()}, srv.Client())
	out, err := c.Complete(FailOnEarlyFinish(context.Background()), []Message{{Role: "user", Content: "hi"}})
	if err != nil || out != "partial" {
		t.Fatalf("context cut under the flag: out=%q err=%v, want partial/nil", out, err)
	}
}
