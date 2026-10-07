package summarize

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/subtitles"
)

func manyCues(n int) []subtitles.Cue {
	cues := make([]subtitles.Cue, n)
	for i := range cues {
		cues[i] = subtitles.Cue{StartSeconds: i * 5, Text: fmt.Sprintf("line %d with a few more words in it", i)}
	}
	return cues
}

// A transcript inside the budget is one call at the model's default reasoning:
// nobody waits on it, and it is prose a reader sees, so it is no short gate.
// The user message carries what the prompt promises: title, length, word
// target and a clock-stamped cue index the model copies timestamps from.
func TestInDepth_singlePassIsOneCallWithClockStampedCues(t *testing.T) {
	var calls int
	var sys, user string
	var reasoning llm.Reasoning
	var gate bool
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		calls++
		sys, user = m[0].Content, m[1].Content
		reasoning, gate = llm.ReasoningFor(ctx), llm.ShortGateFrom(ctx)
		return "  Lead.\n\n### Point [1:05]\n\nBody.\n", nil
	}))
	cues := []subtitles.Cue{{StartSeconds: 0, Text: "hello"}, {StartSeconds: 65, Text: "the point"}, {StartSeconds: 3725, Text: "late"}}

	got, err := s.InDepth(context.Background(), "A title", 1800, cues)
	if err != nil {
		t.Fatal(err)
	}
	if got != "Lead.\n\n### Point [1:05]\n\nBody." {
		t.Fatalf("in-depth = %q, want the reply trimmed", got)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	if reasoning != llm.ReasoningDefault || gate {
		t.Fatalf("reasoning=%q gate=%v, want default reasoning and no short gate", reasoning, gate)
	}
	if sys != videoSystemPrompt {
		t.Fatalf("system prompt = %q, want the shared video prompt", sys)
	}
	if !strings.HasSuffix(user, inDepthTask+"\nWORD TARGET: about 750 words") {
		t.Fatalf("user message does not end with the task and word target:\n%s", user)
	}
	for _, want := range []string{"TITLE: A title", "LENGTH: 30:00", "[0:00] hello", "[1:05] the point", "[1:02:05] late"} {
		if !strings.Contains(user, want) {
			t.Errorf("user message missing %q:\n%s", want, user)
		}
	}
}

// The step fakes route on these phrases. An in-depth prompt carrying one would
// be answered as a summary, a category or key-points JSON, and the shared
// system prompt must carry none at all, its own task's included.
func TestInDepth_promptsAvoidTheOtherStepsMarkers(t *testing.T) {
	for name, p := range map[string]string{"single": inDepthTask, "map": inDepthSectionPrompt, "merge": inDepthMergePrompt} {
		for _, marker := range []string{"cohesive summary", "category id", "JSON"} {
			if strings.Contains(p, marker) {
				t.Errorf("%s prompt contains %q", name, marker)
			}
		}
	}
	for _, marker := range []string{"cohesive summary", "in-depth summary", "category id", "JSON"} {
		if strings.Contains(videoSystemPrompt, marker) {
			t.Errorf("shared system prompt contains %q", marker)
		}
	}
}

// The point of the shared prefix: the summary and the in-depth call open with
// the same bytes up to the task, so the second one's prefill is a cache read.
func TestSummaryAndInDepthShareTheirPrefix(t *testing.T) {
	var msgs [][]llm.Message
	s := New(completerFunc(func(_ context.Context, m []llm.Message) (string, error) {
		msgs = append(msgs, m)
		return "text", nil
	}))
	cues := manyCues(20)
	p := subtitles.Parsed{Transcript: "unused on this path", Cues: cues}
	if _, err := s.SummarizeVideo(context.Background(), "T", 900, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InDepth(context.Background(), "T", 900, cues); err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("calls = %d, want 2", len(msgs))
	}
	prefix := videoPrefix("T", 900, s.videoChunks(cues)[0].Text)
	for i, m := range msgs {
		if m[0].Content != videoSystemPrompt {
			t.Errorf("call %d system = %q, want the shared one", i, m[0].Content)
		}
		if !strings.HasPrefix(m[1].Content, prefix) {
			t.Errorf("call %d user message does not open with the shared prefix", i)
		}
	}
	if !strings.HasSuffix(msgs[0][1].Content, summaryTask) {
		t.Errorf("summary call does not end with its task")
	}
}

