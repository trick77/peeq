package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

// fakeAsk is a StreamCompleter that emits fixed deltas, or fails.
type fakeAsk struct {
	deltas   []string
	err      error
	messages []llm.Message
	called   bool
}

func (f *fakeAsk) CompleteStream(_ context.Context, m []llm.Message, onDelta func(string)) (string, error) {
	f.called = true
	f.messages = m
	var b strings.Builder
	for _, d := range f.deltas {
		b.WriteString(d)
		if onDelta != nil {
			onDelta(d)
		}
	}
	if f.err != nil {
		return b.String(), f.err
	}
	return b.String(), nil
}

// answerDeps seeds a video with one indexed chunk, so retrieval has something.
func answerDeps(t *testing.T) (Deps, *fakeAsk) {
	t.Helper()
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{
		ID: "v1", URL: "u1", Title: "Why Athletes Cramp", ChannelName: "Attia",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "the electrolytes you replace matter", StartSeconds: 872},
	})
	ask := &fakeAsk{deltas: []string{"Yes — ", "Attia covers it[1]."}}
	deps.Ask = ask
	return deps, ask
}

// events splits an SSE body into (name, data) pairs.
func events(t *testing.T, body string) [][2]string {
	t.Helper()
	var out [][2]string
	for _, frame := range strings.Split(body, "\n\n") {
		var name, data string
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if name != "" {
			out = append(out, [2]string{name, data})
		}
	}
	return out
}

func names(evs [][2]string) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e[0])
	}
	return out
}

func TestAnswerStreamsSourcesThenTokensThenDone(t *testing.T) {
	deps, ask := answerDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q, want an event stream", ct)
	}
	got := names(events(t, rec.Body.String()))
	want := []string{"sources", "token", "token", "done"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("frames = %v, want %v", got, want)
	}
	if !ask.called {
		t.Error("the model was never called")
	}
	// The citation number in the prompt has to mean the same passage the UI
	// will resolve it to, or every [n] points somewhere else.
	prompt := ask.messages[len(ask.messages)-1].Content
	if !strings.Contains(prompt, "[1]") || !strings.Contains(prompt, "Why Athletes Cramp") {
		t.Errorf("excerpt block did not number the source: %q", prompt)
	}
	if !strings.Contains(rec.Body.String(), `"n":1`) {
		t.Errorf("sources frame did not carry the same numbering: %s", rec.Body.String())
	}
}

// The most important honesty case must not depend on the model choosing to
// admit it — so it never reaches the model at all.
func TestAnswerWithNoResultsSaysSoWithoutCallingTheModel(t *testing.T) {
	deps, ask := answerDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=unicornhusbandry", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ask.called {
		t.Error("the model was called for a query that retrieved nothing")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Nothing in your library covers that") {
		t.Errorf("no honest answer in: %s", body)
	}
	if got := names(events(t, body)); strings.Join(got, ",") != "sources,token,done" {
		t.Errorf("frames = %v", got)
	}
}

// Chat down must degrade to citations, never to an error status — the sources
// are still useful and the results below are unaffected.
func TestAnswerWithoutChatStillSendsSources(t *testing.T) {
	deps, _ := answerDeps(t)
	deps.Ask = nil
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a missing chat client is not an error", rec.Code)
	}
	got := names(events(t, rec.Body.String()))
	if strings.Join(got, ",") != "sources,error,done" {
		t.Fatalf("frames = %v, want sources,error,done", got)
	}
	if !strings.Contains(rec.Body.String(), `"video_id":"v1"`) {
		t.Errorf("sources were not sent: %s", rec.Body.String())
	}
}

// A mid-stream failure cannot become an HTTP status — the headers are long
// gone — so it has to arrive as an event, after whatever text did make it.
func TestAnswerMidStreamFailureKeepsEarlierTokens(t *testing.T) {
	deps, ask := answerDeps(t)
	ask.deltas = []string{"Yes — Attia"}
	ask.err = errors.New("endpoint died")
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — headers were already sent", rec.Code)
	}
	got := names(events(t, rec.Body.String()))
	if strings.Join(got, ",") != "sources,token,error,done" {
		t.Fatalf("frames = %v", got)
	}
	if !strings.Contains(rec.Body.String(), "Yes — Attia") {
		t.Errorf("the text that did arrive was dropped: %s", rec.Body.String())
	}
}

// A clean stream that carried no content is the one failure that arrives with
// err == nil: the endpoint can finish on "length" after spending the whole
// token budget reasoning. Reporting it is what keeps the panel from rendering a
// header and a source list with nothing between them.
func TestAnswerWithEmptyCompletionReportsAnError(t *testing.T) {
	deps, ask := answerDeps(t)
	ask.deltas = nil
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := names(events(t, rec.Body.String())); strings.Join(got, ",") != "sources,error,done" {
		t.Fatalf("frames = %v, want sources,error,done", got)
	}
}

func TestAnswerBlankQueryStreamsNothing(t *testing.T) {
	deps, ask := answerDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ask.called {
		t.Error("the model was called for a blank query")
	}
	if got := names(events(t, rec.Body.String())); strings.Join(got, ",") != "sources,done" {
		t.Errorf("frames = %v, want sources,done", got)
	}
}

