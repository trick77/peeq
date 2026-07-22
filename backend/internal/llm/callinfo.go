package llm

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// CallInfo describes the work an LLM call belongs to. It rides on the
// context rather than on Complete's signature so the client can log
// meaningfully (which video, which pipeline step) without widening the
// Completer interface every caller and test fake implements.
type CallInfo struct {
	VideoID string
	Title   string
	Channel string
	Step    string
	// Stage is the caller's position in its pipeline, rendered as "2/4", so a
	// heartbeat says how far along the work is and not just that it is slow.
	Stage string
	// Totals, when set, accumulates the token usage of every call made with
	// this info, so the caller can report a per-video total.
	Totals *Totals
}

type callInfoKey struct{}

// WithCall attaches ci to ctx. Callers set the video identity once and then
// vary only the step via WithStep.
func WithCall(ctx context.Context, ci CallInfo) context.Context {
	return context.WithValue(ctx, callInfoKey{}, ci)
}

// WithStep returns a context carrying the same CallInfo with a different
// step. It is safe on a context that has none: the result then carries an
// otherwise-empty CallInfo with just the step set.
func WithStep(ctx context.Context, step string) context.Context {
	ci := CallFrom(ctx)
	ci.Step = step
	return WithCall(ctx, ci)
}

// WithStage returns a context carrying the same CallInfo with the pipeline
// position set ("2/4"). Like WithStep, it is safe on a context that has none.
func WithStage(ctx context.Context, stage string) context.Context {
	ci := CallFrom(ctx)
	ci.Stage = stage
	return WithCall(ctx, ci)
}

// CallFrom returns the CallInfo attached to ctx, or the zero value when there
// is none — a caller that never attached one still works and simply logs less.
func CallFrom(ctx context.Context) CallInfo {
	ci, _ := ctx.Value(callInfoKey{}).(CallInfo)
	return ci
}

// LogAttrs returns the identity of the call as slog key/value pairs, omitting
// the fields that are unset.
func (ci CallInfo) LogAttrs() []any {
	attrs := make([]any, 0, 10)
	if ci.Step != "" {
		attrs = append(attrs, "step", ci.Step)
	}
	if ci.Stage != "" {
		attrs = append(attrs, "stage", ci.Stage)
	}
	if ci.VideoID != "" {
		attrs = append(attrs, "video_id", ci.VideoID)
	}
	if ci.Title != "" {
		attrs = append(attrs, "title", ci.Title)
	}
	if ci.Channel != "" {
		attrs = append(attrs, "channel", ci.Channel)
	}
	return attrs
}

// Usage accounts one or more chat completions: the tokens the endpoint
// reported, plus how the wall time split between inference and the deliberate
// gap RequestInterval puts in front of each call.
//
// Accounted counts the calls that came back with a usage object. It is what
// separates "the model spent 0 reasoning tokens" from "this endpoint does not
// report reasoning tokens" — indistinguishable otherwise, since both leave the
// field at zero, and the whole reason a zero is worth printing. Counting
// rather than flagging also exposes a partial total: an endpoint that reports
// usage on some calls and not others would otherwise print sums that look
// complete.
type Usage struct {
	Requests         int64
	Accounted        int64
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	ReasoningTokens  int64
	TotalTokens      int64

	// InferenceNanos is time spent waiting on the endpoint; PacedNanos is time
	// spent deliberately not calling it. Nanoseconds so Add can sum them.
	InferenceNanos int64
	PacedNanos     int64
}

// Totals accumulates Usage across the many calls one video costs (the
// map-reduce summary alone is one call per transcript chunk). The zero value
// is ready to use and safe for concurrent callers.
type Totals struct {
	mu sync.Mutex
	u  Usage
}

// Add folds one call's usage into the running total. A nil *Totals is a no-op,
// so callers never have to check.
func (t *Totals) Add(u Usage) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.u.Requests += u.Requests
	t.u.Accounted += u.Accounted
	t.u.PromptTokens += u.PromptTokens
	t.u.CachedTokens += u.CachedTokens
	t.u.CompletionTokens += u.CompletionTokens
	t.u.ReasoningTokens += u.ReasoningTokens
	t.u.TotalTokens += u.TotalTokens
	t.u.InferenceNanos += u.InferenceNanos
	t.u.PacedNanos += u.PacedNanos
}

