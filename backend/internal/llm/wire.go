package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/trick77/llmwire"
)

// The seam onto llmwire.
//
// What moved out of this package: the SSE scanner, the three-bound stall guard
// that named which bound fired, the request struct, and the rate table. All four
// existed in near-identical form in five backends, and every model quirk learned
// here had to be relearned there — which is what llmwire exists to stop.
//
// What deliberately stayed: pacing, the heartbeat, the CallInfo/Totals
// accounting, the context knobs, the per-video session headers, and every log
// line. Those are peeq's policy, not wire protocol, and a library that owned
// them would be a framework.
//
// The usage seam is the important one: llmwire hands back the endpoint's own
// usage bytes verbatim in Usage.Raw, so chatUsage, usageFrom and Accounted are
// unchanged and still answer "did this endpoint report a zero, or report
// nothing". Decoding twice is deliberate — the alternative is trusting a second
// mapping to preserve a distinction this package's logging is built on.

// streamCounters are the live counts the heartbeat reads while the stream is
// still being consumed. Atomic because the consuming goroutine writes them as
// the heartbeat goroutine reads them — the same discipline heartbeat.go
// documents.
type streamCounters struct {
	events atomic.Int64
	chars  atomic.Int64
}

// attrs renders the counters for a heartbeat tick. chunks=0 after a minute is a
// dead socket; chunks climbing with chars flat is a model still reasoning.
func (s *streamCounters) attrs() []any {
	return []any{"chunks", s.events.Load(), "chars", s.chars.Load()}
}

// wireResult is one completed call, in the shape the logging and accounting
// below already expect.
type wireResult struct {
	content      string
	finishReason string
	// rawUsage is the endpoint's own usage object, verbatim. Fed straight into
	// usageFrom, so "reported a zero" stays distinguishable from "reported
	// nothing".
	rawUsage json.RawMessage
	// costNanoUSD is priced by llmwire from its embedded table, with the model
	// that actually ran.
	costNanoUSD int64
	// costPriced says a rate was found. False means unknown, never free.
	costPriced bool
	events     int64
	chars      int64
	warnings   []llmwire.Warning
}

// chatRequestFor maps peeq's context knobs onto an llmwire request.
//
// Everything the model needs that llmwire models is a field; the one thing it
// does not model is Z.ai's thinking object, which rides in ExtraBody. That is
// not a workaround: llmwire's profile for this model says reasoning is
// controlled by reasoning_effort, which is true, and Z.ai accepts the extra
// object alongside it. Sending it keeps the wire byte-identical to what this
// deployment has been answering for months.
//
// temperature and top_p are deliberately NOT set here. llmwire sends the
// profile's recommended values (1.0 and 0.95) when the caller expresses no
// preference, which is exactly what this package has always sent and why:
// omitting them on Z.ai gives lower values, not "the defaults".
func chatRequestFor(ctx context.Context, messages []Message) llmwire.ChatRequest {
	req := llmwire.ChatRequest{
		Model:     modelFrom(ctx),
		Messages:  toWireMessages(messages),
		Reasoning: llmwire.ReasoningEffort(reasoningEffortFrom(ctx)),
		ExtraBody: map[string]any{"thinking": thinkingOptionFor(ctx)},
	}
	if n := maxTokensFrom(ctx); n > 0 {
		req.MaxTokens = &n
	}
	if wantsJSONObject(ctx) {
		req.ResponseFormat = llmwire.ResponseFormat{Kind: llmwire.FormatJSONObject}
	}
	return req
}

func toWireMessages(messages []Message) []llmwire.Message {
	out := make([]llmwire.Message, 0, len(messages))
	for _, m := range messages {
		out = append(out, llmwire.Message{Role: llmwire.Role(m.Role), Text: m.Content})
	}
	return out
}

