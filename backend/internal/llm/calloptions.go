package llm

import (
	"context"

	"github.com/trick77/llmwire"
)

// Per-call knobs carried on the context, same pattern as CallInfo: they keep
// the Completer interface one method wide so every fake that implements it
// keeps compiling, while letting each step tune the request independently
// instead of sharing one hardcoded shape.
//
// Every knob here is an INTENT. What an intent becomes for the configured model
// (a level, a budget, off) is that model's llmwire profile's to say, so a model
// swap is a config change and no call site changes with it.

// --- reasoning ----------------------------------------------------------------

// Reasoning is how much thinking a call asks for.
type Reasoning string

const (
	// ReasoningDefault sends no reasoning knob: the model runs at its own
	// default, the setting its vendor tuned it for. Every call that writes
	// something a reader keeps, and that nobody waits on, takes this.
	ReasoningDefault Reasoning = "default"
	// ReasoningBalanced is the model's fast-but-not-shallow level. For prose a
	// reader keeps where a person IS waiting (the streamed Ask answer): it
	// trades depth for time-to-first-token without dropping to the floor.
	ReasoningBalanced Reasoning = "balanced"
	// ReasoningMinimal is the shallowest setting the model allows, which on
	// some models is thinking OFF. Only for a true gate whose output nobody
	// reads as prose and whose error costs little, under a latency bound: today
	// the Ask understand step. Never for prose, and never for a decision that
	// is persisted (see Summarizer.Classify).
	ReasoningMinimal Reasoning = "minimal"
)

// wire is the llmwire request for r; nil sends nothing.
func (r Reasoning) wire() llmwire.ReasoningRequest {
	switch r {
	case ReasoningBalanced:
		return llmwire.ReasoningBalanced()
	case ReasoningMinimal:
		return llmwire.ReasoningMinimal()
	}
	return nil
}

type reasoningKey struct{}

// WithReasoning sets the reasoning intent for calls made with ctx. Absent one,
// calls take ReasoningDefault.
func WithReasoning(ctx context.Context, r Reasoning) context.Context {
	return context.WithValue(ctx, reasoningKey{}, r)
}

// ReasoningFor names the intent a call made with ctx asks for, for a caller
// that has to report or assert it rather than choose it.
func ReasoningFor(ctx context.Context) Reasoning {
	if r, ok := ctx.Value(reasoningKey{}).(Reasoning); ok && r != "" {
		return r
	}
	return ReasoningDefault
}

// --- model --------------------------------------------------------------------

type shortGateKey struct{}

// ShortGate routes the calls made with ctx to the gate model (Config.GateModel,
// the chat model when unset). It is for the short gates: a step whose answer
// is one id or label from a list the prompt already spells out.
//
// Keep it at its call sites even while the gate model equals the chat model:
// it records which steps are gates, which is what pointing them at a separate
// model needs to know.
//
// It is separate from the reasoning intent: ShortGate picks the model, the
// intent picks the depth. Deliberately NOT for anything that writes text a
// reader sees. The bar is what the call produces — an id or a label that lands
// in a filter. The summary, the coarse section map, the reduce, the keypoints
// and chapters, and the Ask answer are all off-limits.
func ShortGate(ctx context.Context) context.Context {
	return context.WithValue(ctx, shortGateKey{}, true)
}

// ShortGateFrom reports whether ctx was marked as a short gate. A caller that
// needs the DECISION rather than which model it reaches asks here: the two
// models may be the same id.
func ShortGateFrom(ctx context.Context) bool {
	gate, ok := ctx.Value(shortGateKey{}).(bool)
	return ok && gate
}

// --- response format ----------------------------------------------------------

type jsonObjectKey struct{}

// AsJSONObject asks the endpoint to constrain the reply to a single JSON object
// (response_format json_object). Opt-in per call, because most calls here must
// NOT be constrained: classify answers with a bare id and the summary and Ask
// answers are prose.
//
// It is not belt-and-braces over a prompt that already says "as JSON": a
// prompt alone did not yield strictly parseable replies (fenced or prefaced
// with prose), response_format did. summarize.extractJSON still salvages both
// shapes, because a model may fence its reply whatever the format asks.
func AsJSONObject(ctx context.Context) context.Context {
	return context.WithValue(ctx, jsonObjectKey{}, true)
}

func jsonObjectFrom(ctx context.Context) bool {
	v, ok := ctx.Value(jsonObjectKey{}).(bool)
	return ok && v
}

// --- answer cap ---------------------------------------------------------------

type maxAnswerTokensKey struct{}

// WithMaxAnswerTokens caps the visible ANSWER of calls made with ctx at n
// tokens. The reasoning the call's intent allows is added on top by llmwire,
// from the model's profile, and the total is clamped to the model's output
// limit — so size n for the answer alone. 0, the default, sends no cap and
// leaves the endpoint's own limit.
//
// A reasoning allowance is an estimate, not a reservation: a call that thinks
// past it still ends with finish_reason "length" and an empty answer, no
// error. So n stays generous — it is a runaway backstop, not a length target.
func WithMaxAnswerTokens(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, maxAnswerTokensKey{}, n)
}

func maxAnswerTokensFrom(ctx context.Context) int {
	if n, ok := ctx.Value(maxAnswerTokensKey{}).(int); ok && n > 0 {
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
// store half a summary of the whole video. A deterministic cut is deliberately
// tolerated — "length" is our own cap, a context-window finish the prompt
// outgrowing the model — because retrying would just re-cut (see
// deterministicCut in client.go).
func FailOnEarlyFinish(ctx context.Context) context.Context {
	return context.WithValue(ctx, failEarlyKey{}, true)
}

// FailOnEarlyFinishFrom reports whether ctx asks for FailOnEarlyFinish.
// Exported for the same reason ShortGateFrom is: a test that fakes the
// completer sees a context, not the wire, and has to be able to assert what
// a call site asked for.
func FailOnEarlyFinishFrom(ctx context.Context) bool { return failOnEarlyFinishFrom(ctx) }

func failOnEarlyFinishFrom(ctx context.Context) bool {
	v, ok := ctx.Value(failEarlyKey{}).(bool)
	return ok && v
}
