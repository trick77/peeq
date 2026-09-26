package summarize

import (
	"testing"

	"github.com/trick77/llmwire/llmwiretest"
	"github.com/trick77/peeq/internal/llm"
)

// stubGateModel is the gate model the stub client is built with: a different
// id from the chat model, so a test can see which one a call reached.
const stubGateModel = llmwiretest.BudgetModel

// newStubChat starts an llmwiretest fake that answers every call with reply
// and records each request, and returns a real client pointed at it — for the
// tests that assert what goes on the wire (model, answer cap), since a fake
// completer sees a context, not a request.
func newStubChat(t *testing.T, reply string) (*llm.Client, *llmwiretest.Server) {
	t.Helper()
	srv := llmwiretest.NewServer(t)
	srv.SetReply(reply)
	client, err := llm.NewClient(llm.Config{
		Model: llmwiretest.ChatModel, GateModel: stubGateModel, Registry: llmwiretest.Registry(),
		BaseURL: srv.URL, APIKey: "k",
	}, srv.Server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, srv
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
