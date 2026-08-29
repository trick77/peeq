package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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

// Classify is the one step routed to the gate deployment, and this test runs a
// real client at a stub endpoint rather than a fake completer because the choice
// only exists on the wire — a fake sees a context, not a model name.
//
// The summary half is the guard that matters: it is the prose a reader sees, and
// it never takes the gate. The bar for the swap is what the call produces — an
// id or a label — and nothing else; "it reasons shallowly" is true of calls that
// must not move.
func TestClassifyRunsOnTheGateDeploymentAndTheSummaryDoesNot(t *testing.T) {
	var models []string
	var maxTokens []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		models = append(models, body["model"].(string))
		maxTokens = append(maxTokens, body["max_tokens"])
		io.WriteString(w, "data: "+
			`{"choices":[{"delta":{"content":"science","role":"assistant"},"finish_reason":null,"index":0}]}`+"\n\n"+
			"data: "+`{"choices":[{"delta":{"content":null},"finish_reason":"stop","index":0}],"usage":null}`+"\n\n"+
			"data: [DONE]\n\n")
	}))
	defer srv.Close()

	s := New(llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client()))

	if _, err := s.Classify(context.Background(), "A title", "A summary.",
		[]videos.Category{{ID: "science", Label: "Science"}}); err != nil {
		t.Fatal(err)
	}
	// Asserted against ModelFor rather than a literal id. The gate and default
	// deployments hold the same id today, so a literal here would pass for the
	// wrong reason and stop testing anything the moment they are split again.
	if want := llm.ModelFor(llm.ShortGate(context.Background())); models[0] != want {
		t.Fatalf("classify ran on %q, want the gate deployment %q", models[0], want)
	}
	// The cap it went without until now: one id needs a couple of tokens, and an
	// endpoint that starts explaining itself instead had nothing to stop it.
	if got, ok := maxTokens[0].(float64); !ok || int(got) != classifyMaxTokens {
		t.Fatalf("classify max_tokens = %v, want %d", maxTokens[0], classifyMaxTokens)
	}

	if _, err := s.SummarizeText(context.Background(), "a short transcript"); err != nil {
		t.Fatal(err)
	}
	if want := llm.ModelFor(context.Background()); models[1] != want {
		t.Fatalf("the summary ran on %q, want the default deployment %q", models[1], want)
	}
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
			{ID: "gaming", Label: "Gaming"}}); err != nil {
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
// point of the redesign — and that call reasons as deeply as the endpoint
// allows, because it is the synthesis a person actually reads.
func TestSummarizeText_singlePassIsOneCallAtFullEffort(t *testing.T) {
	var calls int
	var efforts []string
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		calls++
		efforts = append(efforts, llm.EffortFor(ctx))
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
	if len(efforts) != 1 || efforts[0] != llm.EffortFor(context.Background()) {
		t.Fatalf("single-pass efforts = %v, want one call at the package default", efforts)
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
// map-reduce: several section calls, then one reduce call that writes the
// summary the reader sees. Neither stage asks for less reasoning — the map's
// prose is what the reduce writes from, so trimming it would cost the reader's
// summary one step later.
func TestSummarizeText_coarseFallbackRunsBothStagesAtFullEffort(t *testing.T) {
	var mapEfforts, reduceEfforts []string
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		if strings.Contains(m[0].Content, "cohesive summary") {
			reduceEfforts = append(reduceEfforts, llm.EffortFor(ctx))
			return "Overall prose summary.", nil
		}
		mapEfforts = append(mapEfforts, llm.EffortFor(ctx))
		return "section summary", nil
	}), WithSummaryChunkTokens(300))

	if _, err := s.SummarizeText(context.Background(), strings.Repeat("word ", 2000)); err != nil {
		t.Fatal(err)
	}
	if len(mapEfforts) < 2 {
		t.Fatalf("coarse fallback made %d section calls, want >1 (else it wasn't the map path)", len(mapEfforts))
	}
	for i, e := range append(append([]string{}, mapEfforts...), reduceEfforts...) {
		if e != llm.EffortFor(context.Background()) {
			t.Errorf("call %d ran at effort %q, want the package default", i, e)
		}
	}
	if len(reduceEfforts) != 1 {
		t.Errorf("coarse fallback made %d reduce calls, want exactly 1", len(reduceEfforts))
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

// Classify is a short gate: its answer is an id that lands in the Library
// filter, never prose a reader sees. It does NOT ask for shallow reasoning —
// nothing waits on it (the backlog sweep fires it in bulk), and reasoning stays
// cheap on this endpoint even at the default.
func TestClassify_isAShortGateAtDefaultEffort(t *testing.T) {
	var gate bool
	var effort string
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		gate = llm.ShortGateFrom(ctx)
		effort = llm.EffortFor(ctx)
		return "science", nil
	}))
	if _, err := s.Classify(context.Background(), "Title", "A summary.", videos.ClassifiableCategories()); err != nil {
		t.Fatal(err)
	}
	if !gate {
		t.Error("classify did not route as a short gate")
	}
	if effort != llm.EffortFor(context.Background()) {
		t.Errorf("classify ran at effort %q, want the package default", effort)
	}
}

