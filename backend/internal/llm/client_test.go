package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/trick77/llmwire"
	"github.com/trick77/llmwire/llmwiretest"
)

// The tests here assert what peeq ASKS for — which model, which reasoning
// intent, how long an answer may be — against llmwiretest's synthetic models.
// How a real model spells those on the wire, and what level an intent becomes
// for it, is llmwire's to know and test.

// fakeClient is a Client on an llmwiretest fake, recording every request.
func fakeClient(t *testing.T, cfg Config) (*Client, *llmwiretest.Server) {
	t.Helper()
	srv := llmwiretest.NewServer(t)
	cfg.BaseURL, cfg.APIKey = srv.URL, "k"
	if cfg.Logger == nil {
		cfg.Logger = discardLogger()
	}
	return mustClient(t, cfg, srv.Server.Client()), srv
}

func complete(ctx context.Context, t *testing.T, c *Client) string {
	t.Helper()
	out, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestComplete_sendsTheConfiguredModelAndReturnsContent(t *testing.T) {
	c, srv := fakeClient(t, Config{})
	if out := complete(context.Background(), t, c); out != llmwiretest.Reply {
		t.Fatalf("content = %q, want %q", out, llmwiretest.Reply)
	}
	last := srv.Last()
	if last.Path != "/chat/completions" {
		t.Errorf("path = %s", last.Path)
	}
	if got := last.Header.Get("Authorization"); got != "Bearer k" {
		t.Errorf("Authorization = %q", got)
	}
	if last.Model() != llmwiretest.ChatModel {
		t.Errorf("model = %q, want the configured %q", last.Model(), llmwiretest.ChatModel)
	}
	if !last.Stream() {
		t.Error("request did not stream")
	}
}

// A call that asks for nothing sends no reasoning knob at all, so the model
// runs at its own default. That is the offline lane's deliberate choice: the
// vendor's default is what the model was tuned for.
func TestComplete_defaultReasoningSendsNothing(t *testing.T) {
	c, srv := fakeClient(t, Config{})
	complete(context.Background(), t, c)
	if got := srv.Last().Reasoning(); got != "" {
		t.Fatalf("reasoning = %q, want none sent (the model's default)", got)
	}
	if ReasoningFor(context.Background()) != ReasoningDefault {
		t.Errorf("ReasoningFor(unset) = %q, want %q", ReasoningFor(context.Background()), ReasoningDefault)
	}
}

func TestComplete_reasoningIntentsReachTheWire(t *testing.T) {
	c, srv := fakeClient(t, Config{})
	for _, tc := range []struct {
		intent Reasoning
		want   string
	}{
		{ReasoningMinimal, llmwiretest.MinimalSent},
		{ReasoningBalanced, llmwiretest.BalancedSent},
	} {
		ctx := WithReasoning(context.Background(), tc.intent)
		if ReasoningFor(ctx) != tc.intent {
			t.Errorf("ReasoningFor = %q, want %q", ReasoningFor(ctx), tc.intent)
		}
		complete(ctx, t, c)
		if got := srv.Last().Reasoning(); got != tc.want {
			t.Errorf("%s: reasoning sent = %q, want %q", tc.intent, got, tc.want)
		}
	}
}

// The cap a call sets is for its ANSWER; the reasoning allowance on top is the
// model profile's, added by llmwire for the level the intent resolved to.
func TestComplete_maxAnswerTokensAddsTheReasoningAllowance(t *testing.T) {
	c, srv := fakeClient(t, Config{})
	const n = 300
	for _, tc := range []struct {
		intent Reasoning
		want   int
	}{
		{ReasoningDefault, n + llmwire.DefaultReasoningOverhead},
		{ReasoningMinimal, n + llmwiretest.MinimalOverhead},
		{ReasoningBalanced, n + llmwiretest.BalancedOverhead},
	} {
		complete(WithMaxAnswerTokens(WithReasoning(context.Background(), tc.intent), n), t, c)
		if got, ok := srv.Last().MaxTokens(); !ok || got != tc.want {
			t.Errorf("%s: wire cap = %d (sent %v), want %d", tc.intent, got, ok, tc.want)
		}
	}
}

// Absent a cap, none is sent: the endpoint's own limit applies.
func TestComplete_noCapByDefault(t *testing.T) {
	c, srv := fakeClient(t, Config{})
	complete(context.Background(), t, c)
	if got, ok := srv.Last().MaxTokens(); ok {
		t.Fatalf("a cap was sent by default: %d", got)
	}
}

// ShortGate routes to the gate model and nothing else does; the reasoning
// intent is a separate choice and never moves a call.
func TestComplete_shortGateRoutesToTheGateModel(t *testing.T) {
	c, srv := fakeClient(t, Config{GateModel: llmwiretest.BudgetModel})
	complete(ShortGate(context.Background()), t, c)
	if got := srv.Last().Model(); got != llmwiretest.BudgetModel {
		t.Fatalf("gate call reached %q, want the gate model %q", got, llmwiretest.BudgetModel)
	}
	complete(WithReasoning(context.Background(), ReasoningMinimal), t, c)
	if got := srv.Last().Model(); got != llmwiretest.ChatModel {
		t.Fatalf("minimal reasoning moved the call to %q; only ShortGate picks the gate model", got)
	}
}

// Left unset, the gate model is the chat model.
func TestComplete_gateModelDefaultsToTheChatModel(t *testing.T) {
	c, srv := fakeClient(t, Config{})
	complete(ShortGate(context.Background()), t, c)
	if got := srv.Last().Model(); got != llmwiretest.ChatModel {
		t.Fatalf("gate call reached %q, want the chat model", got)
	}
}

// ModelFor labels a call that has already happened, so it has to agree with
// what the request carried.
func TestModelFor_namesWhatTheRequestCarries(t *testing.T) {
	c, srv := fakeClient(t, Config{GateModel: llmwiretest.BudgetModel})
	for _, ctx := range []context.Context{ShortGate(context.Background()), context.Background()} {
		complete(ctx, t, c)
		if got := c.ModelFor(ctx); got != srv.Last().Model() {
			t.Fatalf("ModelFor = %q, but the request carried %q", got, srv.Last().Model())
		}
	}
}

// Every model fact is llmwire's, so a model that cannot do what peeq asks of
// it is refused at construction, with the choices that would work.
func TestNewClient_refusesAModelThatCannotFillTheRole(t *testing.T) {
	reg := llmwiretest.Registry()
	for _, tc := range []struct {
		name string
		cfg  Config
		want []string
	}{
		{"no chat model", Config{}, []string{"BACKEND_CHAT_MODEL", llmwiretest.ChatModel}},
		{"unknown chat model", Config{Model: "no-such-model"}, []string{"BACKEND_CHAT_MODEL", "no-such-model"}},
		// The budget model takes no json_object, which the key-points call needs.
		{"chat model lacks json", Config{Model: llmwiretest.BudgetModel}, []string{"BACKEND_CHAT_MODEL", "json_object", llmwiretest.ChatModel}},
		{"unknown gate model", Config{Model: llmwiretest.ChatModel, GateModel: "no-such-model"}, []string{"BACKEND_GATE_MODEL", "no-such-model"}},
		{"embeddings as chat", Config{Model: llmwiretest.EmbedModel}, []string{"BACKEND_CHAT_MODEL", "not chat"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.cfg.Registry, tc.cfg.BaseURL, tc.cfg.APIKey = reg, "http://127.0.0.1:1", "k"
			_, err := NewClient(tc.cfg, nil)
			if err == nil {
				t.Fatal("NewClient accepted it")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err = %q, want it to mention %q", err, w)
				}
			}
		})
	}
}

