package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// channelDescriptionRunes trims a description in list_channels.
const channelDescriptionRunes = 240

func runListChannels(ctx context.Context, h *handler, raw json.RawMessage) toolResult {
	var a struct{}
	if err := decodeArgs(raw, &a); err != nil {
		return errorResult("invalid arguments: " + err.Error())
	}
	chans, err := h.lib.Channels(ctx)
	if err != nil {
		slog.Warn("mcp: list channels failed", "err", err)
		return errorResult("reading the channel list failed; try again")
	}
	if len(chans) == 0 {
		return toolResult{Text: "peeq has no channels yet."}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d channels. Every downloaded or summarized video stays searchable; "+
		"\"caption-only reads kept\" means videos peeq only read (never downloaded) stay searchable too.\n", len(chans))
	for _, c := range chans {
		b.WriteString("\n- " + c.Name)
		if c.Handle != "" {
			b.WriteString(" (" + c.Handle + ")")
		}
		fmt.Fprintf(&b, " · id %s · videos: %d", c.ID, c.Videos)
		if !c.Subscribed {
			b.WriteString(" · not subscribed")
		}
		if c.KeepReads {
			b.WriteString(" · caption-only reads kept")
		}
		if cats := joinCounts(c.TopCategories, 5, true); cats != "" {
			b.WriteString("\n  categories: " + cats)
		}
		if yt := joinCounts(c.TopYouTubeCategories, 3, true); yt != "" {
			b.WriteString("\n  youtube: " + yt)
		}
		if d := trimRunes(c.Description, channelDescriptionRunes); d != "" {
			b.WriteString("\n  " + d)
		}
	}
	return toolResult{Text: b.String()}
}
