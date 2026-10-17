package mcpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler_onlyPostIsAllowed(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPut} {
		req := httptest.NewRequest(method, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405", method, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != http.MethodPost {
			t.Errorf("%s: Allow = %q, want POST", method, got)
		}
	}
}

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

func TestHandler_bodyIsSizeLimited(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	big := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":"` + strings.Repeat("x", maxBodyBytes) + `"}}`
	r := post(t, h, []byte(big), nil)
	if r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", r.status)
	}
}

func TestHandler_parseErrorAndBatch(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := post(t, h, []byte(`{not json`), nil)
	if r.status != http.StatusBadRequest || r.body.Error == nil || r.body.Error.Code != codeParseError {
		t.Errorf("parse error: status %d body %s", r.status, r.raw)
	}
	r = post(t, h, []byte(`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`), nil)
	if r.status != http.StatusBadRequest || r.body.Error == nil || r.body.Error.Code != codeInvalidRequest {
		t.Errorf("batch: status %d body %s", r.status, r.raw)
	}
}

func TestLegacy_initializeIsAnsweredWithoutASession(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := legacyCall(t, h, "initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "c", "version": "1"},
	})
	if r.status != http.StatusOK {
		t.Fatalf("status = %d body %s", r.status, r.raw)
	}
	if r.header.Get("Mcp-Session-Id") != "" {
		t.Error("a stateless server must not mint a session id")
	}
	res := r.body.Result
	if res["protocolVersion"] != "2025-11-25" {
		t.Errorf("protocolVersion = %v, want the requested legacy version echoed", res["protocolVersion"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("capabilities = %v, want tools", res["capabilities"])
	}
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != "peeq" || info["version"] != "1.2.3" {
		t.Errorf("serverInfo = %v", info)
	}
	if s, _ := res["instructions"].(string); !strings.Contains(s, "peeq") {
		t.Errorf("instructions = %q", s)
	}
	if _, ok := res["resultType"]; ok {
		t.Error("a legacy result must not carry resultType")
	}
}

func TestLegacy_initializeWithAnUnknownVersionOffersOurs(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := legacyCall(t, h, "initialize", map[string]any{"protocolVersion": "2024-01-01"})
	if r.body.Result["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want 2025-06-18", r.body.Result["protocolVersion"])
	}
}

func TestLegacy_notificationIsAccepted(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := post(t, h, []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), map[string]string{"MCP-Protocol-Version": "2025-06-18"})
	if r.status != http.StatusAccepted || len(r.raw) != 0 {
		t.Fatalf("status = %d body %q, want 202 and no body", r.status, r.raw)
	}
}

func TestLegacy_ping(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := legacyCall(t, h, "ping", nil)
	if r.status != http.StatusOK || r.body.Result == nil || len(r.body.Result) != 0 {
		t.Fatalf("status = %d body %s, want an empty result", r.status, r.raw)
	}
}

func TestLegacy_toolsListHasNoModernFields(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := legacyCall(t, h, "tools/list", nil)
	if got := toolNames(r); strings.Join(got, ",") != "search,get_video,get_transcript,list_youtube_channels" {
		t.Errorf("tools = %v", got)
	}
	for _, k := range []string{"resultType", "ttlMs", "cacheScope"} {
		if _, ok := r.body.Result[k]; ok {
			t.Errorf("legacy tools/list carries %s", k)
		}
	}
}

func TestLegacy_unknownMethod(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := legacyCall(t, h, "resources/list", nil)
	if r.body.Error == nil || r.body.Error.Code != codeMethodNotFound {
		t.Fatalf("body %s, want -32601", r.raw)
	}
}