// With no BaseURL the host comes from the model's profile and the key from the
// variable llmwire names for it; a missing one comes back named.
func TestNewClient_withoutBaseURLNamesTheMissingVariable(t *testing.T) {
	t.Setenv("LLMWIRE_LLMWIRETEST_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("LLMWIRE_LLMWIRETEST_API_KEY", "")
	_, err := NewClient(Config{Model: llmwiretest.ChatModel, Registry: llmwiretest.Registry()}, nil)
	var me *llmwire.MissingEnvError
	if !errors.As(err, &me) || !strings.Contains(err.Error(), "LLMWIRE_LLMWIRETEST_API_KEY") {
		t.Fatalf("got %v, want a MissingEnvError naming the key variable", err)
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

	// length is tolerated even under the flag: that cut is our own cap, and
	// retrying would just re-truncate.
	ln := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, sseFinish("partial", "length"))
	}))
	defer ln.Close()
	lc := mustClient(t, Config{BaseURL: ln.URL}, ln.Client())
	if out, err := lc.Complete(FailOnEarlyFinish(context.Background()), []Message{{Role: "user", Content: "hi"}}); err != nil || out != "partial" {
		t.Fatalf("length under flag: out=%q err=%v, want partial/nil", out, err)
	}
}

// JSON mode is opt-in and off by default: most calls here must not be
// constrained (classify answers with a bare id, the summary and Ask answers are
// prose), so a leak would be worse than the absence it replaces.
func TestComplete_jsonObjectIsOptIn(t *testing.T) {
	c, srv := fakeClient(t, Config{})
	complete(context.Background(), t, c)
	if rf, present := srv.Last().Body["response_format"]; present {
		t.Fatalf("response_format sent without opting in: %v", rf)
	}
	complete(AsJSONObject(context.Background()), t, c)
	rf, _ := srv.Last().Body["response_format"].(map[string]any)
	if rf == nil || rf["type"] != "json_object" {
		t.Fatalf("response_format = %v, want type %q", srv.Last().Body["response_format"], "json_object")
	}
}
