package mcpserver

import (
	"net/http"
	"strings"
	"testing"
)

// The protocol itself (methods, versions, headers, caps) is kit's
// mcpserver and tested there; these cover what peeq configures.

func TestHandler_requiresTheBearerToken(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := post(t, h, []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`), map[string]string{"Authorization": "Bearer wrong"})
	if r.status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", r.status)
	}
	if r.header.Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", r.header.Get("WWW-Authenticate"))
	}
}

func TestHandler_originMustMatchThePublicURL(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	if r := post(t, h, body, map[string]string{"Origin": "https://evil.example.com"}); r.status != http.StatusForbidden {
		t.Errorf("foreign origin: status = %d, want 403", r.status)
	}
	if r := post(t, h, body, map[string]string{"Origin": "https://peeq.example.com"}); r.status != http.StatusOK {
		t.Errorf("own origin: status = %d, want 200", r.status)
	}
	// Server-to-server callers send no Origin at all.
	if r := post(t, h, body, nil); r.status != http.StatusOK {
		t.Errorf("no origin: status = %d, want 200", r.status)
	}
}

// The origin check runs before auth: a browser page on another origin learns
// nothing, not even whether its token was right.
func TestHandler_foreignOriginIsRefusedBeforeAuth(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := post(t, h, []byte(`{}`), map[string]string{"Origin": "https://evil.example.com", "Authorization": "Bearer nope"})
	if r.status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", r.status)
	}
}

func TestLegacy_initializeNamesPeeq(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := legacyCall(t, h, "initialize", map[string]any{"protocolVersion": "2025-11-25"})
	info, _ := r.body.Result["serverInfo"].(map[string]any)
	if info["name"] != "peeq" || info["version"] != "1.2.3" {
		t.Errorf("serverInfo = %v", info)
	}
	if s, _ := r.body.Result["instructions"].(string); !strings.Contains(s, "peeq") {
		t.Errorf("instructions = %q", s)
	}
}

func TestModern_discover(t *testing.T) {
	r := modernCall(t, newTestHandler(&fakeLibrary{}), "server/discover", nil)
	if s, _ := r.body.Result["instructions"].(string); !strings.Contains(s, "peeq") {
		t.Errorf("instructions = %q", s)
	}
	assertServerInfo(t, r.body.Result)
}

func TestModern_toolsListIsCacheableForAQuarterHour(t *testing.T) {
	r := modernCall(t, newTestHandler(&fakeLibrary{}), "tools/list", nil)
	if r.body.Result["ttlMs"] != float64(900000) {
		t.Errorf("ttlMs = %v", r.body.Result["ttlMs"])
	}
	if got := toolNames(r); strings.Join(got, ",") != "search,get_video,get_transcript,list_youtube_channels" {
		t.Errorf("tools = %v", got)
	}
}

func TestModern_toolCallResultShape(t *testing.T) {
	h := newTestHandler(&fakeLibrary{channels: []Channel{{ID: "UC1", Name: "A", Subscribed: true}}})
	r := modernCall(t, h, "tools/call", map[string]any{"name": "list_youtube_channels", "arguments": map[string]any{}})
	if r.body.Result["resultType"] != "complete" {
		t.Errorf("resultType = %v", r.body.Result["resultType"])
	}
	assertServerInfo(t, r.body.Result)
	if !strings.Contains(toolText(t, r), "A") {
		t.Errorf("text = %q", toolText(t, r))
	}
}

func assertServerInfo(t *testing.T, res map[string]any) {
	t.Helper()
	meta, _ := res["_meta"].(map[string]any)
	info, _ := meta["io.modelcontextprotocol/serverInfo"].(map[string]any)
	if info["name"] != "peeq" || info["version"] != "1.2.3" {
		t.Errorf("_meta serverInfo = %v", res["_meta"])
	}
}

func toolNames(r rpcReply) []string {
	tools, _ := r.body.Result["tools"].([]any)
	var out []string
	for _, tl := range tools {
		m, _ := tl.(map[string]any)
		out = append(out, m["name"].(string))
	}
	return out
}

func TestToolsListCarriesDisplayTitles(t *testing.T) {
	want := map[string]string{
		"search":                "Search your video library",
		"get_video":             "Get a video's details and summaries",
		"get_transcript":        "Read a video's transcript",
		"list_youtube_channels": "List YouTube channels in your library",
	}
	h := newTestHandler(&fakeLibrary{})
	for era, r := range map[string]rpcReply{
		"legacy": legacyCall(t, h, "tools/list", nil),
		"modern": modernCall(t, h, "tools/list", nil),
	} {
		tools, _ := r.body.Result["tools"].([]any)
		if len(tools) != len(want) {
			t.Fatalf("%s: %d tools, want %d", era, len(tools), len(want))
		}
		for _, tl := range tools {
			m, _ := tl.(map[string]any)
			name, _ := m["name"].(string)
			ann, _ := m["annotations"].(map[string]any)
			if m["title"] != want[name] || ann["title"] != want[name] {
				t.Errorf("%s %s: title %v, annotations.title %v, want %q", era, name, m["title"], ann["title"], want[name])
			}
			if ann["readOnlyHint"] != true || ann["openWorldHint"] != false {
				t.Errorf("%s %s: annotations = %v", era, name, ann)
			}
		}
	}
}
