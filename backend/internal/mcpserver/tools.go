package mcpserver

import (
	"context"
	"encoding/json"
	"time"

	"github.com/trick77/peeq/internal/videos"
	mcp "github.com/trick77/ship/kit/mcpserver"
)

// toolsTTL is how long a client may cache tools/list (and discovery): long
// enough to spare a round trip per turn, short enough that a newly subscribed
// channel reaches the search description within the quarter hour.
const toolsTTL = 15 * time.Minute

// searchDescription is the search tool's fixed part; descriptions adds the
// channel hint per request.
const searchDescription = "Search the user's peeq library of YouTube videos: transcripts, summaries, chapters " +
	"and peeq's in-depth analysis. Returns the best-matching videos, each with timestamped " +
	"moments and links that open the video at that moment; cite them. " +
	subcategoryNote

// subcategoryNote tells a client the subcategory filters exist and how they
// differ. It rides in the search description and the server instructions.
const subcategoryNote = "Videos and channels carry subcategories, category/subcategory keys such as sports/cycling " +
	"or ai/building: filter with subcategory (the video's own) or channel_subcategory (videos from channels " +
	"labelled with it, which also reaches videos peeq only read); list_youtube_channels shows each channel's."

// toolAliases maps retired tool names to their replacement. They answer calls
// but are never listed: a client that cached the old tools/list (up to the ttl,
// or a whole session for an initialize-era client) keeps working until it
// refreshes.
var toolAliases = map[string]string{
	"list_channels": "list_youtube_channels",
}

// tools is peeq's registry in tools/list order. Every tool reads peeq's own
// library only: read-only, closed world.
func (h *handler) tools() []mcp.Tool {
	run := func(f func(context.Context, *handler, json.RawMessage) mcp.Result) func(context.Context, json.RawMessage) mcp.Result {
		return func(ctx context.Context, args json.RawMessage) mcp.Result { return f(ctx, h, args) }
	}
	return []mcp.Tool{
		{
			Name:            "search",
			Title:           "Search your video library",
			Description:     searchDescription,
			DescriptionFunc: h.searchDescription,
			Schema:          searchSchema(),
			ReadOnly:        true,
			Run:             run(runSearch),
		},
		{
			Name:  "get_video",
			Title: "Get a video's details and summaries",
			Description: "Read one video from peeq by id: metadata, short summary, key points, chapters with " +
				"timestamps and the in-depth summary. Use it after search to go deeper into one result.",
			Schema: mcp.ObjectSchema(map[string]any{
				"id": map[string]any{"type": "string", "description": "The video id, as shown in search results."},
			}, "id"),
			ReadOnly: true,
			Run:      run(runGetVideo),
		},
		{
			Name:  "get_transcript",
			Title: "Read a video's transcript",
			Description: "Read a video's transcript as timestamped lines, optionally only a window of it. Long " +
				"transcripts come back in parts; the reply says which start_seconds to request next.",
			Schema: mcp.ObjectSchema(map[string]any{
				"id": map[string]any{"type": "string", "description": "The video id."},
				"start_seconds": map[string]any{"type": "integer", "minimum": 0,
					"description": "Start of the window, in seconds from the start of the video. Default 0."},
				"end_seconds": map[string]any{"type": "integer", "minimum": 0,
					"description": "End of the window in seconds, exclusive. Default: the end of the video."},
			}, "id"),
			ReadOnly: true,
			Run:      run(runGetTranscript),
		},
		{
			Name:  "list_youtube_channels",
			Title: "List YouTube channels in your library",
			Description: "List the channels in peeq: subscriptions plus channels whose caption-only reads are kept, " +
				"with description, video count, top peeq and YouTube categories and the channel's subcategories. " +
				"Use it to find the right channel name, or the channel_subcategory, for search.",
			Schema:   mcp.ObjectSchema(map[string]any{}),
			ReadOnly: true,
			Run:      run(runListYouTubeChannels),
		},
	}
}

// searchDescription is the search description with the per-request channel
// hint.
func (h *handler) searchDescription(ctx context.Context) string {
	if hint := h.channelHint(ctx); hint != "" {
		return searchDescription + "\n\n" + hint
	}
	return searchDescription
}

func searchSchema() map[string]any {
	date := func(what string) map[string]any {
		return map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`,
			"description": "Only videos released " + what + " this date (YYYY-MM-DD), inclusive."}
	}
	return mcp.ObjectSchema(map[string]any{
		"query": map[string]any{"type": "string", "minLength": 1,
			"description": "What to look for: a question, topic or keywords."},
		"channel": map[string]any{"type": "string",
			"description": "Limit to one channel: its name, @handle or id. Matched case-insensitively and tolerant of typos."},
		"category": map[string]any{"type": "string", "enum": videos.CategoryIDs(),
			"description": "Limit to one of peeq's own fixed video categories."},
		"youtube_category": map[string]any{"type": "string",
			"description": "Limit to one of YouTube's own category labels, e.g. \"Science & Technology\" or " +
				"\"Education\". Exact label, case-insensitive; independent of category."},
		"subcategory": map[string]any{"type": "string", "enum": videos.SubcategoryKeys(),
			"description": "Limit to videos labelled with this subcategory (category/subcategory). Finer than " +
				"category; videos peeq only read carry none of their own, so prefer channel_subcategory for " +
				"\"videos from X channels\"."},
		"channel_subcategory": map[string]any{"type": "string", "enum": videos.SubcategoryKeys(),
			"description": "Limit to videos from channels labelled with this subcategory, e.g. sports/cycling " +
				"for \"videos from cycling channels\"."},
		"after":  date("on or after"),
		"before": date("on or before"),
		"mode": map[string]any{"type": "string", "enum": []string{"hybrid", "keyword"}, "default": "hybrid",
			"description": "hybrid (default): keyword and semantic search combined. keyword: literal full-text " +
				"match of every word only (\"quoted phrases\", OR and prefix* are honoured)."},
		"k": map[string]any{"type": "integer", "minimum": 1, "maximum": maxSearchVideos, "default": defaultSearchVideos,
			"description": "Maximum number of videos to return."},
	}, "query")
}
