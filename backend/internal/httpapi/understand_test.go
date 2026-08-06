package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/rag"
)

// fakeUnderstander is a stub Completer for the query-understanding call. It
// records what it was asked and replies with whatever the test set.
type fakeUnderstander struct {
	mu     sync.Mutex
	called bool
	prompt string
	reply  string
	err    error
}

func (f *fakeUnderstander) Complete(_ context.Context, msgs []llm.Message) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.called = true
	for _, m := range msgs {
		if m.Role == "user" {
			f.prompt = m.Content
		}
	}
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

// recordingEmbedder captures the inputs of every Embed call, which is how the
// "both vectors in ONE request" property is asserted. It hands back a distinct
// vector per input so the two semantic lanes cannot be confused for one.
type recordingEmbedder struct {
	mu    sync.Mutex
	calls [][]string
}

func (e *recordingEmbedder) Embed(_ context.Context, inputs []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, append([]string(nil), inputs...))
	out := make([][]float32, len(inputs))
	for i := range inputs {
		out[i] = []float32{float32(i) + 1, 0, 0}
	}
	return out, nil
}

func TestParseUnderstandingReadsTheDocumentedShape(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		wantOK       bool
		wantTopic    string
		wantCounting bool
	}{
		{
			name:         "plain object",
			raw:          `{"topic": "bike geometry", "counting": false}`,
			wantOK:       true,
			wantTopic:    "bike geometry",
			wantCounting: false,
		},
		{
			name:         "fenced as json",
			raw:          "```json\n{\"topic\": \"transients\", \"counting\": false}\n```",
			wantOK:       true,
			wantTopic:    "transients",
			wantCounting: false,
		},
		{
			// Prose either side is the other thing that actually happens.
			name:         "object buried in prose",
			raw:          `Sure! {"topic":"sourdough starter","counting":false} Hope that helps.`,
			wantOK:       true,
			wantTopic:    "sourdough starter",
			wantCounting: false,
		},
		{
			// The one question the label exists for: how many, not what about.
			name:         "a counting question",
			raw:          `{"topic":"","counting":true,"filters":{"watched":"unwatched"}}`,
			wantOK:       true,
			wantTopic:    "",
			wantCounting: true,
		},
		{
			// A wrong TYPE must not fail the whole parse. Declared as a bool it
			// would, taking the topic and every filter beside it down with it —
			// and a short-gate model quoting its boolean is exactly the near-miss
			// this step exists to survive.
			name:         "a non-boolean counting value stays false without failing the parse",
			raw:          `{"topic":"head angle","counting":"inventory"}`,
			wantOK:       true,
			wantTopic:    "head angle",
			wantCounting: false,
		},
		{
			// The one string worth honouring, because a model asked for a boolean
			// will sometimes quote it.
			name:         "a quoted true still counts",
			raw:          `{"topic":"","counting":"true"}`,
			wantOK:       true,
			wantTopic:    "",
			wantCounting: true,
		},
		{
			// A filter must survive a malformed counting value rather than being
			// discarded with it.
			name:         "a bad counting value does not take the filters with it",
			raw:          `{"topic":"ontology","counting":7,"filters":{"watched":"unwatched"}}`,
			wantOK:       true,
			wantTopic:    "ontology",
			wantCounting: false,
		},
		{
			// The label the old intent field used carries no meaning now, and must
			// not somehow revive as a count.
			name:         "the retired intent key is ignored",
			raw:          `{"topic":"head angle","intent":"inventory"}`,
			wantOK:       true,
			wantTopic:    "head angle",
			wantCounting: false,
		},
		{
			name:         "newlines and control characters are stripped",
			raw:          "{\"topic\":\"bike\\ngeometry\",\"counting\":false}",
			wantOK:       true,
			wantTopic:    "bike geometry",
			wantCounting: false,
		},
		{
			// Long enough to be a paraphrase of the question rather than a topic.
			// The topic is dropped; the reply is still parsed.
			name:         "overlong topic is dropped",
			raw:          `{"topic":"` + strings.Repeat("a", understandMaxTopicRunes+1) + `","counting":false}`,
			wantOK:       true,
			wantTopic:    "",
			wantCounting: false,
		},
		{name: "not json at all", raw: "I think you mean bike geometry.", wantOK: false},
		{name: "empty", raw: "   ", wantOK: false},
		{name: "malformed json", raw: `{"topic": "bike geometry"`, wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _, ok := parseUnderstanding(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got.Topic != tc.wantTopic {
				t.Errorf("topic = %q, want %q", got.Topic, tc.wantTopic)
			}
			if got.Counting != tc.wantCounting {
				t.Errorf("counting = %v, want %v", got.Counting, tc.wantCounting)
			}
		})
	}
}

