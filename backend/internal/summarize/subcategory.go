package summarize

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/videos"
)

// subcategoryMaxAnswerTokens bounds both subcategory calls. An obedient answer
// is a JSON object of three short ids; the cap is generous for the reason
// classifyMaxAnswerTokens is: a cut reply parses as nothing, and the sweep
// records "nothing" as a final answer.
const subcategoryMaxAnswerTokens = 1024

// subcategoryRules is the tail both prompts share: the multi-label bar, the
// escape, the classify tie-break and the reply shape.
const subcategoryRules = "Pick at most 3, the best fit first, and only ones the content is substantially about; " +
	"one is fine when one fits. Use other only when nothing else fits. " +
	"Label by what the content is about, not by the institutions, agencies, countries or eras it mentions. " +
	"Reply with a JSON object of the form {\"subcategories\": [...]} and nothing else."

// ClassifySubcategories labels a video with up to videos.MaxSubcategories of
// its own category's subcategories, from the title and the short summary, and
// returns the normalized keys (none when the model said nothing fits). A reply
// that is no answer at all is errNotAnAnswer. A category without subcategories
// — 'uncategorized' — needs no call and returns nil.
//
// Same settings as Classify, for the same reasons: the gate model (the labels
// never reach a reader as prose) and the model's default reasoning (a
// persisted decision; the sweep never asks again, so an unstable answer is
// permanent).
//
// NOT llm.AsJSONObject: a gate model's profile may not support JSON-object
// output, and llmwire refuses the request outright rather than degrade. The
// understand step faces the same gate the same way — the prompt asks for the
// object and videos.NormalizeSubcategories cuts it out of whatever wraps it.
func (s *Summarizer) ClassifySubcategories(ctx context.Context, title, summary, category string) ([]string, error) {
	allowed := videos.SubcategoriesFor(category)
	if len(allowed) == 0 {
		return nil, nil
	}
	lines := make([]string, len(allowed))
	for i, sc := range allowed {
		lines[i] = subcategoryLine(sc.ID, sc)
	}
	sys := "You label a video with subcategory ids of its category, " + videos.CategoryLabel(category) +
		". The subcategories are:\n" + strings.Join(lines, "\n") + "\n" +
		subcategoryRules + " Each entry is one of the subcategory ids above."
	reply, err := s.c.Complete(subcategoryCtx(ctx), []llm.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: "TITLE: " + title + "\n\nSUMMARY:\n" + summary},
	})
	if err != nil {
		return nil, err
	}
	return normalized(reply, allowed)
}

// errNotAnAnswer is a reply that parses to no answer: empty, prose, cut, or
// only junk. It is an error rather than an empty result because the sweep
// persists an empty result as final ("nothing fits") and never asks again.
var errNotAnAnswer = errors.New("subcategory reply is not an answer")

func normalized(reply string, allowed []videos.Subcategory) ([]string, error) {
	keys, ok := videos.NormalizeSubcategories(reply, allowed)
	if !ok {
		return nil, fmt.Errorf("%w (%d chars)", errNotAnAnswer, len(reply))
	}
	return keys, nil
}

// ClassifyChannelSubcategories labels a channel with up to
// videos.MaxSubcategories keys from the whole vocabulary — a channel spans
// what its videos span, so its labels may cross categories — from its name,
// description and recent titles.
func (s *Summarizer) ClassifyChannelSubcategories(ctx context.Context, name, description string, titles []string) ([]string, error) {
	lines := make([]string, len(videos.Subcategories))
	for i, sc := range videos.Subcategories {
		lines[i] = subcategoryLine(sc.Key(), sc)
	}
	sys := "You label a YouTube channel with channel subcategory keys describing what the channel is about. " +
		"The keys, as category/subcategory, are:\n" + strings.Join(lines, "\n") + "\n" +
		subcategoryRules + " Each entry is one of the full keys above, category/subcategory."
	var b strings.Builder
	b.WriteString("CHANNEL: " + name)
	if d := strings.TrimSpace(description); d != "" {
		b.WriteString("\n\nDESCRIPTION:\n" + d)
	}
	if len(titles) > 0 {
		b.WriteString("\n\nRECENT VIDEO TITLES:")
		for _, t := range titles {
			b.WriteString("\n- " + t)
		}
	}
	reply, err := s.c.Complete(subcategoryCtx(ctx), []llm.Message{
		{Role: "system", Content: sys},
		{Role: "user", Content: b.String()},
	})
	if err != nil {
		return nil, err
	}
	return normalized(reply, videos.Subcategories)
}

func subcategoryLine(id string, sc videos.Subcategory) string {
	line := "- " + id + " (" + sc.Label + ")"
	if sc.Hint != "" {
		line += ": " + sc.Hint
	}
	return line
}

func subcategoryCtx(ctx context.Context) context.Context {
	return llm.WithMaxAnswerTokens(llm.ShortGate(ctx), subcategoryMaxAnswerTokens)
}
