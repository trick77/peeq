package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

// mcpScopeFixture is mcpFixture plus a third channel, Wheel Works, whose one
// video is a long talk with a "trail" moment every minute (more than a
// search's per-video cap), indexed near every query vector so only a filter
// keeps it out; bikeVideo01 is watched and bikeVideo02 a favourite.
func mcpScopeFixture(t *testing.T, extra ...func(*Deps)) http.Handler {
	t.Helper()
	return mcpFixture(t, &fakeEmbedder{vec: dim1536(1.0)}, append([]func(*Deps){func(d *Deps) {
		if err := d.Videos.Upsert(videos.Video{ID: "wheelTalk01", URL: "u7", Title: "An hour on trail",
			ChannelID: "UCwheels", ChannelName: "Wheel Works", PublishedAt: "2026-03-03"}); err != nil {
			t.Fatal(err)
		}
		rows := make([]rag.ChunkRow, 16)
		vecs := make([][]float32, len(rows))
		for i := range rows {
			rows[i] = rag.ChunkRow{Ordinal: i, Kind: rag.KindTranscript, StartSeconds: 60 * i,
				Text: fmt.Sprintf("bicycle trail, point %d of the talk", i)}
			vecs[i] = dim1536(1.0)
		}
		store, ok := d.Rag.(*rag.Store)
		if !ok {
			t.Fatalf("Rag is %T", d.Rag)
		}
		if err := store.ReplaceVideoChunks(context.Background(), "wheelTalk01",
			rag.IndexMeta{Model: "test-model", Dim: 1536, Rev: rag.ChunkRecipeRev}, rows, vecs); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Videos.SetWatched("bikeVideo01", true); err != nil {
			t.Fatal(err)
		}
		if err := d.Videos.SetFavorite("bikeVideo02", true); err != nil {
			t.Fatal(err)
		}
	}}, extra...)...)
}

// ftsRungs counts the keyword rungs a search runs.
type ftsRungs struct {
	RagStore
	n atomic.Int32
}

func (f *ftsRungs) SearchFTSFiltered(ctx context.Context, match string, n int, flt rag.Filter) ([]rag.Hit, error) {
	f.n.Add(1)
	return f.RagStore.SearchFTSFiltered(ctx, match, n, flt)
}

// The keyword ladder stops at the first rung that covers the scoped videos: a
// precise match inside one video never runs the relaxed rungs or the OR floor
// (which, under a one-video filter, walks every match). A library-wide search
// still descends while it has fewer than keywordVideoTarget videos.
func TestMCP_oneVideoSearchStopsAtTheFirstRungWithHits(t *testing.T) {
	rungs := &ftsRungs{}
	h := mcpScopeFixture(t, func(d *Deps) { rungs.RagStore = d.Rag; d.Rag = rungs })
	text := mcpToolText(t, callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail", "video_id": "wheelTalk01"}))
	if n := rungs.n.Load(); n != 1 || momentLines(text) != singleVideoMatches {
		t.Errorf("one-video search ran %d rungs, %d moments:\n%s", n, momentLines(text), text)
	}
	rungs.n.Store(0)
	mcpToolText(t, callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail"}))
	if n := rungs.n.Load(); n < 2 {
		t.Errorf("library-wide search ran %d rungs, want the ladder to descend", n)
	}
}

func TestLadderVideoTarget(t *testing.T) {
	many := make([]string, keywordVideoTarget+3)
	for _, tc := range []struct {
		f    rag.Filter
		want int
	}{
		{rag.Filter{}, keywordVideoTarget},
		{rag.Filter{ChannelIDs: []string{"UC1"}}, keywordVideoTarget},
		{rag.Filter{VideoIDs: []string{"v1"}}, 1},
		{rag.Filter{VideoIDs: []string{"v1", "v2"}}, 2},
		{rag.Filter{VideoIDs: many}, keywordVideoTarget},
	} {
		if got := ladderVideoTarget(tc.f); got != tc.want {
			t.Errorf("%+v: %d, want %d", tc.f, got, tc.want)
		}
	}
}

// momentLines counts an item's timestamped match lines.
func momentLines(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "   [") {
			n++
		}
	}
	return n
}

