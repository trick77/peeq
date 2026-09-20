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

	c := mustClient(t, Config{BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	out, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if out != "hello world" {
		t.Fatalf("content = %q", out)
	}
	if gotBody["model"] != model {
		t.Fatalf("model = %v", gotBody["model"])
	}
	// The default is max, which is the endpoint's own default and Z.ai's
	// recommendation — a regression to a shallower value would be silent.
	if gotBody["reasoning_effort"] != maxReasoningEffort {
		t.Fatalf("reasoning_effort = %v", gotBody["reasoning_effort"])
	}
	// Z.ai's recommended sampling point. Omitting these does not give "the
	// model's defaults", it gives lower ones, so absence is the bug to catch.
	//
	// Literals on purpose. The values come from llmwire's profile now, and the
	// whole point of this assertion is that the right numbers reach the wire —
	// comparing against the same constant the renderer read would pass whatever
	// that constant became.
	if gotBody["temperature"] != 1.0 {
		t.Fatalf("temperature = %v, want 1.0", gotBody["temperature"])
	}
	if gotBody["top_p"] != 0.95 {
		t.Fatalf("top_p = %v, want 0.95", gotBody["top_p"])
	}
	if gotBody["stream"] != true {
		t.Fatalf("stream = %v", gotBody["stream"])
	}
	// No hand-built thinking object. Depth is reasoning_effort, rendered by
	// llmwire from its profile; the object peeq used to add rode in ExtraBody
	// and is exactly the kind of by-hand wire knob this package no longer owns.
	if _, ok := gotBody["thinking"]; ok {
		t.Fatalf("request carried a thinking object: %v", gotBody["thinking"])
	}
	// No stream_options. Z.ai does not take the parameter and sends usage on the
	// final frame regardless; sending it would be an undocumented field.
	if _, ok := gotBody["stream_options"]; ok {
		t.Fatalf("request carried stream_options: %v", gotBody["stream_options"])
	}
	_ = strings.TrimSpace
}

// Shallow cannot switch thinking off — the endpoint refuses that outright with
// code 1210 — so it lowers reasoning_effort instead. A regression that sent a
// thinking:{"type":"disabled"} object would fail every call in prod.
func TestComplete_shallowLowersEffort(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, sseStream("news", ""))
	}))
	defer srv.Close()

	c := mustClient(t, Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Complete(Shallow(context.Background()), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := gotBody["thinking"]; ok {
		t.Fatalf("shallow sent a thinking object: %v", gotBody["thinking"])
	}
	if gotBody["reasoning_effort"] != lowReasoningEffort {
		t.Fatalf("reasoning_effort = %v, want %v", gotBody["reasoning_effort"], lowReasoningEffort)
	}

	// An explicit override still wins over Shallow.
	if _, err := c.Complete(WithReasoningEffort(Shallow(context.Background()), highReasoningEffort), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if gotBody["reasoning_effort"] != highReasoningEffort {
		t.Fatalf("reasoning_effort = %v, want the explicit override to win", gotBody["reasoning_effort"])
	}
}

// ShortGate routes to shortGateModel and nothing else does. Both consts hold the
// same id today, so this asserts against the consts rather than two literals:
// written with literals it would pass for the wrong reason now and silently stop
// testing anything if the deployments are ever split again.
func TestComplete_shortGateRoutesToTheGateDeployment(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, sseStream("news", ""))
	}))
	defer srv.Close()

	c := mustClient(t, Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Complete(ShortGate(context.Background()), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != shortGateModel {
		t.Fatalf("model = %v, want the gate deployment", gotBody["model"])
	}

	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != model {
		t.Fatalf("model = %v, want the default for a call that did not opt in", gotBody["model"])
	}
}

// The deployment and the reasoning depth are separate choices, and either can be
// made without the other: asking for shallow reasoning must not silently move a
// call onto the gate deployment.
func TestComplete_shallowDoesNotChangeTheDeployment(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, sseStream("news", ""))
	}))
	defer srv.Close()

	c := mustClient(t, Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Complete(Shallow(context.Background()), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if gotBody["model"] != model {
		t.Fatalf("model = %v, want the default: shallow reasoning is not the same as the gate deployment", gotBody["model"])
	}
}

func TestCompleteErrorsOnNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := mustClient(t, Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("expected error")
	}
}

