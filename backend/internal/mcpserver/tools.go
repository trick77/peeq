package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/trick77/peeq/internal/videos"
)

// toolTimeout bounds one tool call. The calling client gives up at 30s; this
// leaves room to still deliver a degraded answer or an error inside that.
const toolTimeout = 25 * time.Second

// Source is one citable link in a tool result's structuredContent.
type Source struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Label   string `json:"label"`
	Snippet string `json:"snippet"`
	// Image is YouTube's public thumbnail: peeq's own thumbnail route needs a
	// session the citing client's browser does not have.
	Image string `json:"image,omitempty"`
}

// toolResult is what a tool produced. Sources nil means "no structured
// content"; a non-nil empty slice is sent as an empty list.
type toolResult struct {
	Text    string
	Sources []Source
	IsError bool
}

func errorResult(msg string) toolResult { return toolResult{Text: msg, IsError: true} }

type tool struct {
	name        string
	title       string
	description string
	schema      map[string]any
	run         func(ctx context.Context, h *handler, args json.RawMessage) toolResult
}

// toolOrder is the deterministic tools/list order.
var toolOrder = []string{"search", "get_video", "get_transcript", "list_youtube_channels"}

// tools is the static registry. It reads nothing, so looking a tool up for a
// call costs no query; only toolDefs adds the per-request channel hint.
var tools = map[string]tool{
	"search": {
		name:  "search",
		title: "Search your video library",
		description: "Search the user's peeq library of YouTube videos: transcripts, summaries, chapters " +
			"and peeq's in-depth analysis. Returns the best-matching videos, each with timestamped " +
			"moments and links that open the video at that moment; cite them.",
		schema: searchSchema(),
		run:    runSearch,
	},
	"get_video": {
		name:  "get_video",
		title: "Get a video's summaries",
		description: "Read one video from peeq by id: metadata, short summary, key points, chapters with " +
			"timestamps and the in-depth summary. Use it after search to go deeper into one result.",
		schema: objectSchema(map[string]any{
			"id": map[string]any{"type": "string", "description": "The video id, as shown in search results."},
		}, "id"),
		run: runGetVideo,
	},
	"get_transcript": {
		name:  "get_transcript",
		title: "Read a video's transcript",
		description: "Read a video's transcript as timestamped lines, optionally only a window of it. Long " +
			"transcripts come back in parts; the reply says which start_seconds to request next.",
		schema: objectSchema(map[string]any{
			"id": map[string]any{"type": "string", "description": "The video id."},
			"start_seconds": map[string]any{"type": "integer", "minimum": 0,
				"description": "Start of the window, in seconds from the start of the video. Default 0."},
			"end_seconds": map[string]any{"type": "integer", "minimum": 0,
				"description": "End of the window in seconds, exclusive. Default: the end of the video."},
		}, "id"),
		run: runGetTranscript,
	},
	"list_youtube_channels": {
		name:  "list_youtube_channels",
		title: "List your YouTube channels",
		description: "List the channels in peeq: subscriptions plus channels whose caption-only reads are kept, " +
			"with description, video count and top peeq and YouTube categories. Use it to find the right " +
			"channel name for search.",
		schema: objectSchema(map[string]any{}),
		run:    runListChannels,
	},
}

func (h *handler) toolDefs(ctx context.Context) []map[string]any {
	out := make([]map[string]any, 0, len(toolOrder))
	for _, name := range toolOrder {
		t := tools[name]
		desc := t.description
		if name == "search" {
			if hint := h.channelHint(ctx); hint != "" {
				desc += "\n\n" + hint
			}
		}
		out = append(out, map[string]any{
			"name":        t.name,
			"title":       t.title,
			"description": desc,
			"inputSchema": t.schema,
			"annotations": map[string]any{"title": t.title, "readOnlyHint": true, "openWorldHint": false},
		})
	}
	return out
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func searchSchema() map[string]any {
	date := func(what string) map[string]any {
		return map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`,
			"description": "Only videos released " + what + " this date (YYYY-MM-DD), inclusive."}
	}
	return objectSchema(map[string]any{
		"query": map[string]any{"type": "string", "minLength": 1,
			"description": "What to look for: a question, topic or keywords."},
		"channel": map[string]any{"type": "string",
			"description": "Limit to one channel: its name, @handle or id. Matched case-insensitively and tolerant of typos."},
		"category": map[string]any{"type": "string", "enum": videos.CategoryIDs(),
			"description": "Limit to one of peeq's own fixed video categories."},
		"youtube_category": map[string]any{"type": "string",
			"description": "Limit to one of YouTube's own category labels, e.g. \"Science & Technology\" or " +
				"\"Education\". Exact label, case-insensitive; independent of category."},
		"after":  date("on or after"),
		"before": date("on or before"),
		"mode": map[string]any{"type": "string", "enum": []string{"hybrid", "keyword"}, "default": "hybrid",
			"description": "hybrid (default): keyword and semantic search combined. keyword: literal full-text " +
				"match of every word only (\"quoted phrases\", OR and prefix* are honoured)."},
		"k": map[string]any{"type": "integer", "minimum": 1, "maximum": maxSearchVideos, "default": defaultSearchVideos,
			"description": "Maximum number of videos to return."},
	}, "query")
}

func (h *handler) callTool(w http.ResponseWriter, r *http.Request, req request, modern bool) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil || p.Name == "" {
		writeRPCError(w, http.StatusOK, req.ID, codeInvalidParams, "tools/call needs a tool name", nil)
		return
	}
	t, ok := tools[p.Name]
	if !ok {
		writeRPCError(w, http.StatusOK, req.ID, codeInvalidParams, "unknown tool: "+p.Name, nil)
		return
	}
	if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
		p.Arguments = json.RawMessage("{}")
	}
	ctx, cancel := context.WithTimeout(r.Context(), toolTimeout)
	defer cancel()
	start := time.Now()
	res := t.run(ctx, h, p.Arguments)
	slog.Info("mcp tool call", "tool", p.Name, "is_error", res.IsError, "ms", time.Since(start).Milliseconds())

	result := map[string]any{
		"content": []map[string]any{{"type": "text", "text": capText(res.Text, maxToolTextBytes)}},
	}
	if res.Sources != nil {
		result["structuredContent"] = map[string]any{"sources": res.Sources}
	}
	if res.IsError {
		result["isError"] = true
	}
	if modern {
		result = h.modern(result)
	}
	writeResult(w, req.ID, result)
}

// decodeArgs unmarshals tool arguments strictly, so a misspelt field is an
// error the model can read rather than a silently ignored filter.
func decodeArgs(raw json.RawMessage, into any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(into)
}
