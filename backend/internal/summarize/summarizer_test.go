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
