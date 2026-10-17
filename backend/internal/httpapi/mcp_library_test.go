package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/mcpserver"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

const mcpTestToken = "mcp-integration-token"

// mcpFixture stands up the real handler over a seeded library: two subscribed
// channels, three analysed videos, one transcript.
func mcpFixture(t *testing.T, embedder SearchEmbedder, mutate ...func(*Deps)) http.Handler {
	t.Helper()
	deps, db, ragStore := searchTestDepsWithStores(t)
	deps.Channels = channels.New(db)
	deps.Embedder = embedder
	deps.MCPToken = mcpTestToken
	deps.PublicURL = "https://peeq.example.com"
	deps.Version = "9.9.9"

	for _, c := range []channels.Channel{
		{ID: "UCbikes", Name: "Bike Physics", Handle: "@bikephysics", Description: "Why bicycles behave."},
		{ID: "UCcook", Name: "Kitchen Lab", Description: "Cooking, measured."},
		{ID: "UCfresh", Name: "Fresh Start", Description: "Subscribed, nothing scanned yet."},
	} {
		if err := deps.Channels.Upsert(c); err != nil {
			t.Fatal(err)
		}
		if err := deps.Channels.Subscribe(c.ID, "2099-01-01 00:00:00"); err != nil {
			t.Fatal(err)
		}
		if err := deps.Channels.MarkAdded(c.ID, "2026-01-01 00:00:00"); err != nil {
			t.Fatal(err)
		}
	}
	// keep_reads defaults to on (0037); only Bike Physics keeps its reads.
	for _, id := range []string{"UCcook", "UCfresh"} {
		if _, _, err := deps.Channels.SetKeepReads(id, false); err != nil {
			t.Fatal(err)
		}
	}

	seed := func(v videos.Video, category, yt, summary string, rows []rag.ChunkRow) {
		if err := deps.Videos.Upsert(v); err != nil {
			t.Fatal(err)
		}
		mustExec(t, db, `UPDATE videos SET category = ?, yt_categories = ? WHERE id = ?`, category, yt, v.ID)
		if err := deps.Videos.SetSummary(v.ID, summary,
			`[{"ts":0,"title":"Intro","source":"model"},{"ts":190,"title":"Self-stability","source":"model"}]`,
			`[{"ts":61,"text":"Trail is what steers the bike"}]`); err != nil {
			t.Fatal(err)
		}
		vec := dim1536(1.0)
		if category == "food" {
			// Far from every query vector the fake embedder returns: past the
			// distance bound, so only a keyword match can find it.
			vec = dim1536(-1.0)
		}
		vecs := make([][]float32, len(rows))
		for i := range vecs {
			vecs[i] = vec
		}
		if err := ragStore.ReplaceVideoChunks(context.Background(), v.ID,
			rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev}, rows, vecs); err != nil {
			t.Fatal(err)
		}
	}
	seed(videos.Video{ID: "bikeVideo01", URL: "u1", Title: "Why bikes stay upright", ChannelID: "UCbikes",
		ChannelName: "Bike Physics", PublishedAt: "2026-05-02"}, "science", `["Science & Technology"]`,
		"Gyroscopes are not why bikes stay up; trail and mass distribution are.",
		[]rag.ChunkRow{{Ordinal: 0, Text: "the trail of the front wheel steers the bicycle", StartSeconds: 754}})
	seed(videos.Video{ID: "bikeVideo02", URL: "u2", Title: "Bicycle trail explained", ChannelID: "UCbikes",
		ChannelName: "Bike Physics", PublishedAt: "2025-01-10"}, "engineering", `["Education"]`,
		"A workshop look at fork rake and trail.",
		[]rag.ChunkRow{{Ordinal: 0, Text: "measuring the trail on a workshop bicycle", StartSeconds: 30}})
	seed(videos.Video{ID: "cookVideo01", URL: "u3", Title: "Perfect rice", ChannelID: "UCcook",
		ChannelName: "Kitchen Lab", PublishedAt: "2026-02-02"}, "food", `["Howto & Style"]`,
		"Rinse, ratio, rest.",
		[]rag.ChunkRow{{Ordinal: 0, Text: "rinse the rice until the water is clear", StartSeconds: 12}})
	if err := deps.Videos.SetInDepth("bikeVideo01", "## Trail\nThe contact patch trails the steering axis."); err != nil {
		t.Fatal(err)
	}
	vtt := "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nhello riders\n\n00:01:05.000 --> 00:01:08.000\ntrail matters\n"
	if err := deps.Videos.SetTranscript("bikeVideo01", videos.TranscriptSourceDownload, vtt); err != nil {
		t.Fatal(err)
	}
	for _, m := range mutate {
		m(&deps)
	}
	return New(deps)
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

