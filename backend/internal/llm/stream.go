package llm

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// The wire shape below was verified against token-plan-sgp.xiaomimimo.com with
// stream:true and stream_options.include_usage, and three of its details are
// load-bearing enough to record rather than rediscover:
//
//   - Reasoning arrives as its own delta field, reasoning_content, with content
//     null throughout. On a "count to 20" probe: nine reasoning deltas, THEN
//     fourteen content deltas. So a long thinking phase produces a steady flow
//     of events carrying no content at all — which is precisely why the idle
//     guard counts events rather than output.
//   - The usage chunk arrives AFTER the chunk carrying finish_reason, and has
//     an EMPTY choices array. Stopping at finish_reason would silently lose all
//     token accounting, and indexing choices[0] on it would panic.
//   - That finish_reason chunk carries "usage":null. A usage field being
//     present is therefore not the same as usage being reported, so we keep the
//     last one that chatUsage.reported() accepts rather than the last one seen.
const (
	// dataPrefix has no trailing space on purpose: SSE permits "data:{...}" as
	// well as "data: {...}", and matching only the spaced form would silently
	// skip every event from an endpoint that omits it — as a non-data line, with
	// no error anywhere. The space, if present, comes off with TrimSpace below.
	dataPrefix = "data:"
	doneMarker = "[DONE]"
	// maxStreamLine caps one SSE line. A reasoning delta is ordinarily a few
	// words, but the limit is generous because the cost of guessing low is a
	// failed summary on a legitimately large chunk, and bufio's default 64KB is
	// a guess we did not make deliberately.
	maxStreamLine = 1 << 20
)

// streamDelta is one chunk's incremental payload.
type streamDelta struct {
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content"`
}

