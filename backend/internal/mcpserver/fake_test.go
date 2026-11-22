package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcp "github.com/trick77/toolkit/mcpserver"
)

const testToken = "test-mcp-token"

// fakeLibrary is a Library whose answers are set per test.
type fakeLibrary struct {
	channels    []Channel
	channelsN   int
	scopes      map[string]ChannelScope
	result      SearchResult
	searchErr   error
	lastQuery   *SearchQuery
	videos      map[string]*Video
	transcripts map[string]*Transcript
	// readErr fails Video and Transcript; readIDs is every id they were asked.
	readErr error
	readIDs []string
	// added answers AddVideos per url; an unlisted url is queued. addedN
	// counts the calls, addedURLs keeps the last call's urls.
	added     map[string]AddedVideo
	addedN    int
	addedURLs []string
	// addShort drops the last outcome: a library breaking the one-per-url
	// contract.
	addShort bool
	// resolveErr fails ResolveChannels; resolveN counts its calls.
	resolveErr error
	resolveN   int
	// gateErr answers SearchableVideo; gateIDs is every id it was asked.
	gateErr error
	gateIDs []string
	// list answers Videos, listErr fails it; lastList is the last query.
	list     VideoList
	listErr  error
	lastList *VideoListQuery
	// totals answers Totals, totalsErr fails it.
	totals    LibraryTotals
	totalsErr error
	// searchable answers SearchableCounts, searchableErr fails it;
	// searchableN counts the calls.
	searchable    map[string]int
	searchableErr error
	searchableN   int
}

func (f *fakeLibrary) Totals(context.Context) (LibraryTotals, error) {
	return f.totals, f.totalsErr
}

func (f *fakeLibrary) SearchableCounts(context.Context) (map[string]int, error) {
	f.searchableN++
	if f.searchableErr != nil {
		return nil, f.searchableErr
	}
	return f.searchable, nil
}

func (f *fakeLibrary) Videos(_ context.Context, q VideoListQuery) (VideoList, error) {
	f.lastList = &q
	return f.list, f.listErr
}

func (f *fakeLibrary) AddVideos(_ context.Context, urls []string) []AddedVideo {
	f.addedN++
	f.addedURLs = urls
	out := make([]AddedVideo, 0, len(urls))
	for _, u := range urls {
		a, ok := f.added[u]
		if !ok {
			a = AddedVideo{Outcome: AddQueued}
		}
		a.URL = u
		out = append(out, a)
	}
	if f.addShort {
		out = out[:len(out)-1]
	}
	return out
}

func (f *fakeLibrary) Channels(context.Context) ([]Channel, error) {
	f.channelsN++
	return f.channels, nil
}

// ResolveChannels counts its calls in resolveN: one directory read each.
func (f *fakeLibrary) ResolveChannels(_ context.Context, names []string) ([]ChannelScope, error) {
	f.resolveN++
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	out := make([]ChannelScope, len(names))
	for i, name := range names {
		s, ok := f.scopes[strings.ToLower(name)]
		if !ok {
			s = ChannelScope{Suggestions: []string{"Veritasium", "Vsauce"}}
		}
		out[i] = s
	}
	return out, nil
}

// SearchableVideo answers gateErr; gateIDs is every id it was asked.
func (f *fakeLibrary) SearchableVideo(_ context.Context, id string) error {
	f.gateIDs = append(f.gateIDs, id)
	return f.gateErr
}

func (f *fakeLibrary) Search(_ context.Context, q SearchQuery) (SearchResult, error) {
	f.lastQuery = &q
	return f.result, f.searchErr
}

func (f *fakeLibrary) Video(_ context.Context, id string) (*Video, error) {
	f.readIDs = append(f.readIDs, id)
	if f.readErr != nil {
		// Wrapped with the id, as the real store does.
		return nil, fmt.Errorf("get video %s: %w", id, f.readErr)
	}
	return f.videos[id], nil
}

// Transcript answers ErrVideoNotFound for an id not in transcripts; an entry
// without cues is a known video without a transcript.
func (f *fakeLibrary) Transcript(_ context.Context, id string) (*Transcript, error) {
	f.readIDs = append(f.readIDs, id)
	if f.readErr != nil {
		return nil, fmt.Errorf("get transcript %s: %w", id, f.readErr)
	}
	t, ok := f.transcripts[id]
	if !ok {
		return nil, ErrVideoNotFound
	}
	return t, nil
}

func newTestHandler(lib Library) http.Handler {
	return New(Deps{Library: lib, Token: testToken, PublicURL: "https://peeq.example.com", Version: "1.2.3"})
}

// modernMeta is the _meta block every 2026-07-28 request carries.
func modernMeta() map[string]any {
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion":    mcp.ModernProtocolVersion,
		"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "0"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}
}

type rpcReply struct {
	status int
	header http.Header
	raw    []byte
	body   struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  map[string]any  `json:"result"`
		Error   *struct {
			Code    int            `json:"code"`
			Message string         `json:"message"`
			Data    map[string]any `json:"data"`
		} `json:"error"`
	}
}

// post sends one raw body with the bearer token and the given headers.
func post(t *testing.T, h http.Handler, body []byte, headers map[string]string) rpcReply {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+testToken)
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := rpcReply{status: rec.Code, header: rec.Header(), raw: rec.Body.Bytes()}
	if len(out.raw) > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(out.raw, &out.body); err != nil && rec.Code < 300 {
			t.Fatalf("decode %s: %v", out.raw, err)
		}
	}
	return out
}

// legacyCall sends a request the way an initialize-era client does.
func legacyCall(t *testing.T, h http.Handler, method string, params map[string]any) rpcReply {
	t.Helper()
	msg := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		msg["params"] = params
	}
	raw, _ := json.Marshal(msg)
	return post(t, h, raw, map[string]string{"MCP-Protocol-Version": "2025-06-18"})
}

// modernCall sends a 2026-07-28 request with consistent headers.
func modernCall(t *testing.T, h http.Handler, method string, params map[string]any) rpcReply {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = modernMeta()
	headers := map[string]string{
		"MCP-Protocol-Version": mcp.ModernProtocolVersion,
		"Mcp-Method":           method,
	}
	if name, ok := params["name"].(string); ok {
		headers["Mcp-Name"] = name
	}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	return post(t, h, raw, headers)
}

// toolText returns the single text content block of a tools/call result.
func toolText(t *testing.T, r rpcReply) string {
	t.Helper()
	if r.body.Error != nil {
		t.Fatalf("tools/call error: %+v", r.body.Error)
	}
	content, _ := r.body.Result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v, want one block", r.body.Result["content"])
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("block type = %v", block["type"])
	}
	s, _ := block["text"].(string)
	return s
}