type mcpReply struct {
	Result map[string]any `json:"result"`
	Error  *struct {
		Code int `json:"code"`
	} `json:"error"`
}

// mcpPost sends a 2026-07-28 request (or a legacy one when modern is false).
func mcpPost(t *testing.T, h http.Handler, modern bool, method string, params map[string]any) (int, mcpReply) {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	if modern {
		params["_meta"] = map[string]any{"io.modelcontextprotocol/protocolVersion": mcpserver.ModernProtocolVersion}
	}
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if !strings.HasPrefix(method, "notifications/") {
		msg["id"] = 1
	}
	raw, _ := json.Marshal(msg)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(raw))
	if modern {
		req.Header.Set("MCP-Protocol-Version", mcpserver.ModernProtocolVersion)
		req.Header.Set("Mcp-Method", method)
		if name, ok := params["name"].(string); ok {
			req.Header.Set("Mcp-Name", name)
		}
	}
	req.Header.Set("Authorization", "Bearer "+mcpTestToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out mcpReply
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: decode %s: %v", method, rec.Body.String(), err)
		}
	}
	return rec.Code, out
}

func mcpToolText(t *testing.T, r mcpReply) string {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("rpc error %d", r.Error.Code)
	}
	content, _ := r.Result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v", r.Result)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	return text
}

func callMCPTool(t *testing.T, h http.Handler, name string, args map[string]any) mcpReply {
	t.Helper()
	code, r := mcpPost(t, h, true, "tools/call", map[string]any{"name": name, "arguments": args})
	if code != http.StatusOK {
		t.Fatalf("%s: status %d", name, code)
	}
	return r
}

