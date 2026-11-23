package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	mcp "github.com/trick77/toolkit/mcpserver"
)

// channelDescriptionRunes trims a description in list_youtube_channels.
const channelDescriptionRunes = 240

func runListYouTubeChannels(ctx context.Context, h *handler, raw json.RawMessage) mcp.Result {
	var a struct{}
	if err := mcp.DecodeArgs(raw, &a); err != nil {
		return mcp.ErrorResult("invalid arguments: " + err.Error())
	}
	chans, err := h.lib.Channels(ctx)
	if err != nil {
		slog.Warn("mcp: list channels failed", "err", err)
		return mcp.ErrorResult("reading the channel list failed; try again")
	}
	var b strings.Builder
	// The totals are an extra on the list, not the list: a failed read drops them.
	if t, err := h.lib.Totals(ctx); err != nil {
		slog.WarnContext(ctx, "mcp: library totals unavailable", "err", err)
	} else {
		b.WriteString(totalsLine(t, chans) + "\n")
	}
	if len(chans) == 0 {
		return mcp.Result{Text: b.String() + "peeq has no channels yet."}
	}
	// So are the searchable counts, but a plain count reads as "all
	// searchable": their absence is said once, up front.
	searchable, err := h.lib.SearchableCounts(ctx)
	if err != nil {
		slog.WarnContext(ctx, "mcp: channel searchable counts unavailable", "err", err)
	}
	fmt.Fprintf(&b, "%d channels. Every downloaded or summarized video stays searchable, after its file is deleted too unless its channel is set to forget deleted videos; "+
		"\"caption-only reads kept\" means videos peeq only read (never downloaded) stay searchable too.\n", len(chans))
	if err != nil {
		b.WriteString(searchableUnavailable + "\n")
	}
	if inUse := subcategoriesInUse(chans); len(inUse) > 0 {
		b.WriteString("Channel subcategories in use (search's channel_subcategory filter): " + strings.Join(inUse, ", ") + "\n")
	}
	for _, c := range chans {
		b.WriteString("\n- " + c.Name)
		if c.Handle != "" {
			b.WriteString(" (" + c.Handle + ")")
		}
		fmt.Fprintf(&b, " · id %s · videos: %d", c.ID, c.Videos)
		// Only when it differs: the counts are separate reads, so a larger
		// searchable number is a race, not news.
		if n := searchable[c.ID]; err == nil && n < c.Videos {
			fmt.Fprintf(&b, " (%d searchable)", n)
		}
		if !c.Subscribed {
			b.WriteString(" · not subscribed")
		}
		if c.KeepReads {
			b.WriteString(" · caption-only reads kept")
		}
		if len(c.Subcategories) > 0 {
			b.WriteString("\n  subcategories: " + strings.Join(c.Subcategories, ", "))
		}
		if cats := joinCounts(c.TopCategories, 5); cats != "" {
			b.WriteString("\n  categories: " + cats)
		}
		if yt := joinCounts(c.TopYouTubeCategories, 3); yt != "" {
			b.WriteString("\n  youtube: " + yt)
		}
		if d := mcp.OneLine(c.Description, channelDescriptionRunes); d != "" {
			b.WriteString("\n  " + d)
		}
	}
	return mcp.Result{Text: b.String()}
}

// searchableUnavailable stands in for every channel's searchable number.
const searchableUnavailable = "Searchable counts are unavailable right now; a channel's video count may include videos search cannot find."

// totalsLine is the list's first line: the whole library, the searchable
// part with its runtime, and the release dates. The numbers come from
// separate reads, so the total is raised to what the others prove (the
// searchable count, the listed channels' videos), as list_videos raises a
// total below its page: never "3 videos, 4 searchable", never "no videos"
// above channels that have some.
func totalsLine(t LibraryTotals, chans []Channel) string {
	videos := max(t.Videos, t.Searchable)
	listed := 0
	for _, c := range chans {
		listed += c.Videos
	}
	videos = max(videos, listed)
	if videos == 0 {
		return "Library: no videos yet."
	}
	var b strings.Builder
	b.WriteString("Library: " + countOf(videos, "video", "videos"))
	if t.Searchable == 0 {
		b.WriteString(", none searchable yet")
	} else {
		fmt.Fprintf(&b, ", %d searchable", t.Searchable)
		if r := runtimeText(t.SearchableSeconds); r != "" {
			b.WriteString(" (" + r + ")")
		}
	}
	if t.Oldest != "" {
		fmt.Fprintf(&b, ", released %s to %s", t.Oldest, t.Newest)
	}
	return b.String() + "."
}

// runtimeText rounds a runtime to whole hours, or minutes below one hour;
// "" when none is recorded. Hours start where minutes would round to 60.
func runtimeText(sec int64) string {
	switch {
	case sec >= 3570:
		return fmt.Sprintf("%d h", (sec+1800)/3600)
	case sec > 0:
		return fmt.Sprintf("%d min", max(1, (sec+30)/60))
	}
	return ""
}

func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// subcategoriesInUse is every channel subcategory the list carries, sorted:
// the channel_subcategory values that can match anything today.
func subcategoriesInUse(chans []Channel) []string {
	var out []string
	for _, c := range chans {
		for _, s := range c.Subcategories {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	slices.Sort(out)
	return out
}
