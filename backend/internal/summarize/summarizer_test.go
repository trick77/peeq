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
		if strings.Contains(sys, "cohesive summary") {
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

// A transcript that fits the budget is summarized in a SINGLE call — the whole
// point of the redesign — and that call reasons, because it is the synthesis a
// person actually reads.
func TestSummarizeText_singlePassIsOneCallAndThinks(t *testing.T) {
	var calls int
	var thought []bool
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		calls++
		thought = append(thought, llm.ThinkingFrom(ctx))
		return "Overall prose summary.", nil
	}))
	got, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Overall prose summary." {
		t.Fatalf("summary = %q", got)
	}
	if calls != 1 {
		t.Fatalf("single-pass made %d calls, want exactly 1", calls)
	}
	if len(thought) != 1 || !thought[0] {
		t.Fatalf("single-pass thinking = %v, want one call that reasoned", thought)
	}
}

func TestSummarizeText_singlePassErrorPropagates(t *testing.T) {
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		return "", errors.New("boom")
	}))
	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err == nil {
		t.Error("want the single-pass error propagated")
	}
}

// An empty-but-successful completion (e.g. a call that spent its whole budget
// reasoning and ended on "length") must NOT be stored as a blank summary — it
// errors so the job retries instead. Whitespace-only counts as empty.
func TestSummarizeText_emptySinglePassErrors(t *testing.T) {
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		return "  \n ", nil
	}))
	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err == nil {
		t.Error("want an error when the single-pass summary is empty")
	}
}

func TestSummarizeText_emptyReduceErrors(t *testing.T) {
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		if strings.Contains(m[0].Content, "cohesive summary") {
			return "", nil // reduce yields nothing
		}
		return "section summary", nil
	}), WithSummaryChunkTokens(300))
	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err == nil {
		t.Error("want an error when the reduce summary is empty")
	}
}

// A transcript ABOVE the (here deliberately tiny) budget falls back to coarse
// map-reduce: each big section is condensed thinking-OFF, then one reduce call
// (thinking ON) writes the summary the reader sees.
func TestSummarizeText_coarseFallbackThinksOnlyOnReduce(t *testing.T) {
	var mapThought, reduceThought []bool
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		if strings.Contains(m[0].Content, "cohesive summary") {
			reduceThought = append(reduceThought, llm.ThinkingFrom(ctx))
			return "Overall prose summary.", nil
		}
		mapThought = append(mapThought, llm.ThinkingFrom(ctx))
		return "section summary", nil
	}), WithSummaryChunkTokens(300))

	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err != nil {
		t.Fatal(err)
	}
	if len(mapThought) < 2 {
		t.Fatalf("coarse fallback made %d section calls, want >1 (else it wasn't the map path)", len(mapThought))
	}
	for i, thought := range mapThought {
		if thought {
			t.Errorf("section call %d reasoned; coarse condensing is meant to be cheap", i)
		}
	}
	if len(reduceThought) != 1 || !reduceThought[0] {
		t.Errorf("reduce thinking = %v, want exactly one call that reasoned", reduceThought)
	}
}

func TestSummarizeText_coarseMapErrorPropagates(t *testing.T) {
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		return "", errors.New("map boom")
	}), WithSummaryChunkTokens(300))
	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err == nil {
		t.Error("want the map error propagated")
	}
}

func TestSummarizeText_coarseReduceErrorPropagates(t *testing.T) {
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		if strings.Contains(m[0].Content, "cohesive summary") {
			return "", errors.New("reduce boom")
		}
		return "section summary", nil
	}), WithSummaryChunkTokens(300))
	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err == nil {
		t.Error("want the reduce error propagated")
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

// Key points now runs thinking-OFF: it is an extractive JSON step, and running
// it with reasoning is what once let it spiral to tens of thousands of tokens
// and return nothing. max_tokens counts reasoning too, so a cap alone would
// still hand back empty — disabling thinking is what guarantees output.
func TestKeyPoints_doesNotThink(t *testing.T) {
	var thought bool
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		thought = llm.ThinkingFrom(ctx)
		return `{"key_points":[{"ts":0,"text":"intro"}]}`, nil
	}))
	cues := []subtitles.Cue{{StartSeconds: 0, Text: "intro"}}
	if _, _, err := s.KeyPoints(context.Background(), "A summary.", cues, []Chapter{{TS: 0, Title: "Intro", Source: "yt-dlp"}}); err != nil {
		t.Fatal(err)
	}
	if thought {
		t.Error("key points reasoned; it is an extractive step that must not be allowed to spiral")
	}
}
