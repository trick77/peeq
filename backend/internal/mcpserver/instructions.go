package mcpserver

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/videos"
)

// hintTTL is how long a built hint is reused. Short against the 15 min client
// cache, so a new topic shows within a minute of it.
const hintTTL = time.Minute

const instructionsIntro = "peeq is the user's own YouTube library: transcripts, summaries, chapters, key points " +
	"and in-depth analyses of the videos in it. Search it to answer from what those videos say."

const (
	autoSearchHeader = "Search peeq automatically (without being asked) when the user's question touches these topics:"
	explicitLine     = "The user can also ask explicitly (\"search peeq for …\", \"in channel X …\"); list_youtube_channels names the channels."
)

// instructions is the server's usage hint, built per request so a new topic
// shows up without a restart. A client may put it into every turn (loom
// does): it says when to search, and leaves what the tools do to their own
// descriptions and which channels there are to list_youtube_channels.
func (h *handler) instructions(ctx context.Context) string {
	hint := h.topicHint(ctx)
	if hint == "" {
		return instructionsIntro
	}
	return instructionsIntro + "\n\n" + hint
}

// topicHint is the auto-search block of the instructions.
func (h *handler) topicHint(ctx context.Context) string {
	h.hintMu.Lock()
	defer h.hintMu.Unlock()
	if time.Now().Before(h.hintExpires) {
		return h.hint
	}
	chans, err := h.lib.LabelledChannels(ctx)
	if err != nil {
		// The last hint, if any, and not cached: a client keeps what it gets
		// for 15 minutes, and the next request tries again.
		slog.Warn("mcp: channel list unavailable for the hint", "err", err)
		return h.hint
	}
	h.hint, h.hintExpires = buildTopicHint(chans), time.Now().Add(hintTTL)
	return h.hint
}

// buildTopicHint renders the channels' topics, as the keys search's filters
// take. Its size is bounded by the vocabulary, so nothing is cut however many
// channels there are.
func buildTopicHint(chans []Channel) string {
	if len(chans) == 0 {
		return ""
	}
	used := map[string]bool{}
	for _, c := range chans {
		// A channel can be labelled from its description before it has a
		// video: nothing to find there yet.
		if c.Videos > 0 {
			used[topicOf(c)] = true
		}
	}
	var topics []string
	for _, cat := range videos.CategoryIDs() {
		// "other" names no subject: the category itself does, beside its
		// specific topics too.
		if used[cat+"/"+videos.OtherSubcategory] {
			topics = append(topics, cat)
		}
		for _, s := range videos.SubcategoriesFor(cat) {
			if used[s.Key()] && s.ID != videos.OtherSubcategory {
				topics = append(topics, s.Key())
			}
		}
	}
	if len(topics) == 0 {
		return explicitLine
	}
	return autoSearchHeader + "\n" + strings.Join(topics, ", ") + "\n" + explicitLine
}

// topicOf is the one subcategory key a channel counts as, "" without any: its
// primary label (a side label would trigger a search on every question about
// it), or the largest category of a channel not labelled yet.
func topicOf(c Channel) string {
	for _, k := range c.Subcategories {
		if videos.ValidSubcategory(k) {
			return k
		}
	}
	for _, cat := range c.TopCategories {
		if k := cat.Category + "/" + videos.OtherSubcategory; videos.ValidSubcategory(k) {
			return k
		}
	}
	return ""
}
