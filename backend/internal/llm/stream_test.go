package llm

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The SSE framing the fixtures build. These used to be package constants next to
// the scanner that consumed them; the scanner now lives in llmwire, so the
// fixtures own the strings they write.
const (
	dataPrefix = "data:"
	doneMarker = "[DONE]"

	// The bound names llmwire reports when it gives up. Pinned here as literals
	// on purpose, rather than imported: these strings are what an operator greps
	// for in a job log, so peeq's contract is the TEXT. Importing the constant
	// would let a rename upstream pass silently and quietly break every runbook
	// that mentions them.
	stallHeaders = "no response headers"
	stallIdle    = "stream idle"
)

// decodeJSON reads a captured request body, failing the test rather than the
// handler so an assertion error points at the test that made it.
func decodeJSON(t *testing.T, r *http.Request, v any) {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sseEvent frames one payload as the endpoint frames it: a data line and a
// blank separator.
//
// The payload is flattened first, because a raw newline inside it would end the
// data line early and split one event into two malformed ones. A Go test
// literal wrapped for readability does exactly that, and the resulting failure
// (usage silently unparsed, no error anywhere) points nowhere near the cause.
// Stripping newlines and tabs is safe for JSON, which cannot carry either
// unescaped inside a string.
func sseEvent(payload string) string {
	flat := strings.NewReplacer("\n", "", "\t", "").Replace(payload)
	// The space after "data:" is what the observed endpoint sends; the parser
	// accepts either, and the spaceless form has its own test below.
	return dataPrefix + " " + flat + "\n\n"
}

// sseStream renders the event sequence token-plan-sgp.xiaomimimo.com actually
// sends, verified on the wire and reproduced here so the fakes cannot be
// kinder than the endpoint: an empty opening delta carrying the role, the
// content, then a finish_reason chunk with "usage":null, then a separate usage
// chunk with an EMPTY choices array, then [DONE]. rawUsage empty omits that
// last usage chunk, standing in for an endpoint that reports nothing.
func sseStream(content, rawUsage string) string {
	var b strings.Builder
	b.WriteString(sseEvent(`{"choices":[{"delta":{"content":"","role":"assistant","reasoning_content":null},"finish_reason":null,"index":0}]}`))
	b.WriteString(sseEvent(`{"choices":[{"delta":{"content":` + strconv.Quote(content) + `,"role":null,"reasoning_content":null},"finish_reason":null,"index":0}]}`))
	b.WriteString(sseEvent(`{"choices":[{"delta":{"content":null,"role":null,"reasoning_content":null},"finish_reason":"stop","index":0}],"usage":null}`))
	if rawUsage != "" {
		b.WriteString(sseEvent(`{"choices":[],"usage":` + rawUsage + `}`))
	}
	b.WriteString(sseEvent(doneMarker))
	return b.String()
}

// flush writes s and pushes it to the client immediately. Without the flush the
// server buffers the whole body and nothing streams, which would let every
// timing test below pass for the wrong reason.
func flush(t *testing.T, w http.ResponseWriter, s string) {
	t.Helper()
	if _, err := w.Write([]byte(s)); err != nil {
		return
	}
	w.(http.Flusher).Flush()
}

// fastBounds keeps a test's timeouts in milliseconds. Callers override only the
// bound under test, so the other two cannot fire first and mislabel the result.
func fastBounds(cfg Config) Config {
	if cfg.HeaderTimeout == 0 {
		cfg.HeaderTimeout = 5 * time.Second
	}
	if cfg.StreamIdleTimeout == 0 {
		cfg.StreamIdleTimeout = 5 * time.Second
	}
	if cfg.CallTimeout == 0 {
		cfg.CallTimeout = 10 * time.Second
	}
	return cfg
}

func TestComplete_concatenatesContentDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, part := range []string{"Hello", ", ", "world"} {
			flush(t, w, sseEvent(`{"choices":[{"delta":{"content":`+strconv.Quote(part)+`},"finish_reason":null,"index":0}]}`))
		}
		flush(t, w, sseEvent(`{"choices":[{"delta":{},"finish_reason":"stop","index":0}]}`))
		flush(t, w, sseEvent(doneMarker))
	}))
	defer srv.Close()

	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client())
	got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Hello, world" {
		t.Fatalf("content = %q, want %q", got, "Hello, world")
	}
}

