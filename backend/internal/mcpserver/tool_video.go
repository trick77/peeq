package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// maxVideoTags trims yt-dlp's tag list, which can run to dozens of SEO terms.
const maxVideoTags = 15

type videoArgs struct {
	ID string `json:"id"`
}

func runGetVideo(ctx context.Context, h *handler, raw json.RawMessage) toolResult {
	var a videoArgs
	if err := decodeArgs(raw, &a); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return errorResult("id is required")
	}
	v, err := h.lib.Video(ctx, id)
	if err != nil {
		slog.Warn("mcp: get video failed", "video_id", id, "err", err)
		return errorResult("reading the video failed; try again")
	}
	if v == nil {
		return errorResult(fmt.Sprintf("No video with id %q in peeq. Use the id from a search result.", id))
	}

	var b strings.Builder
	title := v.Title
	if title == "" {
		title = v.ID
	}
	b.WriteString(title + "\n")
	var meta []string
	add := func(k, val string) {
		if val != "" {
			meta = append(meta, k+": "+val)
		}
	}
	add("channel", v.Channel)
	add("published", v.PublishedAt)
	if v.DurationSeconds > 0 {
		add("duration", clock(int(v.DurationSeconds)))
	}
	add("category", v.Category)
	add("subcategories", strings.Join(v.Subcategories, ", "))
	add("youtube categories", strings.Join(v.YouTubeCategories, ", "))
	if len(meta) > 0 {
		b.WriteString(strings.Join(meta, " · ") + "\n")
	}
	if len(v.Tags) > 0 {
		b.WriteString("tags: " + strings.Join(v.Tags[:min(len(v.Tags), maxVideoTags)], ", ") + "\n")
	}
	fmt.Fprintf(&b, "peeq: %s  youtube: %s\n", h.videoURL(v.ID, 0), youtubeURL(v.ID, 0))

	if v.Summary != "" {
		b.WriteString("\nSummary:\n" + strings.TrimSpace(v.Summary) + "\n")
	}
	writeMoments(&b, "Key points", v.KeyPoints)
	writeMoments(&b, "Chapters", v.Chapters)
	// Last, being the longest: if anything is cut by the size cap, it is this.
	if v.InDepth != "" {
		b.WriteString("\nIn-depth summary:\n" + strings.TrimSpace(v.InDepth) + "\n")
	}
	return toolResult{
		Text: strings.TrimRight(b.String(), "\n"),
		Sources: []Source{{
			URL:     h.videoURL(v.ID, 0),
			Title:   title,
			Label:   sourceLabel(v.Channel),
			Snippet: trimRunes(v.Summary, snippetRunes),
			Image:   thumbnailURL(v.ID),
		}},
	}
}

func writeMoments(b *strings.Builder, heading string, ms []Moment) {
	if len(ms) == 0 {
		return
	}
	b.WriteString("\n" + heading + ":\n")
	for _, m := range ms {
		fmt.Fprintf(b, "- [%s] %s\n", clock(m.Seconds), strings.TrimSpace(m.Text))
	}
}
