package summarize

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/llmwire"
	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/videos"
)

// Both subcategory calls are labels no reader sees, persisted for good: the
// gate model, the model's default reasoning (a persisted decision never runs
// at the shallowest setting — AGENTS.md), and an answer cap. No JSON mode: the
// gate model may not support it, and llmwire refuses rather than degrades.
func TestSubcategoryCallsAreGatesAtDefaultReasoning(t *testing.T) {
	client, srv := newStubChat(t, `{"subcategories":["building"]}`)
	s := New(client)
	ctx := context.Background()

	if _, err := s.ClassifySubcategories(ctx, "Title", "Summary.", "ai"); err != nil {
		t.Fatal(err)
	}
	checkGate := func(name string) {
		t.Helper()
		r := srv.Last()
		if r.Model() != stubGateModel {
			t.Fatalf("%s ran on %q, want the gate model", name, r.Model())
		}
		if got := r.Reasoning(); got != "" {
			t.Errorf("%s sent reasoning %q, want the model's default", name, got)
		}
		if got, ok := r.MaxTokens(); !ok || got != subcategoryMaxAnswerTokens+llmwire.DefaultReasoningOverhead {
			t.Errorf("%s cap = %d (sent %v)", name, got, ok)
		}
		if rf, ok := r.Body["response_format"]; ok {
			t.Errorf("%s sent response_format %v; the gate model may not support it", name, rf)
		}
	}
	checkGate("video")

	if _, err := s.ClassifyChannelSubcategories(ctx, "GCN", "A cycling channel.", []string{"Pogacar wins"}); err != nil {
		t.Fatal(err)
	}
	checkGate("channel")
}

func TestClassifySubcategories_offersOnlyTheCategorysList(t *testing.T) {
	var sys, user string
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		sys, user = m[0].Content, m[1].Content
		if !llm.ShortGateFrom(ctx) {
			t.Error("not routed to the gate model")
		}
		return `{"subcategories":["cycling","fitness"]}`, nil
	}))
	got, err := s.ClassifySubcategories(context.Background(), "Watts", "A training video.", "sports")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "sports/cycling,sports/fitness" {
		t.Fatalf("got %v", got)
	}
	if !strings.Contains(sys, "- cycling (Cycling): racing, training") || !strings.Contains(sys, "- other (Other)") {
		t.Fatalf("system prompt does not list the sports subcategories with hints: %q", sys)
	}
	if strings.Contains(sys, "building") || strings.Contains(sys, "uap") {
		t.Fatalf("system prompt offers another category's subcategories: %q", sys)
	}
	if !strings.Contains(sys, "at most 3") || !strings.Contains(sys, "subcategory ids") {
		t.Fatalf("system prompt missing the cap or the step phrase: %q", sys)
	}
	if !strings.Contains(user, "Watts") || !strings.Contains(user, "training video") {
		t.Fatalf("user message missing title/summary: %q", user)
	}
}

func TestClassifySubcategories_noListNoCall(t *testing.T) {
	s := New(completerFunc(func(context.Context, []llm.Message) (string, error) {
		t.Fatal("called the model for a category without subcategories")
		return "", nil
	}))
	got, err := s.ClassifySubcategories(context.Background(), "T", "S", videos.UncategorizedCategory)
	if err != nil || got != nil {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestClassifyChannelSubcategories_offersEveryKeyAndReadsTitles(t *testing.T) {
	var sys, user string
	s := New(completerFunc(func(_ context.Context, m []llm.Message) (string, error) {
		sys, user = m[0].Content, m[1].Content
		return `{"subcategories":["sports/cycling","tech/wearables","other"]}`, nil
	}))
	got, err := s.ClassifyChannelSubcategories(context.Background(), "DC Rainmaker", "Sports tech reviews.",
		[]string{"Garmin Edge 1060 review", "Apple Watch Ultra 4"})
	if err != nil {
		t.Fatal(err)
	}
	// 'other' names no category across the whole vocabulary, so it is dropped.
	if strings.Join(got, ",") != "sports/cycling,tech/wearables" {
		t.Fatalf("got %v", got)
	}
	if !strings.Contains(sys, "sports/cycling (Cycling)") || !strings.Contains(sys, "ai/building") || !strings.Contains(sys, "channel subcategory keys") {
		t.Fatalf("system prompt does not offer the whole vocabulary as keys: %.300q", sys)
	}
	for _, want := range []string{"DC Rainmaker", "Sports tech reviews.", "- Garmin Edge 1060 review"} {
		if !strings.Contains(user, want) {
			t.Fatalf("user message missing %q: %q", want, user)
		}
	}
}