func TestComplete_streamsWithoutStreamOptions(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decodeJSON(t, r, &body)
		flush(t, w, sseStream("ok", ""))
	}))
	defer srv.Close()

	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if body["stream"] != true {
		t.Errorf("stream = %v, want true", body["stream"])
	}
	// MiMo needed stream_options.include_usage before it would send the trailing
	// usage chunk. Z.ai does not take the parameter and sends usage on the final
	// frame regardless, so the client must NOT send it — an undocumented field on
	// this endpoint. The accounting it used to buy is covered by the usage-chunk
	// tests below, which is what actually proves tokens are still counted.
	if _, present := body["stream_options"]; present {
		t.Errorf("stream_options = %v, want it absent", body["stream_options"])
	}
}

// The usage chunk arrives AFTER finish_reason and carries an empty choices
// array, while the finish_reason chunk carries "usage":null. Stopping at
// finish_reason loses the accounting; treating a present-but-null usage field
// as a report overwrites it with zeros. Both were live risks in this rewrite.
func TestComplete_keepsTheUsageChunkThatFollowsFinishReason(t *testing.T) {
	const raw = `{"prompt_tokens":265,"completion_tokens":80,"total_tokens":345,` +
		`"prompt_tokens_details":{"cached_tokens":192},` +
		`"completion_tokens_details":{"reasoning_tokens":0}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flush(t, w, sseStream("ok", raw))
	}))
	defer srv.Close()

	totals := &Totals{}
	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client())
	ctx := WithTotals(context.Background(), totals)
	if _, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	got := totals.Snapshot()
	got.InferenceNanos, got.PacedNanos = 0, 0
	// 73 uncached prompt tokens at $0.15/1M, 192 cached at $0.03, 80 output at
	// $0.50: 10_950 + 5_760 + 40_000. Exactly double what this test expected
	// before the migration, because peeq's own table came from a models.dev entry
	// last updated on the model's release day and never revisited, while Z.ai's
	// page has carried twice those figures since. The rate llmwire ships was read
	// off the vendor's page and carries its URL and the date it was read.
	want := Usage{Requests: 1, Accounted: 1, PromptTokens: 265, CachedTokens: 192, CompletionTokens: 80,
		TotalTokens: 345, CostNanoUSD: 56_710}
	if got != want {
		t.Fatalf("totals = %+v, want %+v", got, want)
	}
}

func TestComplete_skipsMalformedDataLinesRatherThanLosingTheAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"be"},"index":0}]}`))
		flush(t, w, sseEvent(`{not json at all`))
		flush(t, w, ": ping\n\n")
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"fore"},"finish_reason":"stop","index":0}]}`))
		flush(t, w, sseEvent(doneMarker))
	}))
	defer srv.Close()

	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client())
	got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("one bad line discarded a finished answer: %v", err)
	}
	if got != "before" {
		t.Fatalf("content = %q, want %q", got, "before")
	}
}

// Reasoning deltas are the liveness signal a long thinking phase produces, and
// they must not reach the caller as output.
func TestComplete_countsReasoningDeltasButExcludesThemFromTheResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, part := range []string{"The user", " wants", " a count"} {
			flush(t, w, sseEvent(`{"choices":[{"delta":{"content":null,"reasoning_content":`+strconv.Quote(part)+`},"index":0}]}`))
		}
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"1"},"finish_reason":"stop","index":0}]}`))
		flush(t, w, sseEvent(doneMarker))
	}))
	defer srv.Close()

	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client())
	got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "1" {
		t.Fatalf("content = %q, want %q — reasoning must not leak into the answer", got, "1")
	}
}

// The three bounds below are the point of the change: each failure must name
// itself, because the bug that prompted this rewrite was a log line that could
// not say which of them had happened.

func TestComplete_namesTheHeaderBoundWhenNothingArrives(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // never writes, so no headers are ever sent
	}))
	defer srv.Close()
	defer close(release)

	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger(), HeaderTimeout: 80 * time.Millisecond}), srv.Client())
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), stallHeaders) {
		t.Fatalf("err = %v, want it to name %q", err, stallHeaders)
	}
}

