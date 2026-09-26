package llm

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"unicode/utf8"

	"github.com/trick77/llmwire"
)

// The seam onto llmwire.
//
// What moved out of this package: the SSE scanner, the three-bound stall guard
// that named which bound fired, the request struct, and the usage decoding. All
// four existed in near-identical form in five backends, and every model quirk
// learned here had to be relearned there — which is what llmwire exists to stop.
//
// What deliberately stayed: pacing, the heartbeat, the CallInfo/Totals
// accounting, the context knobs, and every log line. Those are peeq's policy,
// not wire protocol, and a library that owned them would be a framework.
//
// The usage seam: llmwire decodes the endpoint's usage object into typed lanes
// whose nil means "not reported", and keeps the bytes in Usage.Raw for the debug
// line. usageFromWire (client.go) folds those lanes into this package's Usage
// and derives Accounted from lane presence, so "reported a zero" and "reported
// nothing" stay distinguishable without a second parser.

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

// wireResult is one completed call: llmwire's own result (content, finish
// reason, and the decoded usage whose pointer lanes keep "reported a zero"
// distinct from "reported nothing", with Raw for the debug line) plus the
// counts this package kept while it was streaming and the warnings.
//
// events and chars are peeq's counters, not StreamResult's: StreamResult is
// written once, when the stream ends, so the heartbeat cannot read it mid-call,
// and the done line uses the same numbers the heartbeat showed rather than a
// second definition of "chunk" (llmwire counts data: frames; this counts
// content, reasoning and finish events).
type wireResult struct {
	wire     llmwire.StreamResult
	events   int64
	chars    int64
	warnings []llmwire.Warning
}

// chatRequestFor maps peeq's context knobs onto an llmwire request.
//
// Everything the model needs is a field llmwire models; nothing rides in
// ExtraBody. Sampling (temperature, top_p) is deliberately NOT set: llmwire
// sends the profile's recommended values when the caller expresses no
// preference, and an endpoint's own fallback is not necessarily the model's
// tuned point.
func (c *Client) chatRequestFor(ctx context.Context, messages []Message) llmwire.ChatRequest {
	req := llmwire.ChatRequest{
		Model:     c.ModelFor(ctx),
		Messages:  toWireMessages(messages),
		Reasoning: ReasoningFor(ctx).wire(),
	}
	if n := maxAnswerTokensFrom(ctx); n > 0 {
		req.MaxAnswerTokens = &n
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
	defer func() { _ = stream.Close() }()

	for stream.Next() {
		ev := stream.Event()
		switch ev.Kind {
		case llmwire.EventContent:
			if onDelta != nil {
				onDelta(ev.Text)
			}
			// Runes, not bytes: this endpoint returns non-ASCII routinely, and a
			// count inflated by UTF-8 encoding reads as more output than the
			// model produced.
			res.chars = counters.chars.Add(int64(utf8.RuneCountInString(ev.Text)))
			res.events = counters.events.Add(1)
		case llmwire.EventReasoning, llmwire.EventFinish:
			res.events = counters.events.Add(1)
		}
	}
	res.warnings = stream.Warnings()
	if err := stream.Err(); err != nil {
		return res, chatError(err)
	}
	// Content, finish reason and usage are llmwire's to assemble; valid now that
	// Next has returned false.
	res.wire = stream.Result()
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
		return statusError(err, apiErr)
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
//
// cause is the error as llmwire returned it, kept reachable through Unwrap so
// errors.Is(err, llmwire.ErrRateLimited) and errors.As on *RateLimitError (the
// Retry-After) still work on what this package hands out. A plain fmt.Errorf
// without %w used to cut that chain, which nothing noticed only because nothing
// downstream asked yet.
func statusError(cause error, e *llmwire.APIError) error {
	var msg string
	switch {
	case e.StatusCode == 0 && e.Code != "":
		msg = fmt.Sprintf("chat failed mid-stream (code %s): %s", e.Code, e.Message)
	case e.StatusCode == 0:
		msg = "chat failed mid-stream: " + e.Message
	default:
		msg = fmt.Sprintf("chat failed with status %d: %s", e.StatusCode, e.Message)
	}
	return Rephrase(cause, msg)
}

// Rephrase returns an error whose text is msg and whose chain is cause's:
// Error() says what the runbook greps for, Unwrap keeps llmwire's typed error
// behind it. Rendering with %w instead would prefix llmwire's own text, which
// is exactly what the rephrasing exists to avoid. Exported for the embedding
// client, which rephrases the same way.
func Rephrase(cause error, msg string) error {
	return &rephrasedError{msg: msg, cause: cause}
}

type rephrasedError struct {
	msg   string
	cause error
}

func (e *rephrasedError) Error() string { return e.msg }
func (e *rephrasedError) Unwrap() error { return e.cause }