// Past the budget the summary keeps its coarse map-reduce over the plain
// transcript; the prefix only pays on a single call.
func TestSummarizeVideo_longVideoFallsBackToTheCoarsePath(t *testing.T) {
	var reduce int
	s := New(completerFunc(func(_ context.Context, m []llm.Message) (string, error) {
		if m[0].Content == videoSystemPrompt {
			t.Error("a multi-chunk video took the shared-prefix call")
		}
		if strings.Contains(m[0].Content, "cohesive summary") {
			reduce++
		}
		return "section", nil
	}), WithSummaryChunkTokens(300))
	cues := manyCues(400)
	words := make([]string, len(cues))
	for i, c := range cues {
		words[i] = c.Text
	}
	if _, err := s.SummarizeVideo(context.Background(), "T", 7200, subtitles.Parsed{Transcript: strings.Join(words, " "), Cues: cues}); err != nil {
		t.Fatal(err)
	}
	if reduce != 1 {
		t.Fatalf("reduce calls = %d, want 1", reduce)
	}
}

// Past the budget: a map over the parts writing notes in the same format, then
// one merge call that writes the reader's text from them.
func TestInDepth_longVideoMapsThenMerges(t *testing.T) {
	var maps int
	var mergeInput string
	s := New(completerFunc(func(_ context.Context, m []llm.Message) (string, error) {
		switch m[0].Content {
		case inDepthSectionPrompt:
			maps++
			return fmt.Sprintf("### Part %d [0:00]\n\nnotes", maps), nil
		case inDepthMergePrompt:
			mergeInput = m[1].Content
			return "Final.", nil
		}
		return "", errors.New("unexpected prompt")
	}), WithSummaryChunkTokens(300))

	got, err := s.InDepth(context.Background(), "T", 7200, manyCues(400))
	if err != nil {
		t.Fatal(err)
	}
	if got != "Final." {
		t.Fatalf("got %q, want the merge's answer", got)
	}
	if maps < 2 {
		t.Fatalf("map calls = %d, want more than one", maps)
	}
	if !strings.Contains(mergeInput, "### Part 1") || !strings.Contains(mergeInput, fmt.Sprintf("### Part %d", maps)) {
		t.Fatalf("merge input lacks the part notes:\n%s", mergeInput)
	}
	if !strings.Contains(mergeInput, "about 900 words") {
		t.Fatalf("merge input lacks the word target:\n%s", mergeInput)
	}
}

func TestInDepth_failures(t *testing.T) {
	boom := completerFunc(func(context.Context, []llm.Message) (string, error) { return "", errors.New("boom") })
	blank := completerFunc(func(context.Context, []llm.Message) (string, error) { return " \n", nil })
	blankMap := completerFunc(func(_ context.Context, m []llm.Message) (string, error) {
		if m[0].Content == inDepthMergePrompt {
			return "Final.", nil
		}
		return "", nil
	})
	for name, tc := range map[string]struct {
		s    *Summarizer
		cues []subtitles.Cue
	}{
		"no cues":        {New(blank), nil},
		"call error":     {New(boom), manyCues(3)},
		"empty answer":   {New(blank), manyCues(3)},
		"empty map part": {New(blankMap, WithSummaryChunkTokens(300)), manyCues(400)},
	} {
		if _, err := tc.s.InDepth(context.Background(), "T", 600, tc.cues); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestInDepth_answerCapOnTheWire(t *testing.T) {
	client, srv := newStubChat(t, "Lead.")
	if _, err := New(client).InDepth(context.Background(), "T", 600, manyCues(3)); err != nil {
		t.Fatal(err)
	}
	if got, ok := srv.Last().MaxTokens(); !ok || got < inDepthMaxAnswerTokens {
		t.Fatalf("wire cap = %d (sent %v), want at least the answer cap %d", got, ok, inDepthMaxAnswerTokens)
	}
}

func TestInDepthWordTarget(t *testing.T) {
	for secs, want := range map[int]int{0: 600, 300: 350, 1800: 750, 3 * 3600: 900} {
		if got := inDepthWordTarget(secs); got != want {
			t.Errorf("inDepthWordTarget(%d) = %d, want %d", secs, got, want)
		}
	}
}
