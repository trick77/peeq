package summarize

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/subtitles"
	"github.com/trick77/peeq/internal/videos"
)

// fakeCompleter dispatches on the system prompt rather than call order: the
// number of map calls varies with chunk count (transcript-dependent), so a
// purely positional reply cycle would misalign whenever that count isn't a
// multiple of len(replies) apart from the two reduce calls.
type fakeCompleter struct {
	replies []string
	i       int
}

func (f *fakeCompleter) Complete(ctx context.Context, m []llm.Message) (string, error) {
	if len(m) > 0 {
		sys := m[0].Content
		if strings.Contains(sys, "Combine these section summaries") {
			return f.replies[1], nil
		}
		if strings.Contains(sys, "JSON") {
			return f.replies[2], nil
		}
	}
	f.i++
	return f.replies[0], nil
}

func TestSummarizeThenKeyPointsPrefersYtdlpChapters(t *testing.T) {
	cues := []subtitles.Cue{{StartSeconds: 0, Text: "intro"}, {StartSeconds: 108, Text: "titanium frame"}}
	transcript := strings.Repeat("word ", 2000)
	fc := &fakeCompleter{replies: []string{
		"chunk summary",          // map calls
		"Overall prose summary.", // reduce: summary
		`{"key_points":[{"ts":108,"text":"weight drop"}]}`, // key points
	}}
	s := New(fc)

	summary, err := s.SummarizeText(context.Background(), transcript)
	if err != nil {
		t.Fatal(err)
	}
	if summary == "" {
		t.Fatal("empty summary")
	}

	chapters, keyPoints, err := s.KeyPoints(context.Background(), summary, cues, []Chapter{{TS: 0, Title: "Intro", Source: "yt-dlp"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(chapters) != 1 || chapters[0].Source != "yt-dlp" {
		t.Fatalf("expected yt-dlp chapters preserved: %+v", chapters)
	}
	if len(keyPoints) != 1 || keyPoints[0].TS != 108 {
		t.Fatalf("key points: %+v", keyPoints)
	}
}

// TestKeyPointsParsesProseWrappedJSON is the regression test for the extractJSON
// fix: stripFences only trims a fence at exact string boundaries, so a reply
// that prefixes prose before the ```json fence used to fail json.Unmarshal
// silently, leaving key_points/chapters empty. extractJSON instead slices
// from the first '{' to the last '}', which recovers the object regardless
// of what surrounds it.
func TestKeyPointsParsesProseWrappedJSON(t *testing.T) {
	cues := []subtitles.Cue{{StartSeconds: 0, Text: "intro"}, {StartSeconds: 108, Text: "titanium frame"}}
	fc := &fakeCompleter{replies: []string{
		"chunk summary",          // (unused here)
		"Overall prose summary.", // (unused here)
		"Here are the key points:\n```json\n{\"key_points\":[{\"ts\":108,\"text\":\"x\"}]}\n```", // key points, prose-wrapped
	}}
	s := New(fc)
	_, keyPoints, err := s.KeyPoints(context.Background(), "Overall prose summary.", cues, []Chapter{{TS: 0, Title: "Intro", Source: "yt-dlp"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(keyPoints) != 1 || keyPoints[0].TS != 108 || keyPoints[0].Text != "x" {
		t.Fatalf("expected prose-wrapped JSON to be parsed, got: %+v", keyPoints)
	}
}

func TestClassifyReturnsRawReplyAndSendsAllowedIDs(t *testing.T) {
	var gotSystem, gotUser string
	fc := completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		gotSystem = m[0].Content
		gotUser = m[1].Content
		return " ai \n", nil
	})
	s := New(fc)
	got, err := s.Classify(context.Background(), "GPT-5 is here", "A video about a new model.",
		[]videos.Category{{ID: "ai", Label: "Artificial Intelligence"}, {ID: "news", Label: "News & Current Events"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != " ai \n" {
		t.Fatalf("Classify returned %q, want raw reply unchanged", got)
	}
	if !strings.Contains(gotSystem, "ai (Artificial Intelligence)") || !strings.Contains(gotSystem, "news (News & Current Events)") {
		t.Fatalf("system prompt missing allowed id (label) pairs: %q", gotSystem)
	}
	// The escape hatch is deliberately absent: offering 'uncategorized' as an
	// answer is what filled that bucket in the first place.
	if strings.Contains(gotSystem, "uncategorized") {
		t.Fatalf("system prompt must not offer uncategorized: %q", gotSystem)
	}
	if !strings.Contains(gotSystem, "Always choose the closest match") {
		t.Fatalf("system prompt missing the forced-choice instruction: %q", gotSystem)
	}
	if !strings.Contains(gotSystem, "category id") {
		t.Fatalf("system prompt missing load-bearing substring %q (worker test's fake completer dispatches on it): %q", "category id", gotSystem)
	}
	if !strings.Contains(gotUser, "GPT-5 is here") || !strings.Contains(gotUser, "new model") {
		t.Fatalf("user content missing title/summary: %q", gotUser)
	}
}

func TestClassifyRendersHintsOnePerLine(t *testing.T) {
	var gotSystem string
	fc := completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		gotSystem = m[0].Content
		return "sports", nil
	})
	s := New(fc)
	if _, err := s.Classify(context.Background(), "Is aero worth it?", "A cycling video.",
		[]videos.Category{
			{ID: "sports", Label: "Sports & Fitness", Hint: "cycling, running, gym"},
			{ID: "gaming", Label: "Gaming"},
		}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotSystem, "- sports (Sports & Fitness): cycling, running, gym") {
		t.Fatalf("hinted category not rendered with its hint: %q", gotSystem)
	}
	// An unhinted category stops at the label — no stray separator.
	if !strings.Contains(gotSystem, "- gaming (Gaming)\n") {
		t.Fatalf("unhinted category should render label-only: %q", gotSystem)
	}
	// One per line: the hints carry commas of their own, so a comma-joined list
	// would read as one category per clause.
	if strings.Contains(gotSystem, "), - ") {
		t.Fatalf("categories must be newline-separated, not comma-joined: %q", gotSystem)
	}
}

// completerFunc adapts a func to the Completer interface for tests.
type completerFunc func(context.Context, []llm.Message) (string, error)

func (f completerFunc) Complete(ctx context.Context, m []llm.Message) (string, error) {
	return f(ctx, m)
}

func TestSummarizeText_emptyTranscriptErrors(t *testing.T) {
	s := New(&fakeCompleter{})
	if _, err := s.SummarizeText(context.Background(), ""); err == nil {
		t.Error("want an error for an empty transcript")
	}
}

func TestSummarizeText_mapErrorPropagates(t *testing.T) {
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		return "", errors.New("map boom")
	}))
	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err == nil {
		t.Error("want the map error propagated")
	}
}

func TestSummarizeText_reduceErrorPropagates(t *testing.T) {
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		if strings.Contains(m[0].Content, "Combine these section summaries") {
			return "", errors.New("reduce boom")
		}
		return "chunk summary", nil
	}))
	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err == nil {
		t.Error("want the reduce error propagated")
	}
}