func TestComplete_namesTheIdleBoundAndHowFarItGot(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Headers and two events arrive, then the socket goes silent — the shape
		// of the stall that motivated all of this.
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"partial"},"index":0}]}`))
		flush(t, w, sseEvent(`{"choices":[{"delta":{"reasoning_content":"hmm"},"index":0}]}`))
		<-release
	}))
	defer srv.Close()
	defer close(release)

	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger(), StreamIdleTimeout: 80 * time.Millisecond}), srv.Client())
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), stallIdle) {
		t.Fatalf("err = %v, want it to name %q", err, stallIdle)
	}
	// A partial answer must not be returned as if it were the whole one.
	if strings.Contains(err.Error(), stallHeaders) {
		t.Errorf("err blames the wrong bound: %v", err)
	}
}

// A DELIBERATE CHANGE OF POLICY, and the one behavioural difference this
// migration makes on a healthy stream.
//
// This package used to re-arm the idle bound on every line, keepalives included.
// llmwire re-arms on `data:` frames only, because a comment proves the socket is
// alive and says nothing about the model making progress — so an upstream
// emitting `: ping` on a timer could hold a stalled model open until the
// 15-minute call cap. loom took the opposite view and re-armed on nothing;
// llmwire merged the two, and measured the case before choosing: the longest
// comment-only gap either endpoint produced was 1.396s against a 90s bound.
//
// What peeq gives up is tolerance for an endpoint that goes quiet for longer
// than StreamIdleTimeout while sending only comments. What it gains is a stalled
// model failing in 90 seconds rather than 15 minutes. Reasoning deltas are
// `data:` frames, so the long silent think this endpoint is known for still
// re-arms the bound.
func TestComplete_keepalivesDoNotHoldOffTheIdleBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Comments only, well past the idle bound. Under the old policy this
		// answered "survived"; now the bound fires.
		for i := 0; i < 10; i++ {
			flush(t, w, ": ping\n\n")
			time.Sleep(20 * time.Millisecond)
		}
		flush(t, w, sseStream("survived", ""))
	}))
	defer srv.Close()

	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger(), StreamIdleTimeout: 80 * time.Millisecond}), srv.Client())
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("want the idle bound to fire: comments are not progress")
	}
	if !strings.Contains(err.Error(), stallIdle) {
		t.Fatalf("err = %v, want it to name %q", err, stallIdle)
	}
}

// The other half of that rule, and the reason it is safe: a reasoning delta IS a
// data frame, so a model that thinks for a long time before saying anything
// keeps the bound re-armed and is not mistaken for a stalled one.
func TestComplete_reasoningDeltasHoldOffTheIdleBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 10; i++ {
			flush(t, w, sseEvent(`{"choices":[{"delta":{"reasoning_content":"thinking"},"index":0}]}`))
			time.Sleep(20 * time.Millisecond)
		}
		flush(t, w, sseStream("survived", ""))
	}))
	defer srv.Close()

	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger(), StreamIdleTimeout: 150 * time.Millisecond}), srv.Client())
	got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("a thinking model was treated as a stalled one: %v", err)
	}
	if got != "survived" {
		t.Fatalf("content = %q, want %q", got, "survived")
	}
}

func TestComplete_namesTheCallCapWhenAStreamNeverFinishes(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		for {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			flush(t, w, ": ping\n\n")
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer srv.Close()

	// Idle is deliberately far larger than the cap, so only the cap can fire.
	c := NewClient(fastBounds(Config{
		BaseURL: srv.URL, Logger: discardLogger(),
		StreamIdleTimeout: 5 * time.Second, CallTimeout: 150 * time.Millisecond,
	}), srv.Client())
	_, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "call cap") {
		t.Fatalf("err = %v, want it to name the call cap", err)
	}
	<-done
}

// A caller cancelling must not be reported as one of our bounds — that would
// blame the endpoint for a shutdown.
func TestComplete_parentCancellationIsNotBlamedOnABound(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"x"},"index":0}]}`))
		<-release
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(60 * time.Millisecond)
		cancel()
	}()
	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client())
	_, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, bound := range []string{stallHeaders, stallIdle, "call cap"} {
		if strings.Contains(err.Error(), bound) {
			t.Errorf("caller cancellation reported as %q: %v", bound, err)
		}
	}
}

