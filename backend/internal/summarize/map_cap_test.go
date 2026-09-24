package summarize

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/llm"
)

// TestSummarizeText_mapCallsAreCapped: every chat call carries a max_tokens
// cap (AGENTS.md), and the coarse map used to be the one that did not. The cap
// counts reasoning, which is never zero at the default effort, so it is the
// summary's generous 8000 — measured worst case on this path is 524 tokens.
func TestSummarizeText_mapCallsAreCapped(t *testing.T) {
	client, stub := newStubChat(t, "section")
	s := New(client, WithSummaryChunkTokens(300))
	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err != nil {
		t.Fatal(err)
	}
	var maps int
	for i, body := range stub.requests() {
		if !strings.HasPrefix(systemPrompt(body), coarseSectionSystemPrompt[:40]) {
			continue
		}
		maps++
		got, ok := body["max_tokens"].(float64)
		if !ok || int(got) != summaryMaxTokens {
			t.Fatalf("map call %d max_tokens = %v, want %d", i, body["max_tokens"], summaryMaxTokens)
		}
	}
	if maps < 2 {
		t.Fatalf("saw %d map calls, want the transcript chunked into several", maps)
	}
}

// TestSummarizeText_emptyMapSectionIsAnError: a section the model returns
// empty (its budget spent on reasoning, say) must not be handed to the reduce
// as a blank — the reader would get a summary with that stretch missing and
// the job marked done. It fails, and the retry usually clears it.
func TestSummarizeText_emptyMapSectionIsAnError(t *testing.T) {
	calls := 0
	s := New(completerFunc(func(_ context.Context, m []llm.Message) (string, error) {
		calls++
		if strings.Contains(m[0].Content, "cohesive summary") {
			return "reduced", nil
		}
		if calls == 2 {
			return "   ", nil
		}
		return "section summary", nil
	}), WithSummaryChunkTokens(300))
	_, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000))
	if err == nil {
		t.Fatal("an empty map section must fail the summary, not feed the reduce")
	}
	if !strings.Contains(err.Error(), "empty section") {
		t.Fatalf("err = %v, want it to name the empty section", err)
	}
}

// The map asks for FailOnEarlyFinish like the single pass and the reduce: a
// refused or filtered section must not reach the reader's summary.
func TestSummarizeText_mapGuardsEarlyFinish(t *testing.T) {
	var guarded []bool
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		if strings.Contains(m[0].Content, "cohesive summary") {
			return "reduced", nil
		}
		guarded = append(guarded, llm.FailOnEarlyFinishFrom(ctx))
		return "section summary", nil
	}), WithSummaryChunkTokens(300))
	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err != nil {
		t.Fatal(err)
	}
	if len(guarded) == 0 {
		t.Fatal("no map calls seen")
	}
	for i, g := range guarded {
		if !g {
			t.Fatalf("map call %d did not ask for FailOnEarlyFinish", i)
		}
	}
}