func TestMCP_unmountedWithoutAToken(t *testing.T) {
	deps := searchTestDeps(t)
	h := New(deps)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestMCP_rejectsAMissingToken(t *testing.T) {
	h := mcpFixture(t, &fakeEmbedder{vec: dim1536(1.0)})
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("status = %d WWW-Authenticate = %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

// The extension's API token never opens /mcp, and the MCP token never opens
// the extension route.
func TestMCP_tokensDoNotCross(t *testing.T) {
	h := mcpFixture(t, nil)
	req := httptest.NewRequest(http.MethodPut, "/api/machine/cookie", strings.NewReader("x"))
	req.Header.Set("Authorization", "Bearer "+mcpTestToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("machine route with the MCP token: status = %d, want 401", rec.Code)
	}
}

func TestMCP_endToEnd(t *testing.T) {
	h := mcpFixture(t, &fakeEmbedder{vec: dim1536(1.0)})

	// An initialize-era client.
	code, r := mcpPost(t, h, false, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	if code != http.StatusOK || r.Result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize: %d %v", code, r.Result)
	}
	instr, _ := r.Result["instructions"].(string)
	if !strings.Contains(instr, "- Bike Physics (@bikephysics) (full catalogue indexed): engineering, science; youtube:") ||
		!strings.Contains(instr, "- Kitchen Lab: food; youtube: Howto & Style") {
		t.Errorf("instructions:\n%s", instr)
	}
	if code, _ := mcpPost(t, h, false, "notifications/initialized", nil); code != http.StatusAccepted {
		t.Errorf("notifications/initialized: %d", code)
	}

	// A 2026-07-28 client.
	if code, r := mcpPost(t, h, true, "server/discover", nil); code != http.StatusOK || r.Result["resultType"] != "complete" {
		t.Fatalf("discover: %d %v", code, r.Result)
	}
	_, r = mcpPost(t, h, true, "tools/list", nil)
	tools, _ := r.Result["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("tools = %v", r.Result)
	}
	if desc, _ := tools[0].(map[string]any)["description"].(string); !strings.Contains(desc, "Bike Physics") {
		t.Errorf("search description lacks the channels: %s", desc)
	}

	r = callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail"})
	text := mcpToolText(t, r)
	for _, want := range []string{
		"Why bikes stay upright — Bike Physics, 2026-05-02, category science · youtube: Science & Technology",
		"[12:34] transcript:",
		"peeq: https://peeq.example.com/video/bikeVideo01?t=754  youtube: https://youtu.be/bikeVideo01?t=754",
		"summary: Gyroscopes are not why bikes stay up",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("search text lacks %q\n---\n%s", want, text)
		}
	}
	if strings.Contains(text, "Perfect rice") {
		t.Errorf("unrelated video in results:\n%s", text)
	}
	sc, _ := r.Result["structuredContent"].(map[string]any)
	sources, _ := sc["sources"].([]any)
	if len(sources) < 2 {
		t.Fatalf("sources = %v", sc)
	}
	// sources[i] is the i-th (unnumbered) text item: same video, same link.
	var headers []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "- ") {
			headers = append(headers, line)
		}
	}
	if len(headers) != len(sources) {
		t.Fatalf("%d items, %d sources", len(headers), len(sources))
	}
	for i, s := range sources {
		src, _ := s.(map[string]any)
		url, _ := src["url"].(string)
		id := strings.TrimPrefix(strings.SplitN(url, "?", 2)[0], "https://peeq.example.com/video/")
		if src["label"] != "peeq · Bike Physics" || src["image"] != "https://i.ytimg.com/vi/"+id+"/mqdefault.jpg" {
			t.Errorf("source[%d] = %v", i, src)
		}
		if !strings.HasPrefix(headers[i], fmt.Sprintf("- %s —", src["title"])) || !strings.Contains(text, "peeq: "+url+" ") {
			t.Errorf("source[%d] %v does not match text item %d", i, src, i)
		}
	}
	if meta, _ := r.Result["_meta"].(map[string]any); meta == nil {
		t.Error("modern tools/call result lacks _meta")
	}

	// Filters reach SQL: by YouTube category, by channel (fuzzy), by peeq category.
	text = mcpToolText(t, callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail", "youtube_category": "education"}))
	if !strings.Contains(text, "Bicycle trail explained") || strings.Contains(text, "Why bikes stay upright") {
		t.Errorf("youtube_category filter:\n%s", text)
	}
	text = mcpToolText(t, callMCPTool(t, h, "search", map[string]any{"query": "rice", "channel": "kitchen lab", "mode": "keyword"}))
	if !strings.Contains(text, "Perfect rice") {
		t.Errorf("channel + keyword search:\n%s", text)
	}
	text = mcpToolText(t, callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail", "category": "engineering"}))
	if strings.Contains(text, "Why bikes stay upright") || !strings.Contains(text, "Bicycle trail explained") {
		t.Errorf("category filter:\n%s", text)
	}
	r = callMCPTool(t, h, "search", map[string]any{"query": "x", "channel": "Gardening World"})
	if r.Result["isError"] != true || !strings.Contains(mcpToolText(t, r), "Closest: ") {
		t.Errorf("unknown channel: %v", r.Result)
	}

	text = mcpToolText(t, callMCPTool(t, h, "get_video", map[string]any{"id": "bikeVideo01"}))
	for _, want := range []string{"Why bikes stay upright", "youtube categories: Science & Technology",
		"[03:10] Self-stability", "[01:01] Trail is what steers the bike", "The contact patch trails"} {
		if !strings.Contains(text, want) {
			t.Errorf("get_video lacks %q\n---\n%s", want, text)
		}
	}

	text = mcpToolText(t, callMCPTool(t, h, "get_transcript", map[string]any{"id": "bikeVideo01", "start_seconds": 60}))
	if !strings.Contains(text, "[01:05] trail matters") || strings.Contains(text, "hello riders") {
		t.Errorf("get_transcript:\n%s", text)
	}

	text = mcpToolText(t, callMCPTool(t, h, "list_youtube_channels", map[string]any{}))
	for _, want := range []string{"Bike Physics (@bikephysics) · id UCbikes · videos: 2 · caption-only reads kept",
		"categories: engineering (1), science (1)", "youtube: Education (1), Science & Technology (1)", "Kitchen Lab"} {
		if !strings.Contains(text, want) {
			t.Errorf("list_youtube_channels lacks %q\n---\n%s", want, text)
		}
	}
}

func TestMCP_searchDegradesToKeywordWhenEmbeddingFails(t *testing.T) {
	h := mcpFixture(t, &fakeEmbedder{err: errors.New("embeddings are down")})
	text := mcpToolText(t, callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail"}))
	if !strings.Contains(text, "keyword-only") || !strings.Contains(text, "bike") {
		t.Errorf("text:\n%s", text)
	}
}

// A broken vector lane over a working keyword lane is still the documented
// keyword-only degrade.
func TestMCP_searchDegradesWhenTheVectorLaneFails(t *testing.T) {
	h := mcpFixture(t, &fakeEmbedder{vec: dim1536(1.0)}, func(d *Deps) {
		d.Rag = &failingRag{real: d.Rag, retrieve: errors.New("vec0 down")}
	})
	text := mcpToolText(t, callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail"}))
	if !strings.Contains(text, "keyword-only") || !strings.Contains(text, "bike") {
		t.Errorf("text:\n%s", text)
	}
}

// Both lanes failing is a failure, not "no videos match".
func TestMCP_searchFailsWhenBothLanesFail(t *testing.T) {
	h := mcpFixture(t, &fakeEmbedder{vec: dim1536(1.0)}, func(d *Deps) {
		d.Rag = &failingRag{real: d.Rag, searchFTS: errors.New("fts down"), retrieve: errors.New("vec0 down")}
	})
	r := callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail"})
	text := mcpToolText(t, r)
	if r.Result["isError"] != true || !strings.Contains(text, "search failed") || strings.Contains(text, "No videos") {
		t.Errorf("isError=%v text=%q", r.Result["isError"], text)
	}
}

func TestMCP_searchFailsWhenEmbeddingAndKeywordFail(t *testing.T) {
	h := mcpFixture(t, &fakeEmbedder{err: errors.New("embeddings are down")}, func(d *Deps) {
		d.Rag = &failingRag{real: d.Rag, searchFTS: errors.New("fts down")}
	})
	r := callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail"})
	if r.Result["isError"] != true {
		t.Errorf("want a tool error, got %q", mcpToolText(t, r))
	}
}

// ftsFailsAfter lets the first n keyword rungs through and fails the rest,
// so the ladder breaks after it already found matches.
type ftsFailsAfter struct {
	RagStore
	n     int
	calls int
}

func (f *ftsFailsAfter) SearchFTSFiltered(ctx context.Context, match string, n int, flt rag.Filter) ([]rag.Hit, error) {
	f.calls++
	if f.calls > f.n {
		return nil, errors.New("fts rung down")
	}
	return f.RagStore.SearchFTSFiltered(ctx, match, n, flt)
}

// orthogonal is a query vector past the distance bound from every seeded
// chunk: the vector lane runs and finds nothing.
func orthogonal() []float32 {
	v := make([]float32, 1536)
	v[1] = 1
	return v
}

// A later keyword rung failing after earlier ones found matches is not a
// failure: the matches come back, with a note.
func TestMCP_searchKeepsHitsWhenALaterRungFails(t *testing.T) {
	h := mcpFixture(t, &fakeEmbedder{vec: orthogonal()}, func(d *Deps) {
		d.Rag = &ftsFailsAfter{RagStore: d.Rag, n: 1}
	})
	r := callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail"})
	text := mcpToolText(t, r)
	if r.Result["isError"] == true || !strings.Contains(text, "Why bikes stay upright") ||
		!strings.Contains(text, "part of the keyword search failed") {
		t.Errorf("isError=%v text:\n%s", r.Result["isError"], text)
	}
}

// The keyword lane failing outright over a vector lane that found nothing is
// a failure, not "no videos match".
func TestMCP_searchFailsWhenKeywordFailsAndVectorFindsNothing(t *testing.T) {
	h := mcpFixture(t, &fakeEmbedder{vec: orthogonal()}, func(d *Deps) {
		d.Rag = &failingRag{real: d.Rag, searchFTS: errors.New("fts down")}
	})
	r := callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail"})
	text := mcpToolText(t, r)
	if r.Result["isError"] != true || !strings.Contains(text, "search failed") {
		t.Errorf("isError=%v text=%q", r.Result["isError"], text)
	}
}

// The keyword lane failing outright over a working vector lane returns the
// semantic matches and says that is all they are.
func TestMCP_searchSaysSemanticOnlyWhenKeywordFails(t *testing.T) {
	h := mcpFixture(t, &fakeEmbedder{vec: dim1536(1.0)}, func(d *Deps) {
		d.Rag = &failingRag{real: d.Rag, searchFTS: errors.New("fts down")}
	})
	r := callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail"})
	text := mcpToolText(t, r)
	if r.Result["isError"] == true || !strings.Contains(text, "these are semantic matches only") ||
		!strings.Contains(text, "Bike Physics") {
		t.Errorf("isError=%v text:\n%s", r.Result["isError"], text)
	}
}

// A renamed channel still answers to the name its older videos carry, and is
// shown under its current name.
func TestMCP_channelResolvesByAnOldVideoName(t *testing.T) {
	h := mcpFixture(t, &fakeEmbedder{vec: dim1536(1.0)}, func(d *Deps) {
		if err := d.Videos.Upsert(videos.Video{ID: "bikeVideo03", URL: "u4", Title: "Old upload about wheels",
			ChannelID: "UCbikes", ChannelName: "Velo Lab Archive"}); err != nil {
			t.Fatal(err)
		}
	})
	r := callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail", "channel": "Velo Lab Archive", "mode": "keyword"})
	text := mcpToolText(t, r)
	if r.Result["isError"] == true || !strings.Contains(text, "Why bikes stay upright") {
		t.Errorf("isError=%v text:\n%s", r.Result["isError"], text)
	}
}

// A listed channel with nothing scanned yet resolves, and says it is empty.
func TestMCP_searchInAChannelWithoutVideos(t *testing.T) {
	h := mcpFixture(t, &fakeEmbedder{vec: dim1536(1.0)})
	list := mcpToolText(t, callMCPTool(t, h, "list_youtube_channels", map[string]any{}))
	if !strings.Contains(list, "Fresh Start") {
		t.Fatalf("list_youtube_channels lacks the empty channel:\n%s", list)
	}
	for _, name := range []string{"Fresh Start", "UCfresh", "fresh start"} {
		r := callMCPTool(t, h, "search", map[string]any{"query": "bicycle", "channel": name})
		text := mcpToolText(t, r)
		if r.Result["isError"] == true || !strings.Contains(text, "No videos yet in Fresh Start") {
			t.Errorf("%s: isError=%v text=%q", name, r.Result["isError"], text)
		}
	}
}
