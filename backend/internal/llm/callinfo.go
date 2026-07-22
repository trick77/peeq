package llm

import (
	"context"
	"strconv"
	"sync"
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

// CallFrom returns the CallInfo attached to ctx, or the zero value when there
// is none — a caller that never attached one still works and simply logs less.
func CallFrom(ctx context.Context) CallInfo {
	ci, _ := ctx.Value(callInfoKey{}).(CallInfo)
	return ci
}

// LogAttrs returns the identity of the call as slog key/value pairs, omitting
// the fields that are unset.
func (ci CallInfo) LogAttrs() []any {
	attrs := make([]any, 0, 8)
	if ci.Step != "" {
		attrs = append(attrs, "step", ci.Step)
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

// Usage is the token accounting of a single chat completion, as far as the
// endpoint reports it. Every field is optional: an endpoint that omits
// prompt_tokens_details simply leaves CachedTokens at zero.
type Usage struct {
	Requests         int64
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	ReasoningTokens  int64
	TotalTokens      int64
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
	t.u.PromptTokens += u.PromptTokens
	t.u.CachedTokens += u.CachedTokens
	t.u.CompletionTokens += u.CompletionTokens
	t.u.ReasoningTokens += u.ReasoningTokens
	t.u.TotalTokens += u.TotalTokens
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
		PromptTokens:     u.PromptTokens - earlier.PromptTokens,
		CachedTokens:     u.CachedTokens - earlier.CachedTokens,
		CompletionTokens: u.CompletionTokens - earlier.CompletionTokens,
		ReasoningTokens:  u.ReasoningTokens - earlier.ReasoningTokens,
		TotalTokens:      u.TotalTokens - earlier.TotalTokens,
	}
}

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

// LogAttrs returns the usage as slog key/value pairs, omitting zero fields so
// an endpoint that reports no cached/reasoning breakdown doesn't pad every
// line with zeroes. The keys say "chat" because these are chat-model tokens
// only — embedding tokens are accounted separately, by the embedding client.
func (u Usage) LogAttrs() []any {
	attrs := make([]any, 0, 12)
	add := func(k string, v int64) {
		if v != 0 {
			attrs = append(attrs, k, FormatTokens(v))
		}
	}
	if u.Requests != 0 {
		attrs = append(attrs, "chat_requests", u.Requests)
	}
	add("chat_tokens_in", u.PromptTokens)
	add("chat_tokens_cached", u.CachedTokens)
	add("chat_tokens_out", u.CompletionTokens)
	add("chat_tokens_reasoning", u.ReasoningTokens)
	add("chat_tokens_total", u.TotalTokens)
	return attrs
}