var mcpModes = []string{"hybrid", "keyword"}

// video_id scopes every lane to one video and lifts the per-video cap; the
// same query without it keeps the Search page's four.
func TestMCP_searchOneVideo(t *testing.T) {
	h := mcpScopeFixture(t)
	for _, mode := range mcpModes {
		text := mcpToolText(t, callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail", "mode": mode}))
		if !strings.Contains(text, "id: wheelTalk01") || !strings.Contains(text, "id: bikeVideo01") {
			t.Fatalf("%s unscoped:\n%s", mode, text)
		}
		wheel := text[strings.Index(text, "- An hour on trail"):]
		if next := strings.Index(wheel[1:], "\n- "); next >= 0 {
			wheel = wheel[:next+1]
		}
		if n := momentLines(wheel); n > maxMatchesPerVideo {
			t.Errorf("%s: unscoped search shows %d moments of one video", mode, n)
		}

		r := callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail", "mode": mode,
			"video_id": "https://youtu.be/wheelTalk01", "k": 1})
		text = mcpToolText(t, r)
		if r.Result["isError"] == true || strings.Count(text, "\n- ")+1 != 1 || !strings.HasPrefix(text, "- An hour on trail — Wheel Works") {
			t.Fatalf("%s scoped:\n%s", mode, text)
		}
		if n := momentLines(text); n != singleVideoMatches {
			t.Errorf("%s: scoped search shows %d moments, want %d\n%s", mode, n, singleVideoMatches, text)
		}
	}
}

// An unknown id and a known one search cannot reach are answered apart.
func TestMCP_searchOneVideoMisses(t *testing.T) {
	h := mcpScopeFixture(t)
	for _, mode := range mcpModes {
		r := callMCPTool(t, h, "search", map[string]any{"query": "bicycle", "mode": mode, "video_id": "zzzzzzzzzzz"})
		if text := mcpToolText(t, r); r.Result["isError"] != true ||
			text != `No video with id "zzzzzzzzzzz" in peeq. Use the id from a search result.` {
			t.Errorf("%s unknown: %v %q", mode, r.Result["isError"], text)
		}
		r = callMCPTool(t, h, "search", map[string]any{"query": "bicycle", "mode": mode, "video_id": "bikeInbox01"})
		if text := mcpToolText(t, r); r.Result["isError"] != true ||
			text != `Video "bikeInbox01" is in peeq but not searchable: nothing of it is indexed yet. `+
				`Call get_video for its details or get_transcript for its transcript.` {
			t.Errorf("%s not indexed: %v %q", mode, r.Result["isError"], text)
		}
	}
	// The video before the channel: an empty channel never hides an unknown id.
	r := callMCPTool(t, h, "search", map[string]any{"query": "bicycle", "video_id": "zzzzzzzzzzz", "channel": "Fresh Start"})
	if text := mcpToolText(t, r); r.Result["isError"] != true || !strings.HasPrefix(text, `No video with id "zzzzzzzzzzz"`) {
		t.Errorf("unknown video in an empty channel: %v %q", r.Result["isError"], text)
	}
	// Searchable, nothing matches: said of the video, never "drop video_id".
	r = callMCPTool(t, h, "search", map[string]any{"query": "rice", "mode": "keyword", "video_id": "wheelTalk01"})
	if text := mcpToolText(t, r); r.Result["isError"] == true ||
		text != `Nothing in video "wheelTalk01" matches this search. Call get_transcript to read it, or get_video for its summary.` {
		t.Errorf("no match in the video: %v %q", r.Result["isError"], text)
	}
}