// Key points must NOT be routed as a short gate, whatever its effort: chapter
// titles and key-point text are what a reader sees in the Player. Reasoning can
// no longer be switched off here (it is what once let this call spiral to tens
// of thousands of tokens), so keypointsMaxTokens is the only remaining guard.
func TestKeyPoints_isNotAShortGate(t *testing.T) {
	var gate bool
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		gate = llm.ShortGateFrom(ctx)
		return `{"key_points":[{"ts":0,"text":"intro"}]}`, nil
	}))
	cues := []subtitles.Cue{{StartSeconds: 0, Text: "intro"}}
	if _, _, err := s.KeyPoints(context.Background(), "A summary.", cues, []Chapter{{TS: 0, Title: "Intro", Source: "yt-dlp"}}); err != nil {
		t.Fatal(err)
	}
	if gate {
		t.Error("key points routed as a short gate; its text is what a reader sees in the Player")
	}
}

// A key point is rendered verbatim in the Player's Highlights panel and on the
// share page, so the debris the prompt already forbids is also taken out of the
// reply: a ">>" speaker marker copied out of the captions, a leading bullet, and
// quotes wrapped around the whole line.
func TestKeyPoints_sanitizesModelText(t *testing.T) {
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		return `{"chapters":[{"ts":0,"title":">> Intro"}],` +
			`"key_points":[{"ts":9,"text":"- >>  Explains the \"weight drop\" here."},` +
			`{"ts":20,"text":"\"A quoted line on its own.\""}]}`, nil
	}))
	cues := []subtitles.Cue{{StartSeconds: 9, Text: "intro"}}

	chapters, keyPoints, err := s.KeyPoints(context.Background(), "A summary.", cues, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(keyPoints) != 2 {
		t.Fatalf("expected 2 key points, got %+v", keyPoints)
	}
	// The inner quotes around "weight drop" survive: a quoted phrase inside the
	// sentence is the point, only a wrapping pair is stripped.
	if got, want := keyPoints[0].Text, `Explains the "weight drop" here.`; got != want {
		t.Errorf("key point 0 = %q, want %q", got, want)
	}
	if got, want := keyPoints[1].Text, "A quoted line on its own."; got != want {
		t.Errorf("key point 1 = %q, want %q", got, want)
	}
	if len(chapters) != 1 || chapters[0].Title != "Intro" {
		t.Errorf("model-written chapter title kept its speaker marker: %+v", chapters)
	}
}

// A Cue can only come from ParseVTT, which takes the speaker markers out at the
// source, so formatCues renders the text it is handed verbatim. What it must not
// do is mangle an operator a coding video's captions spell out.
func TestFormatCues_rendersCueTextVerbatim(t *testing.T) {
	got := formatCues([]subtitles.Cue{
		{StartSeconds: 0, Text: "Welcome back."},
		{StartSeconds: 7, Text: "And now the news over to you."},
		{StartSeconds: 12, Text: "5 > 3 is still true."},
		{StartSeconds: 20, Text: "cout>>x reads a word."},
	})
	want := "0: Welcome back.\n7: And now the news over to you.\n" +
		"12: 5 > 3 is still true.\n20: cout>>x reads a word.\n"
	if got != want {
		t.Errorf("formatCues =\n%q\nwant\n%q", got, want)
	}
}

// SummarizeText takes arbitrary text, not just parser output: the coarse
// map-reduce re-feeds its own section summaries, and model output imitates any
// marker it was shown. So the strip stays here even though ParseVTT now runs
// one too.
func TestSummarizeText_stripsSpeakerMarkers(t *testing.T) {
	var seen string
	s := New(completerFunc(func(ctx context.Context, m []llm.Message) (string, error) {
		seen = m[1].Content
		return "A summary.", nil
	}))
	if _, err := s.SummarizeText(context.Background(), ">> Hello there. >> Hello to you."); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(seen, ">>") {
		t.Errorf("transcript reached the model with speaker markers: %q", seen)
	}
}

// A sentence that both opens and closes on a quoted phrase is not a quoted
// line: stripping its ends would move the quotes onto the wrong words.
func TestSanitizeKeyPointText_keepsQuotesItDidNotWrap(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`"Weight drop" beats "frame stiffness".`, `"Weight drop" beats "frame stiffness".`},
		{`"A quoted line on its own."`, "A quoted line on its own."},
		{"'Don't quote me', he says", "'Don't quote me', he says"},
		{">>Tight marker, no space.", "Tight marker, no space."},
	} {
		if got := sanitizeKeyPointText(tc.in); got != tc.want {
			t.Errorf("sanitizeKeyPointText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
