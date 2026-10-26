package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/channels"
)

// The understand call is the latency-bound gate in front of every Ask answer,
// so it offers only the channel subcategories some channel carries.
func TestBuildUnderstandPrompt_offersOnlyTheGivenChannelSubcategories(t *testing.T) {
	if strings.Contains(understandSystemPrompt, "channel_subcategory") {
		t.Fatal("with none in use the prompt must not offer the filter")
	}
	got := buildUnderstandPrompt([]string{"ai/building", "sports/cycling"})
	if !strings.Contains(got, `"channel_subcategory": one of ai/building, sports/cycling — `) {
		t.Fatalf("filter line missing or wrong:\n%s", got)
	}
	if strings.Contains(got, "ai/models") || strings.Contains(got, "science/uap") {
		t.Fatal("the prompt lists keys no channel carries")
	}
	if !strings.Contains(got, `{"channel_subcategory": "sports/cycling"}`) {
		t.Fatal("the worked example is missing")
	}
	// The example only ever names a key the list offers.
	got = buildUnderstandPrompt([]string{"ai/building"})
	if strings.Contains(got, "sports/cycling") || !strings.Contains(got, `{"channel_subcategory": "ai/building"}`) {
		t.Fatalf("example names a key outside the list:\n%s", got)
	}
}

func TestUnderstandPrompt_readsTheChannelsInUse(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	s := &server{channels: deps.Channels}
	ctx := context.Background()
	if got := s.understandPrompt(ctx); got != understandSystemPrompt {
		t.Fatal("no labelled channel yet: want the plain prompt")
	}
	if err := deps.Channels.Upsert(channels.Channel{ID: "UCa", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Channels.SetSubcategoriesIfAuto(ctx, "UCa", []string{"sports/cycling"}, 1); err != nil {
		t.Fatal(err)
	}
	// Cached for the TTL; a fresh cache sees the label.
	s.understandPromptCache = understandPromptCache{}
	if got := s.understandPrompt(ctx); !strings.Contains(got, "one of sports/cycling — ") {
		t.Fatalf("the labelled channel's key is not offered:\n%s", got)
	}
}
