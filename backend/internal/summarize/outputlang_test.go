package summarize

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/subtitles"
)

// Captions now come in the video's own language when it has no English track,
// so every call whose text a reader sees has to ask for English: the prompts
// are English, but a model writes in the language of what it reads. Checked on
// the requests, every path: single pass, multi-chunk map and reduce, key points
// with and without chapters, in-depth single pass, map and merge.
func TestReaderFacingCallsAskForEnglish(t *testing.T) {
	var systems []string
	rec := completerFunc(func(_ context.Context, m []llm.Message) (string, error) {
		systems = append(systems, m[0].Content)
		if strings.Contains(m[0].Content, "Output JSON only") {
			return `{"chapters":[],"key_points":[]}`, nil
		}
		return "Lead.", nil
	})
	single := New(rec)
	multi := New(rec, WithSummaryChunkTokens(300))
	cues := manyCues(400)
	ctx := context.Background()

	steps := map[string]func() error{
		"summary single": func() error {
			_, err := single.SummarizeVideo(ctx, "T", 600, subtitles.Parsed{Cues: cues[:20]})
			return err
		},
		"summary multi": func() error {
			_, err := multi.SummarizeText(ctx, strings.Repeat("word ", 2000))
			return err
		},
		"key points": func() error {
			_, _, err := single.KeyPoints(ctx, "S", cues[:20], []Chapter{{TS: 0, Title: "Intro"}})
			return err
		},
		"key points and chapters": func() error {
			_, _, err := single.KeyPoints(ctx, "S", cues[:20], nil)
			return err
		},
		"in-depth single": func() error {
			_, err := single.InDepth(ctx, "T", 600, cues[:20])
			return err
		},
		"in-depth multi": func() error {
			_, err := multi.InDepth(ctx, "T", 3600, cues)
			return err
		},
	}
	for name, run := range steps {
		systems = nil
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(systems) == 0 {
			t.Fatalf("%s: no call made", name)
		}
		for i, s := range systems {
			if !strings.Contains(s, outputEnglish) {
				t.Errorf("%s: call %d does not ask for English: %q", name, i, s)
			}
		}
	}
}
