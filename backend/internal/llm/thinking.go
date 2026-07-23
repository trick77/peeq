package llm

import "context"

// Thinking is MiMo's native switch for chain-of-thought, carried on the
// context rather than on Complete's signature — the same reasoning as CallInfo
// (see callinfo.go): the Completer interface stays one method wide and every
// fake that implements it keeps compiling.
//
// The default is on, which is what the endpoint does anyway: MiMo returns
// reasoning_content whether or not the field is sent. What sending it buys is
// honest accounting — without an explicit thinking object the endpoint reports
// completion_tokens_details.reasoning_tokens as 0 and folds those tokens into
// completion_tokens, so the log read as "the model did not think" on a call
// that plainly had. Verified against token-plan-sgp.xiaomimimo.com: the same
// prompt reports 0 reasoning tokens with the field absent and 594 with it
// enabled, both returning ~2.5 kB of reasoning_content.
const (
	thinkingEnabled  = "enabled"
	thinkingDisabled = "disabled"
)

type thinkingKey struct{}

// WithoutThinking returns a context whose LLM calls ask the model not to think.
// It is for the steps whose answer is a lookup rather than a deduction — a
// one-word category, a two-sentence precis — where the reasoning is pure cost:
// classification burns several hundred completion tokens to emit a single id.
// Mirrors loom, which disables thinking on its utility calls for the same
// reason and lets only the main chat reason.
func WithoutThinking(ctx context.Context) context.Context {
	return context.WithValue(ctx, thinkingKey{}, false)
}

// ThinkingFrom reports whether the calls made with ctx should reason. Absent a
// value the answer is yes, so a caller that never opts out is unchanged.
func ThinkingFrom(ctx context.Context) bool {
	enabled, ok := ctx.Value(thinkingKey{}).(bool)
	return !ok || enabled
}

// thinkingOption is the wire shape of the switch: {"type":"enabled"}.
type thinkingOption struct {
	Type string `json:"type"`
}

func thinkingOptionFor(ctx context.Context) thinkingOption {
	if ThinkingFrom(ctx) {
		return thinkingOption{Type: thinkingEnabled}
	}
	return thinkingOption{Type: thinkingDisabled}
}
