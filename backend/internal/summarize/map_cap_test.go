package summarize

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/llmwire"
	"github.com/trick77/peeq/internal/llm"
)

// TestSummarizeText_mapCallsAreCapped: every chat call carries an answer cap
// (AGENTS.md), and the coarse map used to be the one that did not. It is the
// summary's; the reasoning allowance on top is the model profile's.
func TestSummarizeText_mapCallsAreCapped(t *testing.T) {
	client, srv := newStubChat(t, "section")
	s := New(client, WithSummaryChunkTokens(300))
	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err != nil {
		t.Fatal(err)
	}
	var maps int
	for i, req := range srv.Requests() {
		if !strings.HasPrefix(systemPrompt(req.Body), coarseSectionSystemPrompt[:40]) {
			continue
		}
		maps++
		want := summaryMaxAnswerTokens + llmwire.DefaultReasoningOverhead
		if got, ok := req.MaxTokens(); !ok || got != want {
			t.Fatalf("map call %d cap = %d (sent %v), want the answer cap %d plus the default allowance", i, got, ok, want)
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