func TestModern_discover(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := modernCall(t, h, "server/discover", nil)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d body %s", r.status, r.raw)
	}
	res := r.body.Result
	if res["resultType"] != "complete" {
		t.Errorf("resultType = %v", res["resultType"])
	}
	versions, _ := res["supportedVersions"].([]any)
	if len(versions) == 0 || versions[0] != ModernProtocolVersion {
		t.Errorf("supportedVersions = %v", res["supportedVersions"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("capabilities = %v", res["capabilities"])
	}
	if s, _ := res["instructions"].(string); !strings.Contains(s, "peeq") {
		t.Errorf("instructions = %q", s)
	}
	assertServerInfo(t, res)
}

func TestModern_toolsListIsCacheable(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := modernCall(t, h, "tools/list", nil)
	res := r.body.Result
	if res["resultType"] != "complete" || res["ttlMs"] != float64(900000) || res["cacheScope"] != "private" {
		t.Errorf("result = %v", res)
	}
	assertServerInfo(t, res)
	if got := toolNames(r); strings.Join(got, ",") != "search,get_video,get_transcript,list_youtube_channels" {
		t.Errorf("tools = %v", got)
	}
}

func TestModern_headerMismatch(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	call := func(method string, params map[string]any, headers map[string]string) rpcReply {
		params["_meta"] = modernMeta()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
		return post(t, h, raw, headers)
	}
	cases := map[string]rpcReply{
		"missing version header": call("tools/list", map[string]any{}, map[string]string{"Mcp-Method": "tools/list"}),
		"missing method header":  call("tools/list", map[string]any{}, map[string]string{"MCP-Protocol-Version": ModernProtocolVersion}),
		"wrong method header": call("tools/list", map[string]any{}, map[string]string{
			"MCP-Protocol-Version": ModernProtocolVersion, "Mcp-Method": "tools/call"}),
		"missing name header": call("tools/call", map[string]any{"name": "search"}, map[string]string{
			"MCP-Protocol-Version": ModernProtocolVersion, "Mcp-Method": "tools/call"}),
		"wrong name header": call("tools/call", map[string]any{"name": "search"}, map[string]string{
			"MCP-Protocol-Version": ModernProtocolVersion, "Mcp-Method": "tools/call", "Mcp-Name": "get_video"}),
	}
	for name, r := range cases {
		if r.status != http.StatusBadRequest || r.body.Error == nil || r.body.Error.Code != codeHeaderMismatch {
			t.Errorf("%s: status %d body %s, want 400/-32020", name, r.status, r.raw)
		}
	}
}

// A modern version in the header with no _meta in the body is a mismatch, not
// a legacy request.
func TestModern_versionHeaderWithoutMetaIsAMismatch(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := post(t, h, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), map[string]string{
		"MCP-Protocol-Version": ModernProtocolVersion, "Mcp-Method": "tools/list"})
	if r.status != http.StatusBadRequest || r.body.Error == nil || r.body.Error.Code != codeHeaderMismatch {
		t.Fatalf("status %d body %s", r.status, r.raw)
	}
}

func TestModern_base64NameHeaderIsDecoded(t *testing.T) {
	h := newTestHandler(&fakeLibrary{channels: []Channel{{ID: "UC1", Name: "A"}}})
	params := map[string]any{"name": "list_youtube_channels", "arguments": map[string]any{}, "_meta": modernMeta()}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	r := post(t, h, raw, map[string]string{
		"MCP-Protocol-Version": ModernProtocolVersion,
		"Mcp-Method":           "tools/call",
		"Mcp-Name":             "=?base64?" + base64.StdEncoding.EncodeToString([]byte("list_youtube_channels")) + "?=",
	})
	if r.status != http.StatusOK || r.body.Error != nil {
		t.Fatalf("status %d body %s", r.status, r.raw)
	}
}

func TestModern_unsupportedVersion(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	meta := modernMeta()
	meta["io.modelcontextprotocol/protocolVersion"] = "2099-01-01"
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{"_meta": meta}})
	r := post(t, h, raw, map[string]string{"MCP-Protocol-Version": "2099-01-01", "Mcp-Method": "tools/list"})
	if r.status != http.StatusBadRequest || r.body.Error == nil || r.body.Error.Code != codeUnsupportedVersion {
		t.Fatalf("status %d body %s, want 400/-32022", r.status, r.raw)
	}
	supported, _ := r.body.Error.Data["supported"].([]any)
	if len(supported) == 0 || supported[0] != ModernProtocolVersion {
		t.Errorf("data.supported = %v", r.body.Error.Data["supported"])
	}
	if r.body.Error.Data["requested"] != "2099-01-01" {
		t.Errorf("data.requested = %v", r.body.Error.Data["requested"])
	}
}

func TestModern_unknownMethodIs404(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	for _, m := range []string{"resources/list", "initialize", "ping"} {
		r := modernCall(t, h, m, nil)
		if r.status != http.StatusNotFound || r.body.Error == nil || r.body.Error.Code != codeMethodNotFound {
			t.Errorf("%s: status %d body %s, want 404/-32601", m, r.status, r.raw)
		}
	}
}

// -32602 must go out as HTTP 200: a dual-era client reads any other 4xx as
// "this server is legacy" and falls back.
func TestModern_unknownToolIsInvalidParamsOn200(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	r := modernCall(t, h, "tools/call", map[string]any{"name": "nope", "arguments": map[string]any{}})
	if r.status != http.StatusOK || r.body.Error == nil || r.body.Error.Code != codeInvalidParams {
		t.Fatalf("status %d body %s, want 200/-32602", r.status, r.raw)
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

func TestHandler_responsesAreJSON(t *testing.T) {
	h := newTestHandler(&fakeLibrary{})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":"a","method":"ping"}`)))
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["id"] != "a" || body["jsonrpc"] != "2.0" {
		t.Errorf("body = %v, want the id echoed", body)
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