// The one case that must refuse rather than degrade, and therefore the only one
// that can still set a status.
func TestAnswerWithoutRagIs503(t *testing.T) {
	deps, _ := answerDeps(t)
	deps.Rag = nil
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	rec := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestAnswerRequiresAuth(t *testing.T) {
	deps, _ := answerDeps(t)
	h := New(deps)
	// No cookie at all — doReq's helper assumes one, so build the request here.
	req := httptest.NewRequest(http.MethodGet, "/api/search/answer?q=electrolytes", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("status = %d, want a redirect or 401", rec.Code)
	}
}

// One video should not be the entire evidence set for a question the library
// answers from several angles.
func TestAnswerCapsSourcesPerVideo(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "chatty"}); err != nil {
		t.Fatal(err)
	}
	rows := make([]rag.ChunkRow, 0, 10)
	for i := range 10 {
		rows = append(rows, rag.ChunkRow{
			Ordinal: i, Text: "electrolytes again", StartSeconds: i * 600,
		})
	}
	seedChunks(t, ragStore, "v1", rows)
	ask := &fakeAsk{deltas: []string{"x"}}
	deps.Ask = ask
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil)

	prompt := ask.messages[len(ask.messages)-1].Content
	if n := strings.Count(prompt, `"chatty"`); n > answerMaxSourcesPerVideo {
		t.Errorf("one video contributed %d excerpts, want at most %d", n, answerMaxSourcesPerVideo)
	}
}

// Ask renders its moments from these sources now, not from a second
// /api/search request, so a source has to carry what a result card shows: the
// match-centred preview, with the keyword lane's highlight markers intact.
func TestAnswerSourcesCarryTheMatchSnippet(t *testing.T) {
	deps, _ := answerDeps(t)
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	body := doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil).Body.String()

	frame := firstEvent(t, body, "sources")
	var got struct {
		Sources []answerSource `json:"sources"`
	}
	if err := json.Unmarshal([]byte(frame), &got); err != nil {
		t.Fatalf("sources frame: %v — %s", err, frame)
	}
	if len(got.Sources) == 0 {
		t.Fatalf("no sources: %s", frame)
	}
	snip := got.Sources[0].Snippet
	if !strings.Contains(snip, "electrolytes") {
		t.Errorf("snippet %q does not show the matched term", snip)
	}
	if !strings.Contains(snip, rag.HighlightStart) {
		t.Errorf("the keyword lane's highlight was lost on the way to the card: %q", snip)
	}
}

// One video, several qualifying passages: three sources, but the video record
// itself travels once. Repeating it per source would put the same title,
// channel and duration on the wire three times.
func TestAnswerVideosAppearOncePerVideo(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "v1", URL: "u", Title: "chatty"}); err != nil {
		t.Fatal(err)
	}
	rows := make([]rag.ChunkRow, 0, 5)
	for i := range 5 {
		rows = append(rows, rag.ChunkRow{Ordinal: i, Text: "electrolytes again", StartSeconds: i * 600})
	}
	seedChunks(t, ragStore, "v1", rows)
	deps.Ask = &fakeAsk{deltas: []string{"x"}}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	frame := firstEvent(t, doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil).Body.String(), "sources")

	var got struct {
		Sources []answerSource `json:"sources"`
		Videos  []answerVideo  `json:"videos"`
	}
	if err := json.Unmarshal([]byte(frame), &got); err != nil {
		t.Fatalf("sources frame: %v — %s", err, frame)
	}
	if len(got.Sources) < 2 {
		t.Fatalf("expected several sources from one chatty video, got %d", len(got.Sources))
	}
	if len(got.Videos) != 1 {
		t.Errorf("one video produced %d video records, want 1", len(got.Videos))
	}
}

// The video record on the wire is answerVideo, not videoDTO. This pins that
// down: videoDTO carries the whole summary, the chapter and key-point blobs and
// the probe set, and twelve of those would dominate a stream whose point is to
// start fast.
func TestAnswerVideosOmitTheSummaryText(t *testing.T) {
	deps, _, ragStore := searchTestDepsWithStores(t)
	if err := deps.Videos.Upsert(videos.Video{
		ID: "v1", URL: "u", Title: "Why Athletes Cramp",
		Summary: "SENTINEL-SUMMARY-TEXT", Description: "SENTINEL-DESCRIPTION",
	}); err != nil {
		t.Fatal(err)
	}
	seedChunks(t, ragStore, "v1", []rag.ChunkRow{
		{Ordinal: 0, Text: "the electrolytes you replace", StartSeconds: 10},
	})
	deps.Ask = &fakeAsk{deltas: []string{"x"}}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	frame := firstEvent(t, doReq(t, h, cookie, http.MethodGet, "/api/search/answer?q=electrolytes", nil).Body.String(), "sources")

	for _, leaked := range []string{"SENTINEL-SUMMARY-TEXT", "SENTINEL-DESCRIPTION"} {
		if strings.Contains(frame, leaked) {
			t.Errorf("%s reached the wire — someone swapped answerVideo for videoDTO: %s", leaked, frame)
		}
	}
}

// firstEvent returns the data of the first frame with the given event name.
func firstEvent(t *testing.T, body, name string) string {
	t.Helper()
	for _, e := range events(t, body) {
		if e[0] == name {
			return e[1]
		}
	}
	t.Fatalf("no %q frame in: %s", name, body)
	return ""
}
