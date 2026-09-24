package summarize

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/trick77/peeq/internal/llm"
)

// stubChat is a fake chat endpoint that answers every call with one word and
// records each request body, for the tests that assert what goes on the
// wire (model, max_tokens) — a fake completer sees a context, not a request.
type stubChat struct {
	mu     sync.Mutex
	bodies []map[string]any
}

// newStubChat starts the endpoint and returns a real client pointed at it.
func newStubChat(t *testing.T, reply string) (*llm.Client, *stubChat) {
	t.Helper()
	st := &stubChat{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		st.mu.Lock()
		st.bodies = append(st.bodies, body)
		st.mu.Unlock()
		io.WriteString(w, "data: "+
			`{"choices":[{"delta":{"content":"`+reply+`","role":"assistant"},"finish_reason":null,"index":0}]}`+"\n\n"+
			"data: "+`{"choices":[{"delta":{"content":null},"finish_reason":"stop","index":0}],"usage":null}`+"\n\n"+
			"data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	client, err := llm.NewClient(llm.Config{BaseURL: srv.URL}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, st
}

// requests returns the captured bodies so far.
func (st *stubChat) requests() []map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]map[string]any(nil), st.bodies...)
}

// systemPrompt returns the system message of a captured request, or "".
func systemPrompt(body map[string]any) string {
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	first, _ := msgs[0].(map[string]any)
	s, _ := first["content"].(string)
	return s
}