func TestUnderstandQueryDegradesToTheRawQuestion(t *testing.T) {
	const q = "what material about bike geometry do we have"

	t.Run("no understander wired", func(t *testing.T) {
		s := &server{}
		u, d := s.understandQuery(context.Background(), q)
		if d.status != understandSkipped {
			t.Errorf("status = %q, want skipped", d.status)
		}
		if u.Topic != "" {
			t.Errorf("topic = %q, want empty", u.Topic)
		}
		if u.Counting {
			t.Errorf("counting = true — the safe default is no count")
		}
	})

	t.Run("call fails", func(t *testing.T) {
		s := &server{understand: &fakeUnderstander{err: errors.New("upstream down")}}
		u, d := s.understandQuery(context.Background(), q)
		if d.status != understandFailed {
			t.Errorf("status = %q, want failed", d.status)
		}
		if u.Topic != "" || u.Counting {
			t.Errorf("a failed call must degrade to the raw question, got %+v", u)
		}
	})

	t.Run("unparseable reply", func(t *testing.T) {
		s := &server{understand: &fakeUnderstander{reply: "bike geometry, probably"}}
		_, d := s.understandQuery(context.Background(), q)
		if d.status != understandFailed {
			t.Errorf("status = %q, want failed", d.status)
		}
	})

	t.Run("topic that merely repeats the question is a noop", func(t *testing.T) {
		reply, _ := json.Marshal(map[string]any{"topic": q, "counting": false})
		s := &server{understand: &fakeUnderstander{reply: string(reply)}}
		u, d := s.understandQuery(context.Background(), q)
		if d.status != understandNoop {
			t.Errorf("status = %q, want noop", d.status)
		}
		if u.Topic != "" {
			t.Errorf("topic = %q — a restated question must not open a lane", u.Topic)
		}
	})

	t.Run("good reply", func(t *testing.T) {
		f := &fakeUnderstander{reply: `{"topic":"bike geometry","counting":false}`}
		s := &server{understand: f}
		u, d := s.understandQuery(context.Background(), q)
		if d.status != understandOK {
			t.Fatalf("status = %q, want ok", d.status)
		}
		if u.Topic != "bike geometry" {
			t.Errorf("topic = %q", u.Topic)
		}
		if u.Counting {
			t.Errorf("counting = true — the reply did not ask for a count")
		}
		if !strings.Contains(f.prompt, q) {
			t.Errorf("the question never reached the model: %q", f.prompt)
		}
	})
}