// Snapshot returns the totals so far. A nil *Totals yields the zero Usage.
func (t *Totals) Snapshot() Usage {
	if t == nil {
		return Usage{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.u
}

// Sub returns the usage accrued between an earlier snapshot and u, which is
// how a caller reports the cost of one pipeline step out of a running total.
func (u Usage) Sub(earlier Usage) Usage {
	return Usage{
		Requests:         u.Requests - earlier.Requests,
		Accounted:        u.Accounted - earlier.Accounted,
		PromptTokens:     u.PromptTokens - earlier.PromptTokens,
		CachedTokens:     u.CachedTokens - earlier.CachedTokens,
		CompletionTokens: u.CompletionTokens - earlier.CompletionTokens,
		ReasoningTokens:  u.ReasoningTokens - earlier.ReasoningTokens,
		TotalTokens:      u.TotalTokens - earlier.TotalTokens,
		InferenceNanos:   u.InferenceNanos - earlier.InferenceNanos,
		PacedNanos:       u.PacedNanos - earlier.PacedNanos,
	}
}

// InferenceMillis is the time spent waiting on the endpoint, for a log line.
func (u Usage) InferenceMillis() int64 { return u.InferenceNanos / int64(time.Millisecond) }

// FormatTokens renders a token count for a log line: thousands as "41.2k",
// millions as "1.35M", anything under a thousand verbatim. Summarizing one
// video costs tens of thousands of tokens, and "41.2k" is readable at a glance
// where "41213" is not.
func FormatTokens(v int64) string {
	switch {
	case v >= 1_000_000 || v <= -1_000_000:
		return strconv.FormatFloat(float64(v)/1_000_000, 'f', 2, 64) + "M"
	case v >= 1_000 || v <= -1_000:
		return strconv.FormatFloat(float64(v)/1_000, 'f', 1, 64) + "k"
	default:
		return strconv.FormatInt(v, 10)
	}
}

// LogAttrs returns the usage as slog key/value pairs. When the endpoint
// reported usage, ALL token fields are emitted including zeros: a zero is an
// answer ("no cache hit on this call", "the model did not think"), and
// dropping it made it indistinguishable from an endpoint that reports nothing.
// This mirrors loom's inferenceLogAttrs, which logs the same five numbers
// whenever usage is present. The keys say "chat" because these are chat-model
// tokens only — embedding tokens are accounted by the embedding client.
//
// Timings are omitted when zero: no inference means no call was made, and no
// pacing means RequestInterval is off, neither of which is worth a column.
func (u Usage) LogAttrs() []any {
	attrs := make([]any, 0, 16)
	if u.Requests != 0 {
		attrs = append(attrs, "chat_requests", u.Requests)
	}
	if u.InferenceNanos != 0 {
		attrs = append(attrs, "chat_inference_ms", u.InferenceMillis())
	}
	if u.PacedNanos != 0 {
		attrs = append(attrs, "chat_paced_ms", u.PacedNanos/int64(time.Millisecond))
	}
	if u.Accounted == 0 {
		return attrs
	}
	if u.Accounted != u.Requests {
		// Some calls came back without usage, so the sums below cover only part
		// of the work. Say so rather than presenting a short total as complete.
		attrs = append(attrs, "chat_accounted", u.Accounted)
	}
	for _, f := range []struct {
		key string
		val int64
	}{
		{"chat_tokens_in", u.PromptTokens},
		{"chat_tokens_cached", u.CachedTokens},
		{"chat_tokens_out", u.CompletionTokens},
		{"chat_tokens_reasoning", u.ReasoningTokens},
		{"chat_tokens_total", u.TotalTokens},
	} {
		attrs = append(attrs, f.key, FormatTokens(f.val))
	}
	return attrs
}
