package llm

import "context"

// Per-call knobs carried on the context, same pattern as thinking.go and
// CallInfo: they keep the Completer interface one method wide so every fake that
// implements it keeps compiling, while letting each summarize stage tune the
// request independently instead of sharing one hardcoded shape.

// --- reasoning effort ---------------------------------------------------------

type reasoningEffortKey struct{}

// WithReasoningEffort overrides the reasoning_effort sent for calls made with
// ctx. Absent an override the package default (reasoningEffort, "high") is used,
// so callers that never opt in are unchanged. Pair it with WithoutThinking off /
// a low effort to keep a cheap extractive call from over-reasoning.
func WithReasoningEffort(ctx context.Context, effort string) context.Context {
	return context.WithValue(ctx, reasoningEffortKey{}, effort)
}

func reasoningEffortFrom(ctx context.Context) string {
	if e, ok := ctx.Value(reasoningEffortKey{}).(string); ok && e != "" {
		return e
	}
	return reasoningEffort
}

// --- model --------------------------------------------------------------------

type nonReasoningKey struct{}

// NonReasoning routes the calls made with ctx to the non-Pro deployment
// (nonReasoningModel). It is for the short gates: a step whose answer is one id
// from a list the prompt already spells out needs a fast answer, not a deep one,
// and Pro is where the long thinking calls queue.
//
// Pair it with WithoutThinking — the two say different things. WithoutThinking
// asks the model not to reason; this asks for a deployment that answers sooner.
// Deliberately NOT for anything that writes text a reader sees: "no reasoning
// needed" is the bar here, not "thinking happens to be off". The summary, the
// coarse section map, the reduce and the Ask answer all stay on Pro — as do the
// keypoints and chapters, whose titles carry a MiMo attribution in the Player.
func NonReasoning(ctx context.Context) context.Context {
	return context.WithValue(ctx, nonReasoningKey{}, true)
}

func modelFrom(ctx context.Context) string {
	if v, ok := ctx.Value(nonReasoningKey{}).(bool); ok && v {
		return nonReasoningModel
	}
	return model
}

// --- max tokens ---------------------------------------------------------------

type maxTokensKey struct{}

// WithMaxTokens caps the completion length (max_tokens) for calls made with ctx.
// 0 — the default — omits the field, leaving the endpoint's own limit. Note the
// cap counts reasoning tokens too, so it bounds a runaway but does not by itself
// guarantee output on a thinking-heavy call: a call that spends the whole budget
// reasoning still returns empty (disable thinking for that).
func WithMaxTokens(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, maxTokensKey{}, n)
}

func maxTokensFrom(ctx context.Context) int {
	if n, ok := ctx.Value(maxTokensKey{}).(int); ok && n > 0 {
		return n
	}
	return 0
}

// --- fail on early finish -----------------------------------------------------

type failEarlyKey struct{}

// FailOnEarlyFinish makes a call return an error when the endpoint ends the
// answer with a finish_reason that signals a refusal or filter (anything other
// than a natural "stop" or a token-limit "length"), instead of returning the
// partial content as success. It is for calls whose truncated output must not be
// persisted — the single-pass summary, where a content_filter cut would silently
// store half a summary of the whole video. "length" is deliberately tolerated:
// that cut is our own max_tokens, and retrying would just re-truncate.
func FailOnEarlyFinish(ctx context.Context) context.Context {
	return context.WithValue(ctx, failEarlyKey{}, true)
}

func failOnEarlyFinishFrom(ctx context.Context) bool {
	v, ok := ctx.Value(failEarlyKey{}).(bool)
	return ok && v
}
