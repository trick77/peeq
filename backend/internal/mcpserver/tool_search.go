package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

const (
	defaultSearchVideos = 6
	maxSearchVideos     = 10
	// summaryRunes trims each result's short summary: enough to judge
	// relevance, not so much that six results crowd out the matches.
	summaryRunes = 300
	// snippetRunes bounds a source's snippet in structuredContent.
	snippetRunes = 240
)

type searchArgs struct {
	Query           string `json:"query"`
	Channel         string `json:"channel"`
	Category        string `json:"category"`
	YouTubeCategory string `json:"youtube_category"`
	After           string `json:"after"`
	Before          string `json:"before"`
	Mode            string `json:"mode"`
	K               *int   `json:"k"`
}

func runSearch(ctx context.Context, h *handler, raw json.RawMessage) toolResult {
	var a searchArgs
	if err := decodeArgs(raw, &a); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	q := SearchQuery{
		Text:            strings.TrimSpace(a.Query),
		Category:        strings.TrimSpace(a.Category),
		YouTubeCategory: strings.TrimSpace(a.YouTubeCategory),
		Videos:          defaultSearchVideos,
	}
	if q.Text == "" {
		return errorResult("query is required")
	}
	if q.Category != "" && !videos.ValidCategory(q.Category) {
		return errorResult(fmt.Sprintf("unknown category %q; use one of: %s", q.Category, strings.Join(videos.CategoryIDs(), ", ")))
	}
	for _, d := range []struct{ name, val string }{{"after", a.After}, {"before", a.Before}} {
		if d.val == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", d.val); err != nil {
			return errorResult(fmt.Sprintf("%s must be a date as YYYY-MM-DD, got %q", d.name, d.val))
		}
	}
	q.After, q.Before = a.After, a.Before
	switch a.Mode {
	case "", "hybrid":
	case "keyword":
		q.Keyword = true
	default:
		return errorResult(fmt.Sprintf("mode must be hybrid or keyword, got %q", a.Mode))
	}
	if a.K != nil {
		q.Videos = min(max(*a.K, 1), maxSearchVideos)
	}
	if name := strings.TrimSpace(a.Channel); name != "" {
		scope, err := h.lib.ResolveChannel(ctx, name)
		if err != nil {
			slog.Warn("mcp: channel resolve failed", "err", err)
			return errorResult("search failed: the channel list could not be read")
		}
		if !scope.Resolved() {
			msg := fmt.Sprintf("No channel in peeq matches %q.", name)
			if len(scope.Suggestions) > 0 {
				msg += " Closest: " + strings.Join(scope.Suggestions, ", ") + "."
			}
			return errorResult(msg + " Call list_channels for every channel, or search without a channel.")
		}
		q.Channel = scope
	}

	res, err := h.lib.Search(ctx, q)
	if err != nil {
		slog.Warn("mcp: search failed", "err", err)
		return errorResult("search failed; try again, or narrow the query")
	}
	return h.renderSearch(q, res)
}

func (h *handler) renderSearch(q SearchQuery, res SearchResult) toolResult {
	sources := make([]Source, 0, len(res.Videos))
	var b strings.Builder
	if res.SemanticDegraded && !q.Keyword {
		b.WriteString("Note: semantic search was unavailable, so these are keyword-only results.\n\n")
	}
	if len(res.Videos) == 0 {
		b.WriteString("No videos in peeq match this search.")
		if len(q.Channel.Matched) > 0 || q.Category != "" || q.YouTubeCategory != "" || q.After != "" || q.Before != "" {
			b.WriteString(" Try again without some of the filters.")
		}
		return toolResult{Text: b.String(), Sources: sources}
	}
	for i, vh := range res.Videos {
		v := vh.Video
		best := bestMoment(vh.Matches)
		fmt.Fprintf(&b, "%d. %s\n", i+1, resultHeader(v))
		if s := trimRunes(v.Summary, summaryRunes); s != "" {
			b.WriteString("   summary: " + s + "\n")
		}
		for _, m := range vh.Matches {
			snip := trimRunes(boldHighlights(m.Snippet), snippetRunes)
			if m.Kind == rag.KindSummary {
				b.WriteString("   summary match: " + snip + "\n")
				continue
			}
			fmt.Fprintf(&b, "   [%s] %s: %s\n", clock(m.StartSeconds), kindLabel(m.Kind), snip)
		}
		fmt.Fprintf(&b, "   peeq: %s  youtube: %s\n", h.videoURL(v.ID, best), youtubeURL(v.ID, best))

		snippet := ""
		if len(vh.Matches) > 0 {
			snippet = trimRunes(stripHighlights(vh.Matches[0].Snippet), snippetRunes)
		}
		sources = append(sources, Source{
			URL:     h.videoURL(v.ID, best),
			Title:   v.Title,
			Label:   sourceLabel(v.Channel),
			Snippet: snippet,
			Image:   thumbnailURL(v.ID),
		})
	}
	return toolResult{Text: strings.TrimRight(b.String(), "\n"), Sources: sources}
}

// resultHeader is "<title> — <channel>, <date>, category <c> · youtube: <yt>",
// each part omitted when unknown.
func resultHeader(v Video) string {
	var meta []string
	if v.Channel != "" {
		meta = append(meta, v.Channel)
	}
	if v.PublishedAt != "" {
		meta = append(meta, v.PublishedAt)
	}
	if v.Category != "" {
		c := "category " + v.Category
		if len(v.YouTubeCategories) > 0 {
			c += " · youtube: " + strings.Join(v.YouTubeCategories, ", ")
		}
		meta = append(meta, c)
	} else if len(v.YouTubeCategories) > 0 {
		meta = append(meta, "youtube: "+strings.Join(v.YouTubeCategories, ", "))
	}
	title := v.Title
	if title == "" {
		title = v.ID
	}
	if len(meta) == 0 {
		return title
	}
	return title + " — " + strings.Join(meta, ", ")
}

// bestMoment is the timestamp a result links to: its best-ranked match that
// points at a moment. A summary describes the whole video and has none.
func bestMoment(ms []Match) int {
	for _, m := range ms {
		if m.Kind != rag.KindSummary {
			return m.StartSeconds
		}
	}
	return 0
}

func kindLabel(kind string) string {
	switch kind {
	case rag.KindInDepth:
		return "in-depth"
	case "":
		return rag.KindTranscript
	}
	return kind
}