// runStream drives one llmwire stream to completion.
//
// onDelta receives CONTENT fragments only. Reasoning deltas still arrive and
// still count as chunks — they are what proves a model is working during the
// long silence before the first word — but they are not part of the answer and
// must never reach a browser.
func (c *Client) runStream(ctx context.Context, wire *llmwire.Client, req llmwire.ChatRequest,
	counters *streamCounters, onDelta func(string)) (wireResult, error) {
	var res wireResult

	stream, warnings, err := wire.ChatStream(ctx, req)
	if err != nil {
		res.warnings = warnings
		return res, chatError(err)
	}
	defer stream.Close()

	var content strings.Builder
	for stream.Next() {
		ev := stream.Event()
		switch ev.Kind {
		case llmwire.EventContent:
			content.WriteString(ev.Text)
			if onDelta != nil {
				onDelta(ev.Text)
			}
			// Runes, not bytes: this endpoint returns non-ASCII routinely, and a
			// count inflated by UTF-8 encoding reads as more output than the
			// model produced.
			res.chars = counters.chars.Add(int64(utf8.RuneCountInString(ev.Text)))
			res.events = counters.events.Add(1)
		case llmwire.EventReasoning:
			res.events = counters.events.Add(1)
		case llmwire.EventFinish:
			res.finishReason = ev.FinishReason
			res.events = counters.events.Add(1)
		}
	}
	if err := stream.Err(); err != nil {
		res.warnings = stream.Warnings()
		return res, chatError(err)
	}

	usage := stream.Usage()
	res.content = content.String()
	res.rawUsage = usage.Raw
	res.costNanoUSD = usage.Cost.NanoUSD
	res.costPriced = usage.Cost.Provenance != llmwire.Unpriced
	res.warnings = stream.Warnings()
	return res, nil
}

// chatError keeps this package's own error phrasing over llmwire's.
//
// Not cosmetic: "chat failed with status 429: …" is what the operator runbook
// greps for and what the summarize worker's retry logging quotes. The status and
// the endpoint's own message are what a reader needs, and llmwire's typed error
// carries both — so the wrapper re-renders them rather than letting the library's
// prefix leak into text this repo has published.
//
// Everything else passes through unchanged, including the named bounds ("no
// response headers within 1m0s", "stream idle for 1m30s", "exceeded the 15m0s
// call cap"), which are already the strings this package used to produce.
func chatError(err error) error {
	// One check covers every refusal, 429 included: llmwire v0.0.6 gave
	// RateLimitError an Unwrap, so a rate limit now reaches *APIError the same way
	// every other status does. Before that it did not — embedding is not
	// unwrapping — and this function carried a second branch for the one status
	// that mattered most. The table test below is what makes removing it safe.
	var apiErr *llmwire.APIError
	if errors.As(err, &apiErr) {
		return statusError(apiErr)
	}
	return fmt.Errorf("chat: %w", err)
}

// statusError renders an endpoint refusal in this package's own phrasing.
//
// StatusCode is 0 for a failure delivered INSIDE a 200 — an error frame in the
// middle of a stream, which this endpoint does send. There is no status to
// report there, and printing "status 0" would send a reader looking for an HTTP
// code that never existed. Before the migration those frames were skipped
// entirely and surfaced as "ended without finish_reason", which said nothing
// about the actual failure; naming the endpoint's own code is strictly better.
func statusError(e *llmwire.APIError) error {
	if e.StatusCode == 0 {
		if e.Code != "" {
			return fmt.Errorf("chat failed mid-stream (code %s): %s", e.Code, e.Message)
		}
		return fmt.Errorf("chat failed mid-stream: %s", e.Message)
	}
	return fmt.Errorf("chat failed with status %d: %s", e.StatusCode, e.Message)
}

// unpricedWarned throttles the no-rate warning to once per model id per process.
//
// llmwire warns on every call, correctly for a library — a per-call return value
// must not depend on call order. peeq logs, where the volume matters: the
// map-reduce path makes dozens of calls per video and a per-call line would bury
// everything else in the job's log. The fact is constant per deployment, so
// saying it once is saying it.
var unpricedWarned sync.Map

func shouldWarnUnpriced(modelID string) bool {
	_, loaded := unpricedWarned.LoadOrStore(modelID, struct{}{})
	return !loaded
}
