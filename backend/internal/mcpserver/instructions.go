package mcpserver

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
)

const (
	// instructionsBudget bounds the hint. It rides in the instructions AND the
	// search tool's description, so it is paid twice per tool listing.
	instructionsBudget = 3000
	// hintMaxChannels caps how many channels the hint names at all.
	hintMaxChannels = 40
	// hintDescriptionRunes trims each channel description.
	hintDescriptionRunes = 160
	// hintTTL is how long a built hint is reused. Short against the 15 min
	// client cache, so a subscription change shows within a minute of it.
	hintTTL = time.Minute
)

const instructionsIntro = "peeq is the user's own YouTube library: transcripts, summaries, chapters, key points " +
	"and in-depth analyses of the videos from the channels below. Search it to answer from what those " +
	"videos say, and cite results with the peeq links given."

const (
	autoSearchHeader = "Search peeq automatically (without being asked) when the user's question touches these channels' topics:"
	explicitLine     = "The user can also ask explicitly (\"search peeq for …\", \"in channel X …\"); list_youtube_channels shows every channel."
)

// instructions is the server's usage hint, built per request so a newly
// subscribed channel shows up without a restart.
func (h *handler) instructions(ctx context.Context) string {
	hint := h.channelHint(ctx)
	if hint == "" {
		return instructionsIntro
	}
	return instructionsIntro + "\n\n" + hint
}

// channelHint is the channel block shared by the instructions and the search
// tool's description: every subscribed channel (keep_reads ones first, then
// the biggest), trimmed so the whole instructions text fits the budget.
// Names and categories come first; descriptions only while room is left.
func (h *handler) channelHint(ctx context.Context) string {
	h.hintMu.Lock()
	defer h.hintMu.Unlock()
	if time.Now().Before(h.hintExpires) {
		return h.hint
	}
	chans, err := h.lib.Channels(ctx)
	if err != nil {
		// Not cached: the next request tries again.
		slog.Warn("mcp: channel list unavailable for the hint", "err", err)
		return ""
	}
	h.hint, h.hintExpires = buildChannelHint(chans), time.Now().Add(hintTTL)
	return h.hint
}

// buildChannelHint renders the hint from a channel list.
func buildChannelHint(chans []Channel) string {
	if len(chans) == 0 {
		return ""
	}
	chans = slices.Clone(chans)
	slices.SortStableFunc(chans, func(a, b Channel) int {
		if a.KeepReads != b.KeepReads {
			if a.KeepReads {
				return -1
			}
			return 1
		}
		return cmp.Compare(b.Videos, a.Videos)
	})

	budget := instructionsBudget - len(instructionsIntro) - 2 - len(autoSearchHeader) - 1 - len(explicitLine) - 1
	bases := make([]string, 0, hintMaxChannels)
	used := 0
	for i, c := range chans {
		if i == hintMaxChannels {
			break
		}
		line := hintLine(c)
		// Room is kept for the "+N more" line whenever a channel is dropped.
		if used+len(line)+1 > budget-40 {
			break
		}
		bases = append(bases, line)
		used += len(line) + 1
	}
	lines := make([]string, len(bases))
	for i, base := range bases {
		lines[i] = base
		if d := trimRunes(chans[i].Description, hintDescriptionRunes); d != "" {
			extra := " — " + d
			if used+len(extra) <= budget-40 {
				lines[i] += extra
				used += len(extra)
			}
		}
	}
	var b strings.Builder
	b.WriteString(autoSearchHeader)
	for _, l := range lines {
		b.WriteString("\n" + l)
	}
	if rest := len(chans) - len(lines); rest > 0 {
		fmt.Fprintf(&b, "\n(+%d more channels)", rest)
	}
	b.WriteString("\n" + explicitLine)
	return b.String()
}

func hintLine(c Channel) string {
	var b strings.Builder
	b.WriteString("- " + c.Name)
	if c.Handle != "" {
		b.WriteString(" (" + c.Handle + ")")
	}
	if c.KeepReads {
		b.WriteString(" (full catalogue indexed)")
	}
	cats := joinCounts(c.TopCategories, 3, false)
	yt := joinCounts(c.TopYouTubeCategories, 2, false)
	switch {
	case cats != "" && yt != "":
		b.WriteString(": " + cats + "; youtube: " + yt)
	case cats != "":
		b.WriteString(": " + cats)
	case yt != "":
		b.WriteString(": youtube: " + yt)
	}
	return b.String()
}
