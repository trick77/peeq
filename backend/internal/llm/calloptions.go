package llm

import "context"

// Per-call knobs carried on the context, same pattern as thinking.go and
// CallInfo: they keep the Completer interface one method wide so every fake that
// implements it keeps compiling, while letting each summarize stage tune the
// request independently instead of sharing one hardcoded shape.

// --- reasoning effort ---------------------------------------------------------

type reasoningEffortKey struct{}

// WithReasoningEffort overrides the reasoning_effort sent for calls made with
// ctx. Absent an override the package default (reasoningEffort, "max") is used,
// so callers that never opt in are unchanged.
//
// This is the ONLY depth control the endpoint offers — thinking itself cannot be
// switched off (see thinking.go), and only low/high/max are accepted. It was
// inert under MiMo, where high and low returned the same reasoning-token
// distribution and the same latency; that is no longer true, and the comment
// that used to say so has been removed rather than kept, because acting on it
// against Z.ai would be wrong.
//
// Effort is chosen by what is waiting on the call, not by token cost. The
// default is max — Z.ai's own default and their recommendation for this model —
// and only Shallow opts out, for the one gate under a hard timeout (see
// thinking.go).
//
// Tokens barely separate the levels (the whole summary of a 40-minute video
// reasons for ~144 tokens at max), so nothing should be tuned downward to save
// money. Time does separate them, sometimes by a lot: the keypoints prompt costs
// 12.8s at high and 69.9s at max. Reach for this only with a latency reason, and
// write the reason down.
func WithReasoningEffort(ctx context.Context, effort string) context.Context {
	return context.WithValue(ctx, reasoningEffortKey{}, effort)
}

// HighReasoningEffort is the tier between Shallow and the max default, exported
// for the one call that must pin it: the streamed Ask answer, where max is paid
// in time-to-first-token while a reader watches (see httpapi/answer_handlers.go).
// Prefer the default everywhere else.
const HighReasoningEffort = highReasoningEffort

// EffortFor names the reasoning depth a call made with ctx will ask for, for a
// caller that has to REPORT or assert which tier a step ran at rather than
// choose one. Same reasoning as ModelFor below: the wire values are unexported
// consts chosen inside this package, so without it a caller could only hardcode
// a second copy of them.
func EffortFor(ctx context.Context) string { return reasoningEffortFrom(ctx) }

func reasoningEffortFrom(ctx context.Context) string {
	if e, ok := ctx.Value(reasoningEffortKey{}).(string); ok && e != "" {
		return e
	}
	if ShallowFrom(ctx) {
		return lowReasoningEffort
	}
	return reasoningEffort
}

// --- model --------------------------------------------------------------------

type shortGateKey struct{}

// ShortGate routes the calls made with ctx to shortGateModel. It is for the
// short gates: a step whose answer is one id from a list the prompt already
// spells out.
//
// It currently changes NOTHING on the wire — Z.ai serves the gates from the same
// model as everything else, so shortGateModel and model are the same id (see
// client.go). It is kept at its call sites because it records which steps are
// gates, which is what a future split would need to know; under MiMo it picked
// the deployment that queued less. Do not remove it on the grounds that it is
// currently a no-op, and do not add a call site expecting it to do something.
//
// Pair it with Shallow — the two say different things. Shallow asks for the
// least reasoning the model allows; this asks for the gate deployment.
//
// Deliberately NOT for anything that writes text a reader sees. The bar is what
// the call produces — an id or a label that lands in a filter. The summary, the
// coarse section map, the reduce, the keypoints and chapters, and the Ask answer
// are all off-limits.
func ShortGate(ctx context.Context) context.Context {
	return context.WithValue(ctx, shortGateKey{}, true)
}

// ShortGateFrom reports whether ctx was marked as a short gate. It exists
// because ModelFor cannot answer this any more: shortGateModel and model hold
// the same id today, so comparing model names would report every call as a gate.
// A caller that needs the DECISION rather than its current wire effect asks
// here.
func ShortGateFrom(ctx context.Context) bool {
	gate, ok := ctx.Value(shortGateKey{}).(bool)
	return ok && gate
}

func modelFrom(ctx context.Context) string {
	if v, ok := ctx.Value(shortGateKey{}).(bool); ok && v {
		return shortGateModel
	}
	return model
}

// ModelFor names the deployment a call made with ctx will reach, for a caller
// that has to REPORT which model ran a step rather than choose one.
//
// It delegates to modelFrom rather than repeating the rule. That is the whole
// point of it existing: the model ids are unexported consts chosen inside this
// package, so a caller that wanted to label a call could only hardcode a second
// copy of both names and the ShortGate test between them — three things that
// drift silently, and whose drift shows up as a wrong model name on a panel
// nobody would think to distrust.
func ModelFor(ctx context.Context) string { return modelFrom(ctx) }

// --- max tokens ---------------------------------------------------------------

type maxTokensKey struct{}

// WithMaxTokens caps the completion length (max_tokens) for calls made with ctx.
// 0 — the default — omits the field, leaving the endpoint's own limit. Note the
// cap counts reasoning tokens too, so it bounds a runaway but does not by itself
// guarantee output on a deep call: one that spends the whole budget reasoning
// still returns empty, with finish_reason "length" and no content. Reasoning can
// no longer be switched off to avoid that (see thinking.go), so leave headroom —
// or, if something is waiting on the call, lower the effort with Shallow.
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