// chatStreamChunk is one `data:` event. Usage is kept as RawMessage for the
// reason usageFrom explains: the identical bytes feed both chatUsage and the
// debug line that shows what the endpoint really sent.
type chatStreamChunk struct {
	Choices []struct {
		Delta        streamDelta `json:"delta"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

// streamResult is what a completed stream yielded.
type streamResult struct {
	content      string
	finishReason string
	rawUsage     json.RawMessage
	events       int64
	chars        int64
	// done records that the endpoint sent [DONE]. Together with finishReason it
	// is the only evidence the answer is whole; see readStream's closing check.
	done bool
}

// streamCounters are the live counts the heartbeat reads while the stream is
// still being consumed. Atomic because the reader goroutine writes them as the
// heartbeat goroutine reads them — the same discipline heartbeat.go documents.
type streamCounters struct {
	events atomic.Int64
	chars  atomic.Int64
}

// attrs renders the counters for a heartbeat tick. chunks=0 after a minute is a
// dead socket; chunks climbing with chars flat is a model still reasoning.
func (s *streamCounters) attrs() []any {
	return []any{"chunks", s.events.Load(), "chars", s.chars.Load()}
}

// readStream consumes the SSE body, accumulating content deltas and keeping the
// last reported usage. It re-arms guard on EVERY event — content delta,
// reasoning delta, and blank separator alike — because any byte from the
// endpoint proves the socket is alive, which is the only question the idle
// bound is asking. A malformed data line is skipped rather than fatal: one
// unparseable keepalive must not discard a summary that otherwise completed.
func readStream(body io.Reader, guard *stallGuard, counters *streamCounters, idle time.Duration, onDelta func(string)) (streamResult, error) {
	var (
		content strings.Builder
		res     streamResult
	)
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64<<10), maxStreamLine)

	for sc.Scan() {
		// Every line re-arms the guard, blank ones included: any byte proves the
		// socket is alive, which is the only question the idle bound asks.
		guard.arm(idle, stallIdle)
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || !strings.HasPrefix(line, dataPrefix) {
			// Blank separators and SSE comment lines (": ping") are liveness and
			// nothing more.
			continue
		}
		// Counted here rather than per line: an event is "data:…" followed by a
		// blank separator, so counting lines reported double, and a log that
		// says 824 for a 412-chunk stream is a log nobody can reconcile with the
		// endpoint.
		res.events = counters.events.Add(1)
		payload := strings.TrimSpace(strings.TrimPrefix(line, dataPrefix))
		if payload == doneMarker {
			res.done = true
			break
		}
		var chunk chatStreamChunk
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				content.WriteString(ch.Delta.Content)
				// Hand the fragment onward as it arrives, for callers streaming
				// to a browser. Nil for the ordinary buffered callers.
				if onDelta != nil {
					onDelta(ch.Delta.Content)
				}
				// Runes, not bytes: this endpoint returns non-ASCII routinely,
				// and a "chars" figure inflated by UTF-8 encoding would misread
				// as more output than the model produced.
				res.chars = counters.chars.Add(int64(utf8.RuneCountInString(ch.Delta.Content)))
			}
			if ch.FinishReason != "" {
				res.finishReason = ch.FinishReason
			}
		}
		// Keep the last usage the endpoint actually reported; see the note above
		// on "usage":null riding along with finish_reason.
		if usageFrom(chunk.Usage).reported() {
			res.rawUsage = chunk.Usage
		}
	}
	res.content = content.String()
	if err := sc.Err(); err != nil {
		return res, err
	}
	// Completion is asserted by the endpoint, never inferred from having
	// received something. A connection that drops mid-answer ends the scan
	// exactly as a finished one does — cleanly, with no read error — so
	// accepting whatever arrived would store a truncated summary and call it
	// done. Silently keeping half an answer is worse than retrying, which is
	// the whole reason this client was rewritten.
	if !res.done && res.finishReason == "" {
		return res, fmt.Errorf("ended after %d events (%d chars) without finish_reason or %s",
			res.events, res.chars, doneMarker)
	}
	return res, nil
}

// Reasons a request was cancelled from our side, used verbatim in the returned
// error so a failure names which bound fired. The whole point of the change is
// that "the endpoint sent nothing at all" and "the model thought for a long
// time" stop being the same log line.
const (
	stallHeaders = "no response headers"
	stallIdle    = "stream idle"
)

// stallGuard cancels a request when nothing has arrived within the current
// bound, and remembers why. It starts armed for headers and is re-armed for
// idleness on every event, so one mechanism covers both phases and each keeps
// its own name.
//
// Doing this ourselves rather than leaning on Transport.ResponseHeaderTimeout
// alone is deliberate: that setting is an HTTP/1.1 transport feature and does
// not apply once a connection is negotiated as HTTP/2, which is exactly the
// case for this endpoint. It is still set as a backstop; this is the bound we
// actually rely on.
type stallGuard struct {
	cancel context.CancelFunc

	mu       sync.Mutex
	timer    *time.Timer
	deadline time.Time // when the current arming expires
	pending  string    // reason the currently-armed deadline would report
	reason   string    // reason it actually fired with, "" while healthy
	fired    bool
}

// newStallGuard arms the guard for its first deadline. Callers must stop() it.
func newStallGuard(cancel context.CancelFunc, d time.Duration, reason string) *stallGuard {
	g := &stallGuard{cancel: cancel, pending: reason, deadline: time.Now().Add(d)}
	g.timer = time.AfterFunc(d, g.fire)
	return g
}

// arm resets the deadline and the reason it would report. It is a no-op once
// the guard has fired: the request is already being cancelled, and re-arming
// would leave a cancelled call looking healthy.
//
// The deadline is recorded as a timestamp, not just handed to Reset, because
// Reset alone is not enough — see fire.
func (g *stallGuard) arm(d time.Duration, reason string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fired {
		return
	}
	g.pending = reason
	g.deadline = time.Now().Add(d)
	g.timer.Reset(d)
}

// fire cancels the request, unless the deadline moved while this firing was
// already on its way.
//
// Reset cannot recall a callback the runtime has ALREADY scheduled. So an event
// arriving in the window between expiry and this function taking the mutex
// leaves arm() believing it disarmed the guard — it saw fired == false — while
// this firing proceeds anyway. Two things went wrong without the check below: a
// stream that had just revived was cancelled regardless, and the failure was
// labelled with the reason arm() had just written rather than the bound that
// actually elapsed. The second is the worse one, since naming the right bound
// is the entire purpose of this type. Comparing against the recorded deadline
// makes a stale firing detectable and re-arms for the remaining time instead.
func (g *stallGuard) fire() {
	g.mu.Lock()
	if g.fired {
		g.mu.Unlock()
		return
	}
	if remaining := time.Until(g.deadline); remaining > 0 {
		g.timer.Reset(remaining)
		g.mu.Unlock()
		return
	}
	g.fired = true
	g.reason = g.pending
	g.mu.Unlock()
	g.cancel()
}

// stop disarms the guard. It does not cancel the request; the caller's own
// defer does that.
func (g *stallGuard) stop() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.timer.Stop()
}

// firedReason returns why the guard cancelled, or "" if it did not. Read after
// a request error to turn a bare "context canceled" into the bound that caused
// it.
func (g *stallGuard) firedReason() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reason
}
