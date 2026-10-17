package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestInstructions_listSubscribedChannelsForAutoSearch(t *testing.T) {
	lib := &fakeLibrary{channels: []Channel{
		{ID: "UC1", Name: "Small", Subscribed: true, Videos: 2, Description: "tiny channel"},
		{ID: "UC2", Name: "Big", Handle: "@big", Subscribed: true, Videos: 90, Description: "lots of physics",
			TopCategories:        []CategoryCount{{"science", 50}, {"space", 20}, {"tech", 10}, {"ai", 5}},
			TopYouTubeCategories: []CategoryCount{{"Education", 80}}},
		{ID: "UC3", Name: "Kept", Subscribed: true, KeepReads: true, Videos: 1},
	}}
	got := (&handler{lib: lib}).instructions(context.Background())

	if !strings.Contains(got, "Search peeq automatically (without being asked)") {
		t.Fatalf("no auto-search line:\n%s", got)
	}
	kept := strings.Index(got, "- Kept")
	big := strings.Index(got, "- Big")
	small := strings.Index(got, "- Small")
	if kept < 0 || big < 0 || small < 0 || kept >= big || big >= small {
		t.Errorf("order wrong (keep_reads first, then by video count):\n%s", got)
	}
	if !strings.Contains(got, "Kept (full catalogue indexed)") {
		t.Errorf("keep_reads channel not marked:\n%s", got)
	}
	if !strings.Contains(got, "science, space, tech") || strings.Contains(got, "tech, ai") {
		t.Errorf("want exactly the top 3 categories:\n%s", got)
	}
	if !strings.Contains(got, "youtube: Education") {
		t.Errorf("YouTube categories missing:\n%s", got)
	}
	if !strings.Contains(got, "lots of physics") {
		t.Errorf("description missing:\n%s", got)
	}
	if !strings.Contains(got, "search peeq") {
		t.Errorf("explicit-request line missing:\n%s", got)
	}
}

func TestInstructions_stayUnderBudget(t *testing.T) {
	var chans []Channel
	for i := 0; i < 120; i++ {
		chans = append(chans, Channel{
			ID: fmt.Sprintf("UC%d", i), Name: fmt.Sprintf("Channel number %d", i), Subscribed: true, Videos: 200 - i,
			Description:          strings.Repeat("A long and winding channel description. ", 20),
			TopCategories:        []CategoryCount{{"science", 3}, {"engineering", 2}, {"education", 1}},
			TopYouTubeCategories: []CategoryCount{{"Science & Technology", 3}, {"Education", 1}},
		})
	}
	got := (&handler{lib: &fakeLibrary{channels: chans}}).instructions(context.Background())
	if len(got) > instructionsBudget {
		t.Fatalf("instructions are %d bytes, budget %d", len(got), instructionsBudget)
	}
	if !strings.Contains(got, "more channels") {
		t.Errorf("dropped channels are not counted:\n%s", got)
	}
	if !strings.Contains(got, "- Channel number 0") {
		t.Errorf("the biggest channel is missing:\n%s", got)
	}
}

// A burst of discovery and listing reads the channel list once per TTL.
func TestChannelHintIsCached(t *testing.T) {
	lib := &fakeLibrary{channels: []Channel{{ID: "UC2", Name: "Big", Subscribed: true, Videos: 9}}}
	h := newTestHandler(lib)
	modernCall(t, h, "server/discover", nil)
	modernCall(t, h, "tools/list", nil)
	legacyCall(t, h, "initialize", map[string]any{"protocolVersion": "2025-06-18"})
	if lib.channelsN != 1 {
		t.Errorf("Channels read %d times, want 1 within the TTL", lib.channelsN)
	}
}

func TestInstructions_noChannels(t *testing.T) {
	got := (&handler{lib: &fakeLibrary{}}).instructions(context.Background())
	if !strings.Contains(got, "peeq") || strings.Contains(got, "automatically") {
		t.Errorf("got:\n%s", got)
	}
}

func TestSearchDescriptionCarriesTheHint(t *testing.T) {
	lib := &fakeLibrary{channels: []Channel{{ID: "UC2", Name: "Big", Subscribed: true, Videos: 9}}}
	r := modernCall(t, newTestHandler(lib), "tools/list", nil)
	tools, _ := r.body.Result["tools"].([]any)
	desc, _ := tools[0].(map[string]any)["description"].(string)
	if !strings.Contains(desc, "- Big") || !strings.Contains(desc, "automatically") {
		t.Errorf("search description = %q", desc)
	}
}