func TestMCP_searchByWatchedAndFavorite(t *testing.T) {
	h := mcpScopeFixture(t)
	for _, mode := range mcpModes {
		for _, tc := range []struct {
			args      map[string]any
			want, not []string
		}{
			{map[string]any{"watched": true}, []string{"id: bikeVideo01"}, []string{"id: bikeVideo02", "id: wheelTalk01"}},
			{map[string]any{"watched": false}, []string{"id: bikeVideo02", "id: wheelTalk01"}, []string{"id: bikeVideo01"}},
			{map[string]any{"favorite": true}, []string{"id: bikeVideo02"}, []string{"id: bikeVideo01", "id: wheelTalk01"}},
			{map[string]any{"favorite": false, "watched": false}, []string{"id: wheelTalk01"}, []string{"id: bikeVideo01", "id: bikeVideo02"}},
		} {
			args := map[string]any{"query": "bicycle trail", "mode": mode}
			for k, v := range tc.args {
				args[k] = v
			}
			text := mcpToolText(t, callMCPTool(t, h, "search", args))
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Errorf("%s %v lacks %s:\n%s", mode, tc.args, w, text)
				}
			}
			for _, n := range tc.not {
				if strings.Contains(text, n) {
					t.Errorf("%s %v carries %s:\n%s", mode, tc.args, n, text)
				}
			}
		}
		text := mcpToolText(t, callMCPTool(t, h, "search", map[string]any{"query": "bicycle trail", "mode": mode}))
		for _, want := range []string{"- Why bikes stay upright — Bike Physics, 2026-05-02, category science · youtube: Science & Technology · watched\n",
			"- Bicycle trail explained — Bike Physics, 2025-01-10, category engineering · youtube: Education · favorite\n"} {
			if !strings.Contains(text, want) {
				t.Errorf("%s header lacks %q:\n%s", mode, want, text)
			}
		}
	}
	text := mcpToolText(t, callMCPTool(t, h, "get_video", map[string]any{"id": "bikeVideo01"}))
	if !strings.Contains(text, " · watched\n") {
		t.Errorf("get_video:\n%s", text)
	}
}

// channels unions its entries (and channel): videos from both, none from a
// third channel the query matches as well.
func TestMCP_searchSeveralChannels(t *testing.T) {
	h := mcpScopeFixture(t)
	for _, mode := range mcpModes {
		query := "bicycle trail rice"
		if mode == "keyword" {
			query = "trail OR rice"
		}
		text := mcpToolText(t, callMCPTool(t, h, "search", map[string]any{"query": query, "mode": mode}))
		if !strings.Contains(text, "id: wheelTalk01") {
			t.Fatalf("%s: the third channel's video is no match without the filter:\n%s", mode, text)
		}
		for _, args := range []map[string]any{
			{"channels": []string{"bike physics", "Kitchen Lab"}},
			{"channel": "@bikephysics", "channels": []string{"UCcook"}},
		} {
			args["query"], args["mode"] = query, mode
			text = mcpToolText(t, callMCPTool(t, h, "search", args))
			if !strings.Contains(text, "id: bikeVideo01") || !strings.Contains(text, "id: cookVideo01") ||
				strings.Contains(text, "wheelTalk01") {
				t.Errorf("%s %v:\n%s", mode, args, text)
			}
		}
	}
	// Fresh Start holds no videos: no error among others, the no-videos answer alone.
	text := mcpToolText(t, callMCPTool(t, h, "search", map[string]any{"query": "rice", "mode": "keyword",
		"channels": []string{"Fresh Start", "Kitchen Lab"}}))
	if !strings.HasPrefix(text, "No videos yet in Fresh Start.\n\n") || !strings.Contains(text, "id: cookVideo01") {
		t.Errorf("empty channel among others:\n%s", text)
	}
	r := callMCPTool(t, h, "search", map[string]any{"query": "rice", "channels": []string{"Fresh Start", "Gardening World"}})
	if text := mcpToolText(t, r); r.Result["isError"] != true || !strings.HasPrefix(text, `No channel in peeq matches "Gardening World".`) {
		t.Errorf("one unknown entry: %v %q", r.Result["isError"], text)
	}
}
