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
		name       string
		raw        string
		wantOK     bool
		wantTopic  string
		wantIntent string
	}{
		{
			name:       "plain object",
			raw:        `{"topic": "bike geometry", "intent": "inventory"}`,
			wantOK:     true,
			wantTopic:  "bike geometry",
			wantIntent: intentInventory,
		},
		{
			name:       "fenced as json",
			raw:        "```json\n{\"topic\": \"transients\", \"intent\": \"content\"}\n```",
			wantOK:     true,
			wantTopic:  "transients",
			wantIntent: intentContent,
		},
		{
			// Prose either side is the other thing that actually happens.
			name:       "object buried in prose",
			raw:        `Sure! {"topic":"sourdough starter","intent":"inventory"} Hope that helps.`,
			wantOK:     true,
			wantTopic:  "sourdough starter",
			wantIntent: intentInventory,
		},
		{
			// An unrecognized label is not a failure: the topic is still good,
			// and the safe default applies to the branch.
			name:       "unknown intent falls back to content",
			raw:        `{"topic":"head angle","intent":"listing"}`,
			wantOK:     true,
			wantTopic:  "head angle",
			wantIntent: intentContent,
		},
		{
			name:       "newlines and control characters are stripped",
			raw:        "{\"topic\":\"bike\\ngeometry\",\"intent\":\"content\"}",
			wantOK:     true,
			wantTopic:  "bike geometry",
			wantIntent: intentContent,
		},
		{
			// Long enough to be a paraphrase of the question rather than a topic.
			// The topic is dropped; the reply is still parsed.
			name:       "overlong topic is dropped",
			raw:        `{"topic":"` + strings.Repeat("a", understandMaxTopicRunes+1) + `","intent":"content"}`,
			wantOK:     true,
			wantTopic:  "",
			wantIntent: intentContent,
		},
		{name: "not json at all", raw: "I think you mean bike geometry.", wantOK: false},
		{name: "empty", raw: "   ", wantOK: false},
		{name: "malformed json", raw: `{"topic": "bike geometry"`, wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseUnderstanding(tc.raw)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if got.Topic != tc.wantTopic {
				t.Errorf("topic = %q, want %q", got.Topic, tc.wantTopic)
			}
			if got.Intent != tc.wantIntent {
				t.Errorf("intent = %q, want %q", got.Intent, tc.wantIntent)
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
		if u.Intent != intentContent {
			t.Errorf("intent = %q, want the safe default", u.Intent)
		}
	})

	t.Run("call fails", func(t *testing.T) {
		s := &server{understand: &fakeUnderstander{err: errors.New("upstream down")}}
		u, d := s.understandQuery(context.Background(), q)
		if d.status != understandFailed {
			t.Errorf("status = %q, want failed", d.status)
		}
		if u.Topic != "" || u.Intent != intentContent {
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
		reply, _ := json.Marshal(map[string]string{"topic": q, "intent": "content"})
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
		f := &fakeUnderstander{reply: `{"topic":"bike geometry","intent":"inventory"}`}
		s := &server{understand: f}
		u, d := s.understandQuery(context.Background(), q)
		if d.status != understandOK {
			t.Fatalf("status = %q, want ok", d.status)
		}
		if u.Topic != "bike geometry" {
			t.Errorf("topic = %q", u.Topic)
		}
		if u.Intent != intentInventory {
			t.Errorf("intent = %q, want inventory", u.Intent)
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
		reply: `{"topic":"bike geometry","intent":"inventory"}`,
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
	deps.Understand = &fakeUnderstander{reply: `{"topic":"","intent":"content"}`}
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
		reply: `{"topic":"bike geometry","intent":"inventory"}`,
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rec := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil)
	evs := events(t, rec.Body.String())
	if len(evs) == 0 || evs[0][0] != "progress" {
		t.Fatalf("frames = %v, want progress first", names(evs))
	}

	var got struct {
		Phase  string `json:"phase"`
		Topic  string `json:"topic"`
		Intent string `json:"intent"`
	}
	if err := json.Unmarshal([]byte(evs[0][1]), &got); err != nil {
		t.Fatalf("progress payload: %v", err)
	}
	if got.Topic != "bike geometry" {
		t.Errorf("topic = %q", got.Topic)
	}
	if got.Intent != intentInventory {
		t.Errorf("intent = %q", got.Intent)
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
