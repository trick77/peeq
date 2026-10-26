package summarize

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/videos"
)

// subcatCompleter answers the two subcategory prompts, routing on the phrase
// each one names its step with, and fails a video whose title says so.
func subcatCompleter(calls *[]string) Completer {
	return completerFunc(func(_ context.Context, m []llm.Message) (string, error) {
		sys := m[0].Content
		switch {
		case strings.Contains(sys, "channel subcategory keys"):
			*calls = append(*calls, "channel")
			return `{"subcategories":["sports/cycling","tech/wearables"]}`, nil
		case strings.Contains(sys, "subcategory ids"):
			*calls = append(*calls, "video")
			if strings.Contains(m[1].Content, "poison") {
				return "", errors.New("boom")
			}
			return `{"subcategories":["cycling"]}`, nil
		}
		return "", errors.New("unexpected prompt: " + sys)
	})
}

func TestIdleSweep_subcategorizesVideosThenChannelsThenRests(t *testing.T) {
	h := newWorkerHarness(t)
	ch := channels.New(h.db)
	ctx := context.Background()
	if err := ch.Upsert(channels.Channel{ID: "UCgcn", Name: "GCN", Description: "Cycling."}); err != nil {
		t.Fatal(err)
	}
	if err := ch.MarkAdded("UCgcn", "2026-07-20 10:00:00"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"v-ok", "v-poison"} {
		if err := h.videos.Upsert(videos.Video{ID: id, URL: "u-" + id, Title: id + " title", ChannelID: "UCgcn"}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.db.Exec(`UPDATE videos SET status='downloaded', summary='s' WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
		if err := h.videos.SetCategory(id, "sports"); err != nil {
			t.Fatal(err)
		}
	}

	var calls []string
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	w := NewWorker(WorkerDeps{
		Jobs: h.jobs, Videos: h.videos, Channels: ch, Rag: h.rag,
		Summarizer: New(subcatCompleter(&calls)), Embedder: fakeWorkerEmbedder{dim: 1536},
		EmbedModel: "test-model", EmbedDim: 1536, Now: func() time.Time { return now },
	})

	// Three working turns: both videos (one fails and is parked), then the channel.
	for i := range 3 {
		if did, err := w.processOne(ctx); err != nil || !did {
			t.Fatalf("turn %d: did=%v err=%v", i, did, err)
		}
	}
	if got, _ := h.videos.SubcategoriesCtx(ctx, "v-ok"); !slices.Equal(got, []string{"sports/cycling"}) {
		t.Fatalf("v-ok = %v", got)
	}
	if got, _ := h.videos.SubcategoriesCtx(ctx, "v-poison"); len(got) != 0 {
		t.Fatalf("v-poison = %v, want none", got)
	}
	keys, manual, _ := ch.Subcategories(ctx, "UCgcn")
	if manual || !slices.Equal(keys, []string{"sports/cycling", "tech/wearables"}) {
		t.Fatalf("channel = %v manual=%v", keys, manual)
	}

	// Nothing left (the poison video is parked): idle, and the empty pass
	// rests the sweep instead of rescanning every poll.
	if did, err := w.processOne(ctx); err != nil || did {
		t.Fatalf("drained turn: did=%v err=%v", did, err)
	}
	if err := h.videos.SetCategory("v-ok", "tech"); err != nil {
		t.Fatal(err)
	}
	before := len(calls)
	if did, _ := w.processOne(ctx); did || len(calls) != before {
		t.Fatalf("swept during the idle hold: did=%v calls=%v", did, calls[before:])
	}
	now = now.Add(subcategoryIdleHold + time.Second)
	if did, err := w.processOne(ctx); err != nil || !did {
		t.Fatalf("after the hold: did=%v err=%v", did, err)
	}
	if calls[len(calls)-1] != "video" {
		t.Fatalf("calls = %v; the re-categorized video should be asked again", calls)
	}
}