func TestComplete_heartbeatReportsWhatHasArrived(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"abcde"},"index":0}]}`))
		<-release
		flush(t, w, sseEvent(`{"choices":[{"delta":{},"finish_reason":"stop","index":0}]}`))
		flush(t, w, sseEvent(doneMarker))
	}))
	defer srv.Close()

	log, buf := capture()
	c := NewClient(fastBounds(Config{
		BaseURL: srv.URL, Logger: log, HeartbeatInterval: 20 * time.Millisecond,
	}), srv.Client())

	go func() {
		time.Sleep(120 * time.Millisecond)
		close(release)
	}()
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}

	beat := find(buf.records(t), "llm: still waiting for response")
	if beat == nil {
		t.Fatal("no heartbeat record")
	}
	// chunks=0 is a dead socket and chunks>0 is a working one; the whole reason
	// for the per-tick provider is that the old line said neither.
	chunks, ok := beat["chunks"].(float64)
	if !ok || chunks < 1 {
		t.Fatalf("heartbeat did not report progress: %v", beat)
	}
	if chars, _ := beat["chars"].(float64); chars != 5 {
		t.Errorf("chars = %v, want 5", beat["chars"])
	}
}

// The failure line must describe the call that failed. Before streaming it
// carried only a duration, so the only numbers near it were the chat_* totals —
// which cover the calls that SUCCEEDED. That mismatch is what made a 300s stall
// read as a 6.5s request in the incident this change came from.
func TestComplete_failureLineCarriesItsOwnCounts(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"abc"},"index":0}]}`))
		<-release
	}))
	defer srv.Close()
	defer close(release)

	log, buf := capture()
	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: log, StreamIdleTimeout: 80 * time.Millisecond}), srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err == nil {
		t.Fatal("want an error")
	}

	failed := find(buf.records(t), "llm: request failed")
	if failed == nil {
		t.Fatal("no failure record")
	}
	// Exactly one: the handler sent one data event, and its blank separator is
	// not a second chunk.
	if chunks, _ := failed["chunks"].(float64); chunks != 1 {
		t.Errorf("chunks = %v, want 1", failed["chunks"])
	}
	if chars, _ := failed["chars"].(float64); chars != 3 {
		t.Errorf("chars = %v, want 3", failed["chars"])
	}
}

// SSE allows the space after "data:" to be omitted. Matching only the spaced
// form would drop every event from such an endpoint as if it were a comment —
// silently, with no error to point at.
//
// The scanner is llmwire's now, so this asserts the property end to end rather
// than calling the parser: what matters to peeq is that such an endpoint still
// produces an answer.
func TestComplete_acceptsDataLinesWithoutTheSpace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, dataPrefix+`{"choices":[{"delta":{"content":"tight"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = io.WriteString(w, dataPrefix+doneMarker+"\n\n")
	}))
	defer srv.Close()

	got, err := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client()).
		Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "tight" {
		t.Errorf("content = %q, want %q", got, "tight")
	}
}

// The character count the heartbeat and the failure line report is in RUNES.
// This endpoint returns non-ASCII routinely, and a byte count reads as more
// output than the model produced — the kind of number nobody can reconcile
// later. Asserted through the failure line, which is where the count surfaces.
func TestComplete_countsRunesNotBytes(t *testing.T) {
	// Four runes, ten bytes.
	const answer = "héllo…"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// No finish_reason and no [DONE]: the stream just stops, which is the
		// failure that makes the counts visible.
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"`+answer+`"}}]}`))
	}))
	defer srv.Close()

	log, buf := capture()
	_, err := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: log}), srv.Client()).
		Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("a stream that stops without finishing must be an error")
	}
	rec := find(buf.records(t), "llm: request failed")
	if rec == nil {
		t.Fatal("no failure line")
	}
	if got := rec["chars"]; got != float64(len([]rune(answer))) {
		t.Errorf("chars = %v, want %d runes (not %d bytes)", got, len([]rune(answer)), len(answer))
	}
}

// A stream that simply stops is an error, not a short answer: a dropped
// connection ends the scan exactly like a finished one, so accepting what
// arrived would persist a truncated summary as a complete one.
func TestComplete_reportsAStreamThatEndsWithNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush(t, w, ": ping\n\n")
	}))
	defer srv.Close()

	_, err := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client()).
		Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "without finish_reason") {
		t.Errorf("err = %v, want it to name the missing completion", err)
	}
}

// Deltas reach the callback in order and whole: a caller relaying to a browser
// and a caller buffering must see exactly the same text.
func TestCompleteStream_deliversDeltasInOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"one "}}]}`))
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"two"},"finish_reason":"stop"}]}`))
		flush(t, w, sseEvent(doneMarker))
	}))
	defer srv.Close()

	var got []string
	whole, err := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client()).
		CompleteStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, func(d string) {
			got = append(got, d)
		})
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if strings.Join(got, "") != whole {
		t.Errorf("streamed %q but returned %q; they must agree", strings.Join(got, ""), whole)
	}
	if len(got) != 2 {
		t.Errorf("got %d fragments, want 2", len(got))
	}
}