func TestComplete_pacesRequestsByInterval(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, sseStream("ok", ""))
	}))
	defer srv.Close()

	c := mustClient(t, Config{BaseURL: srv.URL, RequestInterval: 100 * time.Millisecond}, srv.Client())
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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, sseStream("ok", ""))
	}))
	defer srv.Close()

	c := mustClient(t, Config{BaseURL: srv.URL}, srv.Client()) // RequestInterval 0
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

	c := mustClient(t, Config{BaseURL: srv.URL}, srv.Client())
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

	c := mustClient(t, Config{BaseURL: srv.URL}, srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if _, present := gotBody["max_tokens"]; present {
		t.Fatalf("max_tokens present by default: %v", gotBody["max_tokens"])
	}
	if gotBody["reasoning_effort"] != reasoningEffort {
		t.Fatalf("reasoning_effort = %v, want the package default", gotBody["reasoning_effort"])
	}
}

func TestComplete_failOnEarlyFinish(t *testing.T) {
	// content_filter under the flag → error, so a truncated answer is not
	// persisted (the summary call retries instead).
	cf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, sseFinish("partial", "content_filter"))
	}))
	defer cf.Close()
	c := mustClient(t, Config{BaseURL: cf.URL}, cf.Client())
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
	ln := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, sseFinish("partial", "length"))
	}))
	defer ln.Close()
	lc := mustClient(t, Config{BaseURL: ln.URL}, ln.Client())
	if out, err := lc.Complete(FailOnEarlyFinish(context.Background()), []Message{{Role: "user", Content: "hi"}}); err != nil || out != "partial" {
		t.Fatalf("length under flag: out=%q err=%v, want partial/nil", out, err)
	}
}

// ModelFor has to agree with what the request actually carries, because it
// exists to LABEL a call that has already happened. A second copy of the
// selection rule that drifted from modelFrom would put the wrong model name on
// the trace panel, and nothing downstream could tell.
func TestModelFor_namesWhatTheRequestCarries(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, sseStream("news", ""))
	}))
	defer srv.Close()
	c := mustClient(t, Config{BaseURL: srv.URL}, srv.Client())

	for _, tc := range []struct {
		name string
		ctx  context.Context
	}{
		{"short gate", ShortGate(context.Background())},
		{"default", context.Background()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Complete(tc.ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
				t.Fatal(err)
			}
			if got := ModelFor(tc.ctx); got != gotBody["model"] {
				t.Fatalf("ModelFor = %q, but the request carried %v", got, gotBody["model"])
			}
		})
	}
}

// JSON mode is opt-in and off by default: most calls here must not be
// constrained (classify answers with a bare id, the summary and Ask answers are
// prose), so a leak would be worse than the absence it replaces.
func TestComplete_jsonObjectIsOptIn(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		io.WriteString(w, sseStream("{}", ""))
	}))
	defer srv.Close()
	c := mustClient(t, Config{BaseURL: srv.URL}, srv.Client())

	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if _, present := gotBody["response_format"]; present {
		t.Fatalf("response_format sent without opting in: %v", gotBody["response_format"])
	}

	if _, err := c.Complete(AsJSONObject(context.Background()), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	// The literal is the assertion: this is the string the endpoint understands,
	// and it is rendered by llmwire now.
	rf, _ := gotBody["response_format"].(map[string]any)
	if rf == nil || rf["type"] != "json_object" {
		t.Fatalf("response_format = %v, want type %q", gotBody["response_format"], "json_object")
	}
}
