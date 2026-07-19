package summarize

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/subtitles"
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

func TestRunProducesThreeArtifactsAndPrefersYtdlpChapters(t *testing.T) {
	cues := []subtitles.Cue{{StartSeconds: 0, Text: "intro"}, {StartSeconds: 108, Text: "titanium frame"}}
	transcript := strings.Repeat("word ", 2000)
	fc := &fakeCompleter{replies: []string{
		"chunk summary",          // map calls
		"Overall prose summary.", // reduce: summary
		`{"key_points":[{"ts":108,"text":"weight drop"}]}`, // reduce: key points
	}}
	s := New(fc)
	got, err := s.Run(context.Background(), transcript, cues, []Chapter{{TS: 0, Title: "Intro", Source: "yt-dlp"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary == "" {
		t.Fatal("empty summary")
	}
	if len(got.Chapters) != 1 || got.Chapters[0].Source != "yt-dlp" {
		t.Fatalf("expected yt-dlp chapters preserved: %+v", got.Chapters)
	}
	if len(got.KeyPoints) != 1 || got.KeyPoints[0].TS != 108 {
		t.Fatalf("key points: %+v", got.KeyPoints)
	}
}