// The whole point of the step: the framing word must not be what gets searched
// for, and the raw question must still be searched for as well.
func TestAnswerEmbedsBothTheQuestionAndItsTopicInOneCall(t *testing.T) {
	deps, _ := answerDeps(t)
	emb := &recordingEmbedder{}
	deps.Embedder = emb
	deps.Understand = &fakeUnderstander{
		reply: `{"topic":"bike geometry","counting":false}`,
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet,
		"/api/search/answer?q="+strings.ReplaceAll("what material about bike geometry do we have", " ", "+"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	if len(emb.calls) != 1 {
		t.Fatalf("embed calls = %d, want exactly 1 — the topic must not cost a round-trip", len(emb.calls))
	}
	got := emb.calls[0]
	if len(got) != 2 {
		t.Fatalf("embed inputs = %v, want the raw question and its topic", got)
	}
	if !strings.Contains(got[0], "material") {
		t.Errorf("input[0] = %q, want the raw question kept intact", got[0])
	}
	if got[1] != "bike geometry" {
		t.Errorf("input[1] = %q, want the extracted topic", got[1])
	}
}

// A question with no framing to strip must cost exactly what it costs today:
// one vector, one lane.
func TestAnswerWithoutATopicEmbedsOnlyTheQuestion(t *testing.T) {
	deps, _ := answerDeps(t)
	emb := &recordingEmbedder{}
	deps.Embedder = emb
	deps.Understand = &fakeUnderstander{reply: `{"topic":"","counting":false}`}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(emb.calls) != 1 || len(emb.calls[0]) != 1 {
		t.Fatalf("embed inputs = %v, want just the question", emb.calls)
	}
}

// The progress frame is what stops the spinner claiming the library is being
// searched a second before it is, and it carries the topic so a bad rewrite is
// visible rather than silent.
func TestAnswerProgressFrameCarriesTheUnderstoodQuery(t *testing.T) {
	deps, _ := answerDeps(t)
	deps.Understand = &fakeUnderstander{
		reply: `{"topic":"bike geometry","counting":true}`,
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil)
	evs := events(t, rec.Body.String())
	if len(evs) == 0 || evs[0][0] != "progress" {
		t.Fatalf("frames = %v, want progress first", names(evs))
	}

	var got struct {
		Phase    string `json:"phase"`
		Topic    string `json:"topic"`
		Counting bool   `json:"counting"`
	}
	if err := json.Unmarshal([]byte(evs[0][1]), &got); err != nil {
		t.Fatalf("progress payload: %v", err)
	}
	if got.Topic != "bike geometry" {
		t.Errorf("topic = %q", got.Topic)
	}
	if !got.Counting {
		t.Error("counting did not reach the progress frame")
	}
	if got.Phase != "retrieving" {
		t.Errorf("phase = %q", got.Phase)
	}
}

// The attribution is how "did the rewrite earn its keep" gets answered, so it
// has to key on something unique. A chapter chunk carries the transcript of its
// own span, so two different chunks of one video share a start second — and a
// lane holding the OTHER rendering must not be credited for the passage the
// answer actually read.
func TestAttributionKeysOnTheChunkNotTheSecond(t *testing.T) {
	read := []rag.Hit{{VideoID: "v1", Ordinal: 7, StartSeconds: 872}}
	lanes := []rag.Lane{
		// A keyword rung that found the very chunk that was read.
		{Hits: []rag.Hit{{VideoID: "v1", Ordinal: 7, StartSeconds: 872}}, Weight: rag.WeightKeywordStrict},
		// The raw vector lane, holding the CHAPTER rendering of the same moment:
		// same video, same second, different chunk. It did not find what was read.
		{Hits: []rag.Hit{{VideoID: "v1", Ordinal: 41, StartSeconds: 872}}, Weight: rag.WeightSemantic},
		// The topic lane, which did.
		{Hits: []rag.Hit{{VideoID: "v1", Ordinal: 7, StartSeconds: 872}}, Weight: rag.WeightSemanticTopic},
	}
	d := askDiag{rawLane: 1, topicLane: 2}
	d.attribute(lanes, read)

	if d.excerpts != 1 {
		t.Fatalf("excerpts = %d, want 1", d.excerpts)
	}
	if d.fromRaw != 0 {
		t.Errorf("fromRaw = %d — a different chunk at the same second is not the same passage", d.fromRaw)
	}
	if d.fromTopic != 1 {
		t.Errorf("fromTopic = %d, want 1", d.fromTopic)
	}
	if d.fromKeyword != 1 {
		t.Errorf("fromKeyword = %d, want 1", d.fromKeyword)
	}
}

// The topic is a RETRIEVAL input and nothing else. It is stripped down to what
// would appear in a video about the subject, which throws away everything that
// says what kind of answer is wanted — "what material do we have on X" and "how
// does X work" reduce to the same topic and want different answers. So the model
// must be handed the sentence the reader actually wrote.
func TestTheModelSeesTheWholeQuestionNotTheTopic(t *testing.T) {
	deps, ask := answerDeps(t)
	deps.Understand = &fakeUnderstander{
		reply: `{"topic":"electrolytes","counting":false}`,
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	// Phrased so retrieval actually finds the seeded passage — with no sources
	// the model is never called, and the assertion below would pass vacuously.
	const q = "what material about electrolytes do we have"
	rec := doReq(t, h, cookie, http.MethodGet,
		"/api/search/answer?q="+strings.ReplaceAll(q, " ", "+"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !ask.called {
		t.Fatal("the model was never called")
	}

	var prompt string
	for _, m := range ask.messages {
		if m.Role == "user" {
			prompt = m.Content
		}
	}
	if !strings.Contains(prompt, q) {
		t.Errorf("the model must see the whole question, got: %q", prompt)
	}
	// Not a style point: the reduced query reaching the prompt would quietly
	// change what the answer is written to address.
	if strings.Contains(prompt, "Question: electrolytes\n") {
		t.Errorf("the reduced query reached the answer prompt: %q", prompt)
	}
}

// Coverage is a UNION, not a vote, so one lane is enough to seat a video in
// "Also in your library". The rewrite's safety argument — a bad topic is
// outvoted by the lanes that did not change — holds at fusion and not here, so
// the topic lane stays out until the rewrite has been measured.
func TestCoverageExcludesTheTopicLane(t *testing.T) {
	lanes := []rag.Lane{
		{Hits: []rag.Hit{{VideoID: "kept", Ordinal: 1}}, Weight: rag.WeightKeywordStrict},
		{Hits: []rag.Hit{{VideoID: "raw", Ordinal: 2}}, Weight: rag.WeightSemantic},
		{Hits: []rag.Hit{{VideoID: "from-topic", Ordinal: 3}}, Weight: rag.WeightSemanticTopic},
		{Hits: []rag.Hit{{VideoID: "floor", Ordinal: 4}}, Weight: rag.WeightKeywordAny},
	}
	got := relevantVideos(lanes, 2)

	if !got["kept"] || !got["raw"] {
		t.Errorf("the unchanged lanes must still seat their videos: %v", got)
	}
	if got["from-topic"] {
		t.Error("a video only the topic lane found must not reach the coverage list")
	}
	if got["floor"] {
		t.Error("the recall floor is still out, as #350 established")
	}

	// -1 is "no topic lane ran", and must not silently drop lane 0.
	if all := relevantVideos(lanes, -1); !all["from-topic"] {
		t.Error("with no topic lane to exclude, every lane above the floor counts")
	}
}

// A path with no excerpts to attribute must not read as "the lanes contributed
// nothing" — those are different facts.
func TestUnattributedDiagIsNotZero(t *testing.T) {
	d := askDiag{}
	if d.attributed {
		t.Error("a diag nobody attributed must not claim to be attributed")
	}
	d.attribute(nil, nil)
	if !d.attributed {
		t.Error("attribute must mark the diag even when there is nothing to count")
	}
}

// Understanding is optional in the same way chat is: without it the endpoint
// behaves exactly as it did before the step existed.
func TestAnswerWithoutAnUnderstanderStillAnswers(t *testing.T) {
	deps, ask := answerDeps(t)
	deps.Understand = nil
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !ask.called {
		t.Error("the answer never ran without an understander")
	}
	evs := events(t, rec.Body.String())
	if len(evs) == 0 || evs[0][0] != "progress" {
		t.Fatalf("frames = %v, want progress even when understanding is skipped", names(evs))
	}
	if !strings.Contains(evs[0][1], `"topic":""`) {
		t.Errorf("progress topic should be empty, got %s", evs[0][1])
	}
}