// Thinking is spent per step on purpose: the steps whose answer is a rewrite or
// a lookup turn it off, the ones that have to deduce something keep it. The map
// step is the expensive one to get wrong — it runs once per chunk.
func TestSummarizeText_thinksOnlyOnTheReduce(t *testing.T) {
	var mapThought, reduceThought []bool
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		if strings.Contains(m[0].Content, "Combine these section summaries") {
			reduceThought = append(reduceThought, llm.ThinkingFrom(ctx))
			return "Overall prose summary.", nil
		}
		mapThought = append(mapThought, llm.ThinkingFrom(ctx))
		return "chunk summary", nil
	}))

	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err != nil {
		t.Fatal(err)
	}
	if len(mapThought) == 0 {
		t.Fatal("no map calls were made, so the assertion below proves nothing")
	}
	for i, thought := range mapThought {
		if thought {
			t.Errorf("map call %d reasoned; the chunk summaries are meant to be cheap", i)
		}
	}
	if len(reduceThought) != 1 || !reduceThought[0] {
		t.Errorf("reduce thinking = %v, want exactly one call that reasoned", reduceThought)
	}
}

func TestClassify_doesNotThink(t *testing.T) {
	var thought bool
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		thought = llm.ThinkingFrom(ctx)
		return "science", nil
	}))
	if _, err := s.Classify(context.Background(), "Title", "A summary.", videos.ClassifiableCategories()); err != nil {
		t.Fatal(err)
	}
	if thought {
		t.Error("classify reasoned; picking one id from a list it was just handed should not cost a chain of thought")
	}
}

func TestKeyPoints_thinks(t *testing.T) {
	var thought bool
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		thought = llm.ThinkingFrom(ctx)
		return `{"key_points":[{"ts":0,"text":"intro"}]}`, nil
	}))
	cues := []subtitles.Cue{{StartSeconds: 0, Text: "intro"}}
	if _, _, err := s.KeyPoints(context.Background(), "A summary.", cues, []Chapter{{TS: 0, Title: "Intro", Source: "yt-dlp"}}); err != nil {
		t.Fatal(err)
	}
	if !thought {
		t.Error("key points did not reason; grounding timestamps in the cue index is the fragile step")
	}
}
