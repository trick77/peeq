package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCompleteSendsModelAndEffortAndReturnsContent(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("missing auth header")
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, sseStream("hello world", ""))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	out, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello world" {
		t.Fatalf("content = %q", out)
	}
	if gotBody["model"] != "mimo-v2.5-pro" {
		t.Fatalf("model = %v", gotBody["model"])
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v", gotBody["reasoning_effort"])
	}
	if gotBody["stream"] != true {
		t.Fatalf("stream = %v", gotBody["stream"])
	}
	// Thinking is on unless the caller opts out, and it is sent explicitly:
	// omitting it costs the reasoning-token accounting, not the reasoning.
	if got := thinkingType(t, gotBody); got != "enabled" {
		t.Fatalf("thinking.type = %q", got)
	}
	_ = strings.TrimSpace
}

func TestComplete_withoutThinkingDisablesItOnTheWire(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, sseStream("news", ""))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Complete(WithoutThinking(context.Background()), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if got := thinkingType(t, gotBody); got != "disabled" {
		t.Fatalf("thinking.type = %q", got)
	}
	// Effort still rides along: the endpoint ignores it while thinking is off,
	// and dropping it would be a second, untested divergence from every other
	// request the client makes.
	if gotBody["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v", gotBody["reasoning_effort"])
	}
}

// thinkingType digs the switch out of a decoded request body, failing the test
// when the field is missing entirely — an absent object is exactly the bug this
// pair of tests exists to catch, and it would otherwise read as an empty type.
func thinkingType(t *testing.T, body map[string]any) string {
	t.Helper()
	obj, ok := body["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("request carried no thinking object: %v", body)
	}
	return obj["type"].(string)
}

func TestCompleteErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := NewClient(Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestComplete_pacesRequestsByInterval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sseStream("ok", ""))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, RequestInterval: 100 * time.Millisecond}, srv.Client())
	start := time.Now()
	for i := 0; i < 2; i++ {
		if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
			t.Fatalf("complete %d: %v", i, err)
		}
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Errorf("two paced calls took %v, want >= the 100ms interval between them", elapsed)
	}
}

func TestComplete_zeroIntervalDoesNotPace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sseStream("ok", ""))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL}, srv.Client()) // RequestInterval 0
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatalf("complete: %v", err)
	}
}

// sseFinish streams one content delta and a custom finish_reason, for the
// early-finish guard tests. Reasons are bare words, so inlining them needs no
// quoting helper.
func sseFinish(content, reason string) string {
	return sseEvent(`{"choices":[{"delta":{"content":"`+content+`","role":"assistant"},"finish_reason":null,"index":0}]}`) +
		sseEvent(`{"choices":[{"delta":{"content":null},"finish_reason":"`+reason+`","index":0}],"usage":null}`) +
		sseEvent(doneMarker)
}

func TestComplete_maxTokensAndReasoningEffortFromContext(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, sseStream("ok", ""))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL}, srv.Client())
	ctx := WithMaxTokens(WithReasoningEffort(context.Background(), "low"), 4000)
	if _, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if gotBody["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort = %v, want low (context override)", gotBody["reasoning_effort"])
	}
	if got, ok := gotBody["max_tokens"].(float64); !ok || int(got) != 4000 {
		t.Fatalf("max_tokens = %v, want 4000", gotBody["max_tokens"])
	}
}

// Absent an override, max_tokens is omitted entirely (leaving the endpoint's own
// limit) and reasoning_effort stays at the package default.
func TestComplete_maxTokensOmittedByDefault(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, sseStream("ok", ""))
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if _, present := gotBody["max_tokens"]; present {
		t.Fatalf("max_tokens present by default: %v", gotBody["max_tokens"])
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %v, want the default high", gotBody["reasoning_effort"])
	}
}

func TestComplete_failOnEarlyFinish(t *testing.T) {
	// content_filter under the flag → error, so a truncated answer is not
	// persisted (the summary call retries instead).
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sseFinish("partial", "content_filter"))
	}))
	defer cf.Close()
	c := NewClient(Config{BaseURL: cf.URL}, cf.Client())
	if _, err := c.Complete(FailOnEarlyFinish(context.Background()), []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Error("want an error when content_filter ends the answer under FailOnEarlyFinish")
	}
	// The same stream WITHOUT the flag returns the partial content as success —
	// the behavior every other caller relies on.
	if out, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil || out != "partial" {
		t.Fatalf("without the flag: out=%q err=%v, want partial/nil", out, err)
	}

	// length is tolerated even under the flag: that cut is our own max_tokens,
	// and retrying would just re-truncate.
	ln := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sseFinish("partial", "length"))
	}))
	defer ln.Close()
	lc := NewClient(Config{BaseURL: ln.URL}, ln.Client())
	if out, err := lc.Complete(FailOnEarlyFinish(context.Background()), []Message{{Role: "user", Content: "hi"}}); err != nil || out != "partial" {
		t.Fatalf("length under flag: out=%q err=%v, want partial/nil", out, err)
	}
}
