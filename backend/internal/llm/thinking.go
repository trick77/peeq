package llm

import "context"

// The thinking switch is carried on the context rather than on Complete's
// signature — the same reasoning as CallInfo (see callinfo.go): the Completer
// interface stays one method wide and every fake that implements it keeps
// compiling.
//
// There is nothing to switch any more. GLM-5.3-Flash refuses
// thinking:{"type":"disabled"} with code 1210, "This model always engages in
// thinking and cannot be disabled; please use low, high, or max", so the field
// is sent enabled on every call and depth is chosen with reasoning_effort
// instead (see calloptions.go). The wire shape stays because the endpoint
// requires the object, not because peeq has a choice about its value.
//
// Under MiMo this field was the lever: sending it explicitly was what made
// reasoning_tokens honest (0 reported with the field absent, 594 with it
// enabled, on a call that plainly reasoned either way), and disabling it drove
// reasoning to a measured zero. Neither is true here — Z.ai reports
// reasoning_tokens whether or not the object is sent, and never reports zero.
const thinkingEnabled = "enabled"

type shallowKey struct{}

// Shallow returns a context whose LLM calls ask for the least reasoning the
// model allows (lowReasoningEffort). It is for a step that is a lookup rather
// than a deduction AND that something is waiting on — today only the Ask
// understand gate, which sits in front of the first byte of an answer under a
// 10s timeout.
//
// It is not a cost lever. Reasoning at low is nearly free on this model
// (measured: 0 tokens on a classification, 53 on the understand gate) but so is
// reasoning at high, so saving tokens is never the reason to reach for this.
// Latency is: low answers the understand gate in 2.5s where max takes 7.4s.
//
// A step with no one waiting on it should NOT use this. The default is max, and
// the offline summary calls take it as-is — see WithReasoningEffort.
func Shallow(ctx context.Context) context.Context {
	return context.WithValue(ctx, shallowKey{}, true)
}

// ShallowFrom reports whether the calls made with ctx want the shallowest
// reasoning. Absent a value the answer is no, so a caller that never opts in is
// unchanged.
func ShallowFrom(ctx context.Context) bool {
	shallow, ok := ctx.Value(shallowKey{}).(bool)
	return ok && shallow
}

// thinkingOption is the wire shape of the switch:
// {"type":"enabled","clear_thinking":false}.
//
// clear_thinking controls whether reasoning content is carried across turns.
// Z.ai recommends false for this model, and peeq sends it that way — though it
// makes no difference here in practice: every call is a fresh system+user pair
// with no prior assistant turn, so there is no reasoning to carry or clear. It
// is sent to match the recommended configuration rather than to fix anything.
type thinkingOption struct {
	Type          string `json:"type"`
	ClearThinking bool   `json:"clear_thinking"`
}

func thinkingOptionFor(context.Context) thinkingOption {
	return thinkingOption{Type: thinkingEnabled, ClearThinking: false}
}