// A nil callback is the Complete path, and must not panic on its way through
// the same code.
func TestCompleteStream_nilCallbackIsTheCompletePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"quiet"},"finish_reason":"stop"}]}`))
		flush(t, w, sseEvent(doneMarker))
	}))
	defer srv.Close()

	got, err := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client()).
		CompleteStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if got != "quiet" {
		t.Errorf("content = %q", got)
	}
}

// The nil-client path is what production actually uses: cmd/peeq passes nil, so
// every real request runs through the transport built here. Left untested, a
// whole-request timeout could reappear in it and silently cut streams again —
// the exact failure this package was rewritten to remove.
func TestNewClient_defaultTransportBoundsHeadersAndNotTheWholeRequest(t *testing.T) {
	c := NewClient(Config{BaseURL: "http://example.invalid/v1", HeaderTimeout: 7 * time.Second}, nil)

	if c.http.Timeout != 0 {
		t.Errorf("whole-request timeout = %v, want none: it caps body reads and cuts streams", c.http.Timeout)
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", c.http.Transport)
	}
	// Deliberately LATER than the bound llmwire enforces, so the named failure
	// wins the race against this generic one. Equal values made a transport win
	// report "timeout awaiting response headers" instead of "no response headers
	// within 7s".
	if want := 7*time.Second + headerBackstopHeadroom; tr.ResponseHeaderTimeout != want {
		t.Errorf("ResponseHeaderTimeout = %v, want %v (the configured bound plus headroom)",
			tr.ResponseHeaderTimeout, want)
	}
	// Cloned from the stdlib default rather than built bare, so proxy support
	// and dial timeouts survive.
	if tr.Proxy == nil {
		t.Error("transport lost proxy support")
	}
}

// A finish_reason other than "stop" means the endpoint ended the answer on its
// own terms. Not an error — retrying an answer the model chose to cut would just
// cut it again — but not silent either: a half summary nobody can explain later
// is worse than a warning nobody reads.
func TestComplete_warnsWhenTheAnswerEndedEarly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"cut off mid-"},"finish_reason":"length","index":0}]}`))
		flush(t, w, sseEvent(doneMarker))
	}))
	defer srv.Close()

	log, buf := capture()
	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: log}), srv.Client())
	got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("a length-limited answer must not be an error: %v", err)
	}
	if got != "cut off mid-" {
		t.Fatalf("content = %q", got)
	}
	rec := find(buf.records(t), "llm: answer ended early")
	if rec == nil {
		t.Fatal("a cut-short answer was accepted silently")
	}
	if rec["finish_reason"] != "length" || rec["level"] != "WARN" {
		t.Errorf("warning record = %v", rec)
	}
}

// A "stop" finish is the normal case and must stay quiet, or the warning above
// becomes noise nobody reads.
func TestComplete_doesNotWarnOnANormalFinish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flush(t, w, sseStream("fine", ""))
	}))
	defer srv.Close()

	log, buf := capture()
	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: log}), srv.Client())
	if _, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	if rec := find(buf.records(t), "llm: answer ended early"); rec != nil {
		t.Errorf("warned about a normal finish: %v", rec)
	}
}

// Every refusal an operator greps for renders in this package's own phrasing.
//
// 429 is the one that nearly got away: llmwire returns it as *RateLimitError,
// which embeds *APIError but declares no Unwrap, so a type check against
// *APIError alone silently misses exactly the status the summarize worker's
// retry path cares about most.
func TestComplete_statusErrorsKeepTheirPhrasing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"429", 429, `{"error":{"message":"slow down","code":"1302"}}`, "chat failed with status 429: slow down"},
		{"500", 500, `{"error":{"message":"boom","code":"1000"}}`, "chat failed with status 500: boom"},
		{"401", 401, `{"error":{"message":"bad key","code":"401"}}`, "chat failed with status 401: bad key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			_, err := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client()).
				Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
			if err == nil {
				t.Fatal("expected an error")
			}
			if err.Error() != tc.want {
				t.Errorf("err = %q, want %q", err, tc.want)
			}
		})
	}
}

// A failure delivered INSIDE a 200 — an error frame mid-stream — has no HTTP
// status to report. It must name the endpoint's own code rather than print
// "status 0", and must not be mistaken for a stream that merely stopped.
func TestComplete_midStreamErrorFrameNamesTheCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flush(t, w, sseEvent(`{"choices":[{"delta":{"content":"half"},"index":0}]}`))
		flush(t, w, sseEvent(`{"error":{"message":"upstream gave up","code":"1210"}}`))
	}))
	defer srv.Close()

	_, err := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger()}), srv.Client()).
		Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err == nil {
		t.Fatal("an error frame inside a 200 must not read as a finished answer")
	}
	if !strings.Contains(err.Error(), "upstream gave up") || !strings.Contains(err.Error(), "1210") {
		t.Errorf("err = %v, want the endpoint's own message and code", err)
	}
	if strings.Contains(err.Error(), "status 0") {
		t.Errorf("err invents an HTTP status: %v", err)
	}
}
