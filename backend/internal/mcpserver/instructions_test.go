package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/videos"
)

func TestBuildTopicHint(t *testing.T) {
	labelled := func(keys ...string) Channel { return Channel{Videos: 1, Subcategories: keys} }
	unlabelled := func(cats ...string) Channel {
		c := Channel{Videos: 1}
		for i, id := range cats {
			c.TopCategories = append(c.TopCategories, CategoryCount{Category: id, Count: 90 - i})
		}
		return c
	}
	for name, tc := range map[string]struct {
		chans []Channel
		want  string
	}{
		// The keys search's filters take, not a form of their own.
		"keys in vocabulary order, each topic once": {
			[]Channel{labelled("sports/cycling"), labelled("science/uap"), labelled("ai/models"), labelled("science/physics"),
				labelled("ai/building"), labelled("sports/cycling")},
			"ai/building, ai/models, science/physics, science/uap, sports/cycling",
		},
		// A side label would make peeq search on every gaming question for
		// the sake of one space channel.
		"the primary label only": {
			[]Channel{labelled("space/spaceflight", "science/physics", "gaming/playthroughs")}, "space/spaceflight",
		},
		"an unlabelled channel counts by its top category": {
			[]Channel{unlabelled("food", "travel")}, "food",
		},
		// A specific channel must not narrow a general one.
		"a general channel beside a specific one": {
			[]Channel{labelled("sports/cycling"), unlabelled("sports")}, "sports, sports/cycling",
		},
		"other is the category": {
			[]Channel{labelled("news/other")}, "news",
		},
		"a retired label falls through to the next": {
			[]Channel{labelled("gone/gone", "home/renovation"), unlabelled("gone", "uncategorized", "food")}, "food, home/renovation",
		},
		"a channel without videos has nothing to find": {
			[]Channel{labelled("sports/cycling"), {Subcategories: []string{"gaming/reviews"}}}, "sports/cycling",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := buildTopicHint(tc.chans)
			if !strings.Contains(got, "\n"+tc.want+"\n") {
				t.Errorf("want the topic line %q in:\n%s", tc.want, got)
			}
			if !strings.Contains(got, "Search peeq automatically (without being asked)") || !strings.Contains(got, "list_youtube_channels") {
				t.Errorf("no auto-search line, or no word where the channels are:\n%s", got)
			}
		})
	}
}

// Without a topic there is nothing to search on unasked, but the user can
// still ask for a channel.
func TestBuildTopicHint_withoutTopics(t *testing.T) {
	if got := buildTopicHint(nil); got != "" {
		t.Errorf("hint without channels = %q, want none", got)
	}
	chans := []Channel{{Name: "New", Subscribed: true}, {Videos: 3, TopCategories: []CategoryCount{{Category: "uncategorized", Count: 3}}}}
	got := buildTopicHint(chans)
	if strings.Contains(got, "automatically") || !strings.Contains(got, "list_youtube_channels") {
		t.Errorf("hint without topics = %q, want the explicit line alone", got)
	}
}

// The channel list once rode in every turn of every client. It belongs to
// list_youtube_channels.
func TestInstructions_nameNoChannel(t *testing.T) {
	lib := &fakeLibrary{channels: []Channel{{ID: "UC1", Name: "Veritasium", Handle: "@veri", Description: "lots of curiosity",
		Subscribed: true, KeepReads: true, Videos: 90, Subcategories: []string{"science/physics"},
		TopYouTubeCategories: []CategoryCount{{"Education", 80}}}}}
	got := (&handler{lib: lib}).instructions(context.Background())
	if !strings.Contains(got, "science/physics") {
		t.Errorf("instructions lack the topic:\n%s", got)
	}
	for _, leak := range []string{"Veritasium", "@veri", "curiosity", "Education"} {
		if strings.Contains(got, leak) {
			t.Errorf("instructions carry %q:\n%s", leak, got)
		}
	}
}

// Bounded by the vocabulary, never by the number of channels: even a library
// with a channel on every topic fits loom's 3072 bytes.
func TestInstructions_staySmallWithEveryTopic(t *testing.T) {
	var chans []Channel
	for i, k := range videos.SubcategoryKeys() {
		chans = append(chans, Channel{ID: fmt.Sprintf("UC%d", i), Name: "Channel", Videos: 1, Subscribed: true, Subcategories: []string{k}})
	}
	got := (&handler{lib: &fakeLibrary{channels: chans}}).instructions(context.Background())
	if len(got) > 2800 {
		t.Errorf("instructions are %d bytes, want at most 2800", len(got))
	}
}

func TestInstructions_noChannels(t *testing.T) {
	got := (&handler{lib: &fakeLibrary{}}).instructions(context.Background())
	if !strings.Contains(got, "peeq") || strings.Contains(got, "automatically") {
		t.Errorf("got:\n%s", got)
	}
}

// A burst of discovery reads the channel list once per TTL, and listing the
// tools does not read it at all.
func TestTopicHintIsCached(t *testing.T) {
	lib := &fakeLibrary{channels: []Channel{{ID: "UC2", Name: "Big", Subscribed: true, Videos: 9}}}
	h := newTestHandler(lib)
	modernCall(t, h, "tools/list", nil)
	if lib.channelsN != 0 {
		t.Errorf("tools/list read the channels %d times, want none", lib.channelsN)
	}
	modernCall(t, h, "server/discover", nil)
	legacyCall(t, h, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	if lib.channelsN != 1 {
		t.Errorf("Channels read %d times, want 1 within the TTL", lib.channelsN)
	}
}

// Without labels a cycling channel reads as "sports": a failed read keeps the
// last hint instead, and the next request tries again.
func TestTopicHint_failedReadKeepsTheLastHint(t *testing.T) {
	lib := &fakeLibrary{channels: []Channel{{Videos: 9, Subcategories: []string{"sports/cycling"}}}}
	h := &handler{lib: lib}
	want := h.topicHint(t.Context())
	h.hintExpires = time.Now().Add(-time.Second)
	lib.labelsErr = errors.New("labels unavailable")
	if got := h.topicHint(t.Context()); got != want || !strings.Contains(got, "sports/cycling") {
		t.Errorf("hint after a failed read = %q, want %q", got, want)
	}
	lib.labelsErr = nil
	lib.channels[0].Subcategories = []string{"ai/models"}
	if got := h.topicHint(t.Context()); !strings.Contains(got, "ai/models") {
		t.Errorf("hint was not read again: %q", got)
	}
}

// The description rides in every turn beside the instructions: the topics are
// in one of them only.
func TestSearchDescriptionIsFixed(t *testing.T) {
	lib := &fakeLibrary{channels: []Channel{{ID: "UC2", Name: "Big", Subscribed: true, Videos: 1, Subcategories: []string{"science/physics"}}}}
	r := modernCall(t, newTestHandler(lib), "tools/list", nil)
	tools, _ := r.body.Result["tools"].([]any)
	desc, _ := tools[0].(map[string]any)["description"].(string)
	if desc != searchDescription {
		t.Errorf("search description = %q", desc)
	}
}
