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
	"sync/atomic"
	"testing"
	"time"
)

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

func TestComplete_sendsStreamAndAsksForUsage(t *testing.T) {
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
	// Without include_usage a streamed call reports no tokens at all, so this is
	// the difference between accounting that works and accounting gone dark.
	opts, _ := body["stream_options"].(map[string]any)
	if opts == nil || opts["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage true", body["stream_options"])
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
	ctx := WithCall(context.Background(), CallInfo{Totals: totals})
	if _, err := c.Complete(ctx, []Message{{Role: "user", Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	got := totals.Snapshot()
	got.InferenceNanos, got.PacedNanos = 0, 0
	want := Usage{Requests: 1, Accounted: 1, PromptTokens: 265, CachedTokens: 192, CompletionTokens: 80, TotalTokens: 345}
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

// Keepalives prove the socket is alive without making progress. They must hold
// the idle bound off — otherwise a quiet-but-healthy stream dies — and the
// overall cap must then be what stops an endpoint that never finishes.
func TestComplete_keepalivesHoldOffTheIdleBound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ten keepalives at 20ms span 200ms, well past the 150ms idle bound,
		// while each individual gap stays far enough below it that a scheduling
		// hiccup on a loaded CI machine cannot fail this by itself.
		for i := 0; i < 10; i++ {
			flush(t, w, ": ping\n\n")
			time.Sleep(20 * time.Millisecond)
		}
		flush(t, w, sseStream("survived", ""))
	}))
	defer srv.Close()

	c := NewClient(fastBounds(Config{BaseURL: srv.URL, Logger: discardLogger(), StreamIdleTimeout: 150 * time.Millisecond}), srv.Client())
	got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("keepalives did not re-arm the idle bound: %v", err)
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
func TestReadStream_acceptsDataLinesWithoutTheSpace(t *testing.T) {
	var counters streamCounters
	guard := newStallGuard(func() {}, time.Hour, stallIdle)
	defer guard.stop()

	body := `data:{"choices":[{"delta":{"content":"tight"},"finish_reason":"stop","index":0}]}` + "\n\n" +
		"data:[DONE]\n\n"
	res, err := readStream(strings.NewReader(body), guard, &counters, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.content != "tight" {
		t.Fatalf("content = %q, want %q", res.content, "tight")
	}
}

// chars counts runes, so non-ASCII output is not reported as more text than the
// model produced.
func TestReadStream_countsRunesNotBytes(t *testing.T) {
	var counters streamCounters
	guard := newStallGuard(func() {}, time.Hour, stallIdle)
	defer guard.stop()

	// Five runes, ten bytes in UTF-8.
	body := `data: {"choices":[{"delta":{"content":"héllö"},"finish_reason":"stop","index":0}]}` + "\n\n" +
		"data: [DONE]\n\n"
	res, err := readStream(strings.NewReader(body), guard, &counters, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if res.chars != 5 {
		t.Fatalf("chars = %d, want 5 (runes, not bytes)", res.chars)
	}
}

func TestReadStream_reportsAStreamThatEndsWithNothing(t *testing.T) {
	// An endpoint that closes the connection with neither output nor a
	// finish_reason has not answered, and must not look like an empty summary.
	var counters streamCounters
	guard := newStallGuard(func() {}, time.Hour, stallIdle)
	defer guard.stop()

	_, err := readStream(strings.NewReader(": ping\n\n"), guard, &counters, time.Hour)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "without finish_reason") {
		t.Fatalf("err = %v", err)
	}
}

func TestStallGuard_firesOnceAndRemembersTheArmedReason(t *testing.T) {
	// Atomic because the guard calls cancel from the timer goroutine while the
	// test reads the count.
	var fired atomic.Int64
	g := newStallGuard(func() { fired.Add(1) }, time.Hour, stallHeaders)
	g.arm(10*time.Millisecond, stallIdle)

	// Wait on the CANCEL, not on the reason. fire() publishes the reason under
	// the mutex but calls cancel() after unlocking — deliberately, so the
	// caller's function never runs under the guard's lock — which leaves a
	// window where firedReason() already answers and the counter is still 0.
	// Polling the reason and then asserting the count raced that window and
	// failed with "cancel called 0 times". Once the count is 1 the reason is
	// necessarily published too, so this order has no window at all.
	deadline := time.Now().Add(2 * time.Second)
	for fired.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("cancel called %d times, want 1", got)
	}
	if got := g.firedReason(); got != stallIdle {
		t.Fatalf("reason = %q, want %q", got, stallIdle)
	}
	// Re-arming after the fact must not resurrect a call already being
	// cancelled, nor overwrite the reason the log is about to report.
	g.arm(time.Hour, stallHeaders)
	if got := g.firedReason(); got != stallIdle {
		t.Fatalf("reason after late arm = %q, want %q", got, stallIdle)
	}
	g.stop()
	// Still exactly one: neither the late arm nor stop() may fire it again.
	if got := fired.Load(); got != 1 {
		t.Fatalf("cancel called %d times after late arm, want 1", got)
	}
}

// The stale-firing window: an event lands after the timer has expired and its
// callback is already scheduled, but before that callback takes the mutex.
// time.Reset is powerless against an in-flight AfterFunc callback, so without
// the deadline check in fire() this cancels a stream that had just revived —
// and labels it with the reason arm() just wrote rather than the bound that
// elapsed. Driving fire() directly is what makes the window reachable at all;
// by wall-clock it is microseconds wide.
func TestStallGuard_ignoresAFiringOvertakenByAnEvent(t *testing.T) {
	var fired atomic.Int64
	g := newStallGuard(func() { fired.Add(1) }, time.Hour, stallHeaders)
	defer g.stop()

	// Expire the header deadline WITHOUT letting the runtime schedule a firing
	// of its own. Writing the fields directly is the point: `arm(-time.Second,
	// …)` would call timer.Reset with a negative duration, so the runtime runs
	// the real callback at once, on its own goroutine, racing everything below.
	// When it won that race — landing before the re-arm on the next line — it
	// fired legitimately (the deadline really had passed) and recorded
	// stallHeaders, and the assertion then blamed the code under test for the
	// test's own timer. That is the flake that reddened Backend CI on PRs
	// touching nothing near this package.
	//
	// This test drives fire() by hand precisely because the window is
	// microseconds wide by wall-clock; the timer must stay out of it. It keeps
	// its original one-hour arming throughout and never fires on its own.
	g.mu.Lock()
	g.pending = stallHeaders
	g.deadline = time.Now().Add(-time.Second)
	g.mu.Unlock()

	// The event lands and re-arms for idleness — the order a real stream
	// produces when its first byte arrives right on the bound.
	g.arm(time.Hour, stallIdle)

	// The firing the expired deadline had already scheduled now runs.
	g.fire()

	if got := g.firedReason(); got != "" {
		t.Fatalf("stale firing cancelled a revived stream, blaming %q", got)
	}
	if got := fired.Load(); got != 0 {
		t.Fatalf("cancel called %d times, want 0", got)
	}
}

// A second firing must not cancel twice. The timer is re-armed by fire() on the
// stale path, so a real deadline followed by that rescheduled callback is an
// ordinary sequence, not a contrived one.
func TestStallGuard_secondFiringIsANoOp(t *testing.T) {
	var fired atomic.Int64
	g := newStallGuard(func() { fired.Add(1) }, time.Hour, stallHeaders)
	defer g.stop()

	g.arm(-time.Second, stallIdle)
	g.fire()
	g.fire()

	if got := fired.Load(); got != 1 {
		t.Fatalf("cancel called %d times, want 1", got)
	}
	if got := g.firedReason(); got != stallIdle {
		t.Fatalf("reason = %q, want %q", got, stallIdle)
	}
}

// A model that stops because it hit a token limit has produced a partial
// answer. That is not retried — retrying truncates again — so the only defence
// against an unexplainable half summary later is that it was logged.
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
		t.Fatal("a truncated answer was accepted silently")
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

// The nil-client path is what production actually uses: cmd/peeq passes nil, so
// every real request runs through the transport built here. Left untested, a
// whole-request timeout could reappear in it and silently truncate streams
// again — the exact failure this package was rewritten to remove.
func TestNewClient_defaultTransportBoundsHeadersAndNotTheWholeRequest(t *testing.T) {
	c := NewClient(Config{BaseURL: "http://example.invalid/v1", HeaderTimeout: 7 * time.Second}, nil)

	if c.http.Timeout != 0 {
		t.Errorf("whole-request timeout = %v, want none: it caps body reads and truncates streams", c.http.Timeout)
	}
	tr, ok := c.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", c.http.Transport)
	}
	if tr.ResponseHeaderTimeout != 7*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want the configured 7s", tr.ResponseHeaderTimeout)
	}
	// Cloned from the stdlib default rather than built bare, so proxy support
	// and dial timeouts survive.
	if tr.Proxy == nil {
		t.Error("transport lost proxy support")
	}
}

// The mirror case: nothing revived it, so the firing is real and must report
// the bound whose deadline actually elapsed.
func TestStallGuard_firesWhenTheDeadlineTrulyPassed(t *testing.T) {
	var fired atomic.Int64
	g := newStallGuard(func() { fired.Add(1) }, time.Hour, stallHeaders)
	defer g.stop()

	g.arm(-time.Second, stallIdle)
	g.fire()

	if got := g.firedReason(); got != stallIdle {
		t.Fatalf("reason = %q, want %q", got, stallIdle)
	}
	if got := fired.Load(); got != 1 {
		t.Fatalf("cancel called %d times, want 1", got)
	}
}

// decodeJSON reads a request body into v, matching the io.ReadAll + Unmarshal
// pattern the older tests in this package already use.
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

// discardLogger is for the tests that assert on behaviour rather than output.
// The client logs at debug on every call, and dumping that into the test
// output buries the failures that matter.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
