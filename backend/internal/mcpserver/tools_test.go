package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/subtitles"
)

func callTool(t *testing.T, lib Library, name string, args map[string]any) rpcReply {
	t.Helper()
	return modernCall(t, newTestHandler(lib), "tools/call", map[string]any{"name": name, "arguments": args})
}

type sourcesBody struct {
	Sources []Source `json:"sources"`
}

func sourcesOf(t *testing.T, r rpcReply) []Source {
	t.Helper()
	raw, err := json.Marshal(r.body.Result["structuredContent"])
	if err != nil {
		t.Fatal(err)
	}
	var sc sourcesBody
	if err := json.Unmarshal(raw, &sc); err != nil {
		t.Fatalf("structuredContent %s: %v", raw, err)
	}
	return sc.Sources
}

func isError(r rpcReply) bool { v, _ := r.body.Result["isError"].(bool); return v }

func searchFixture() *fakeLibrary {
	return &fakeLibrary{
		scopes: map[string]ChannelScope{"veritasium": {IDs: []string{"UCV"}, Matched: []string{"Veritasium"}}},
		result: SearchResult{Videos: []VideoHits{
			{
				Video: Video{ID: "abc123", Title: "Why bikes stay upright", Channel: "Veritasium",
					PublishedAt: "2026-05-02", Category: "science",
					YouTubeCategories: []string{"Science & Technology", "Education"},
					Summary:           strings.Repeat("Gyroscopes are not the reason. ", 20)},
				Matches: []Match{
					{StartSeconds: 754, Kind: rag.KindTranscript, Snippet: "the " + rag.HighlightStart + "trail" + rag.HighlightEnd + " steers it"},
					{StartSeconds: 190, Kind: rag.KindChapter, Snippet: "Self-stability"},
					{StartSeconds: 0, Kind: rag.KindSummary, Snippet: "A bike steers into its fall."},
				},
			},
			{
				Video:   Video{ID: "def456", Title: "Summary only", Channel: "Vsauce", Category: "education"},
				Matches: []Match{{Kind: rag.KindSummary, Snippet: "only the summary matched"}},
			},
		}},
	}
}

func TestSearch_textAndSourcesFollowTheContract(t *testing.T) {
	lib := searchFixture()
	r := callTool(t, lib, "search", map[string]any{"query": "bike trail"})
	text := toolText(t, r)

	for _, want := range []string{
		"1. Why bikes stay upright — Veritasium, 2026-05-02, category science · youtube: Science & Technology, Education\n",
		"   summary: Gyroscopes are not the reason.",
		"   [12:34] transcript: the **trail** steers it\n",
		"   [03:10] chapter: Self-stability\n",
		"   summary match: A bike steers into its fall.\n",
		"   peeq: https://peeq.example.com/video/abc123?t=754  youtube: https://youtu.be/abc123?t=754\n",
		"2. Summary only — Vsauce, category education\n",
		"   peeq: https://peeq.example.com/video/def456  youtube: https://youtu.be/def456",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q\n---\n%s", want, text)
		}
	}
	if strings.ContainsAny(text, rag.HighlightStart+rag.HighlightEnd) {
		t.Error("highlight markers leaked into the text")
	}

	src := sourcesOf(t, r)
	if len(src) != 2 {
		t.Fatalf("sources = %+v, want one per video", src)
	}
	if src[0].URL != "https://peeq.example.com/video/abc123?t=754" || src[0].Title != "Why bikes stay upright" ||
		src[0].Label != "peeq · Veritasium" || src[0].Snippet != "the trail steers it" {
		t.Errorf("source[0] = %+v", src[0])
	}
	if src[1].URL != "https://peeq.example.com/video/def456" {
		t.Errorf("source[1].URL = %q", src[1].URL)
	}
	if lib.lastQuery.Videos != defaultSearchVideos || lib.lastQuery.Keyword {
		t.Errorf("query = %+v, want default k and hybrid", lib.lastQuery)
	}
}

// Looking a tool up must not build the channel hint: that is two queries per
// call for a description nobody reads during a call.
func TestToolCallDoesNotReadTheChannelHint(t *testing.T) {
	lib := searchFixture()
	callTool(t, lib, "search", map[string]any{"query": "x"})
	if lib.channelsN != 0 {
		t.Errorf("Channels called %d times during a search call", lib.channelsN)
	}
}

func TestSearchSchemaListsEveryArgument(t *testing.T) {
	props, _ := searchSchema()["properties"].(map[string]any)
	for _, k := range []string{"query", "channel", "category", "youtube_category", "after", "before", "mode", "k"} {
		if _, ok := props[k]; !ok {
			t.Errorf("schema lacks %s", k)
		}
	}
}

func TestSearch_summaryIsTrimmed(t *testing.T) {
	text := toolText(t, callTool(t, searchFixture(), "search", map[string]any{"query": "x"}))
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "   summary: ") && len([]rune(line)) > 320 {
			t.Errorf("summary line is %d runes", len([]rune(line)))
		}
	}
}

func TestSearch_snippetIsCapped(t *testing.T) {
	lib := searchFixture()
	lib.result.Videos[0].Matches[0].Snippet = strings.Repeat("ä", 500)
	src := sourcesOf(t, callTool(t, lib, "search", map[string]any{"query": "x"}))
	if n := len([]rune(src[0].Snippet)); n > 240 {
		t.Errorf("snippet is %d runes, want <= 240", n)
	}
}

func TestSearch_passesFiltersThrough(t *testing.T) {
	lib := searchFixture()
	r := callTool(t, lib, "search", map[string]any{
		"query": "bikes", "channel": "Veritasium", "category": "science", "youtube_category": "Education",
		"after": "2025-01-01", "before": "2026-12-31", "mode": "keyword", "k": 3,
	})
	if isError(r) {
		t.Fatalf("unexpected error: %s", toolText(t, r))
	}
	q := lib.lastQuery
	if q.Text != "bikes" || q.Channel.IDs[0] != "UCV" || q.Category != "science" || q.YouTubeCategory != "Education" ||
		q.After != "2025-01-01" || q.Before != "2026-12-31" || !q.Keyword || q.Videos != 3 {
		t.Errorf("query = %+v", q)
	}
}

func TestSearch_kIsClamped(t *testing.T) {
	lib := searchFixture()
	callTool(t, lib, "search", map[string]any{"query": "x", "k": 50})
	if lib.lastQuery.Videos != maxSearchVideos {
		t.Errorf("k = %d, want %d", lib.lastQuery.Videos, maxSearchVideos)
	}
}

func TestSearch_argumentErrorsAreToolErrors(t *testing.T) {
	cases := map[string]map[string]any{
		"no query":       {},
		"blank query":    {"query": "  "},
		"bad category":   {"query": "x", "category": "cooking"},
		"bad date":       {"query": "x", "after": "May 2026"},
		"bad mode":       {"query": "x", "mode": "fuzzy"},
		"unknown field":  {"query": "x", "chanel": "Veritasium"},
		"unknown chanel": {"query": "x", "channel": "Verytasium"},
	}
	for name, args := range cases {
		lib := searchFixture()
		r := callTool(t, lib, "search", args)
		if r.body.Error != nil || !isError(r) {
			t.Errorf("%s: want a tool error result, got %s", name, r.raw)
		}
		if lib.lastQuery != nil {
			t.Errorf("%s: search ran despite invalid arguments", name)
		}
	}
}

func TestSearch_unknownChannelListsCloseNames(t *testing.T) {
	r := callTool(t, searchFixture(), "search", map[string]any{"query": "x", "channel": "Verytasium"})
	text := toolText(t, r)
	if !strings.Contains(text, "Verytasium") || !strings.Contains(text, "Veritasium") || !strings.Contains(text, "Vsauce") {
		t.Errorf("text = %q", text)
	}
}

func TestSearch_noResults(t *testing.T) {
	r := callTool(t, &fakeLibrary{}, "search", map[string]any{"query": "nothing here"})
	if isError(r) {
		t.Fatal("no results is not an error")
	}
	if text := toolText(t, r); !strings.Contains(text, "No videos") {
		t.Errorf("text = %q", text)
	}
	if src := sourcesOf(t, r); src == nil || len(src) != 0 {
		t.Errorf("sources = %v, want an empty list", src)
	}
}

func TestSearch_degradedIsSaid(t *testing.T) {
	lib := searchFixture()
	lib.result.SemanticDegraded = true
	if text := toolText(t, callTool(t, lib, "search", map[string]any{"query": "x"})); !strings.Contains(text, "keyword-only") {
		t.Errorf("text = %q", text)
	}
}

func TestSearch_failureIsAToolError(t *testing.T) {
	lib := searchFixture()
	lib.searchErr = fmt.Errorf("disk on fire")
	r := callTool(t, lib, "search", map[string]any{"query": "x"})
	if !isError(r) || strings.Contains(toolText(t, r), "disk on fire") {
		t.Errorf("want a generic tool error, got %q", toolText(t, r))
	}
}

func TestSearch_textIsCapped(t *testing.T) {
	lib := searchFixture()
	long := strings.Repeat("word ", 20000)
	for i := range lib.result.Videos {
		lib.result.Videos[i].Video.Title = long
	}
	text := toolText(t, callTool(t, lib, "search", map[string]any{"query": "x"}))
	if len(text) > maxToolTextBytes {
		t.Errorf("text is %d bytes, cap %d", len(text), maxToolTextBytes)
	}
}

// When the results outgrow the cap, whole trailing items are dropped and
// their sources with them, so sources[i] still matches item i+1.
func TestSearch_overflowDropsWholeItemsAndTheirSources(t *testing.T) {
	lib := &fakeLibrary{}
	for i := 0; i < 10; i++ {
		var ms []Match
		for j := 0; j < 4; j++ {
			ms = append(ms, Match{StartSeconds: 100*j + 1, Kind: rag.KindTranscript, Snippet: strings.Repeat("ü", 400)})
		}
		lib.result.Videos = append(lib.result.Videos, VideoHits{
			Video:   Video{ID: fmt.Sprintf("vid%02d", i), Title: fmt.Sprintf("Video %d %s", i, strings.Repeat("é", 400)), Summary: strings.Repeat("ö", 400)},
			Matches: ms,
		})
	}
	r := callTool(t, lib, "search", map[string]any{"query": "x"})
	text := toolText(t, r)
	if len(text) > maxToolTextBytes {
		t.Fatalf("text is %d bytes", len(text))
	}
	if strings.Contains(text, "[output truncated]") {
		t.Error("the generic cap cut the text; items should have been dropped whole")
	}
	src := sourcesOf(t, r)
	if len(src) == 0 || len(src) == 10 {
		t.Fatalf("sources = %d, want some but not all", len(src))
	}
	if !strings.Contains(text, fmt.Sprintf("(%d more results omitted; narrow the query)", 10-len(src))) {
		t.Errorf("no omitted note for %d dropped results:\n%s", 10-len(src), text[len(text)-200:])
	}
	for i, s := range src {
		if !strings.Contains(text, fmt.Sprintf("%d. %s", i+1, s.Title)) || !strings.Contains(text, "peeq: "+s.URL+" ") {
			t.Errorf("source %d does not match a complete item %d", i, i+1)
		}
	}
	if strings.Contains(text, fmt.Sprintf("\n%d. ", len(src)+1)) {
		t.Errorf("item %d is in the text without a source", len(src)+1)
	}
}

func TestSearch_channelWithoutVideos(t *testing.T) {
	lib := searchFixture()
	lib.scopes["fresh channel"] = ChannelScope{IDs: []string{"UCF"}, Matched: []string{"Fresh Channel"}, NoVideos: true}
	r := callTool(t, lib, "search", map[string]any{"query": "x", "channel": "fresh channel"})
	if isError(r) || !strings.Contains(toolText(t, r), "No videos yet in Fresh Channel") {
		t.Errorf("got %q", toolText(t, r))
	}
	if lib.lastQuery != nil {
		t.Error("searched a channel known to hold no videos")
	}
}

func TestSearch_timeoutIsSaid(t *testing.T) {
	lib := searchFixture()
	lib.searchErr = fmt.Errorf("lanes: %w", context.DeadlineExceeded)
	r := callTool(t, lib, "search", map[string]any{"query": "x"})
	if !isError(r) || !strings.Contains(toolText(t, r), "search failed (timed out)") {
		t.Errorf("got %q", toolText(t, r))
	}
}

func TestGetVideo(t *testing.T) {
	lib := &fakeLibrary{videos: map[string]*Video{"abc123": {
		ID: "abc123", Title: "Why bikes stay upright", Channel: "Veritasium", PublishedAt: "2026-05-02",
		DurationSeconds: 3725, Category: "science", YouTubeCategories: []string{"Education"},
		Tags:      []string{"bikes", "physics"},
		Summary:   "Bikes steer into their fall.",
		KeyPoints: []Moment{{Seconds: 61, Text: "Trail matters"}},
		Chapters:  []Moment{{Seconds: 0, Text: "Intro"}, {Seconds: 3700, Text: "Outro"}},
		InDepth:   "## Trail\nThe contact patch sits behind the steering axis.",
	}}}
	r := callTool(t, lib, "get_video", map[string]any{"id": "abc123"})
	text := toolText(t, r)
	for _, want := range []string{
		"Why bikes stay upright", "channel: Veritasium", "published: 2026-05-02", "duration: 1:02:05",
		"category: science", "youtube categories: Education", "tags: bikes, physics",
		"Bikes steer into their fall.", "[01:01] Trail matters", "[1:01:40] Outro",
		"The contact patch sits behind", "https://peeq.example.com/video/abc123", "https://youtu.be/abc123",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q\n---\n%s", want, text)
		}
	}
	src := sourcesOf(t, r)
	if len(src) != 1 || src[0].URL != "https://peeq.example.com/video/abc123" || src[0].Label != "peeq · Veritasium" {
		t.Errorf("sources = %+v", src)
	}
}

func TestGetVideo_tagsAreTrimmed(t *testing.T) {
	tags := make([]string, 40)
	for i := range tags {
		tags[i] = fmt.Sprintf("tag%02d", i)
	}
	lib := &fakeLibrary{videos: map[string]*Video{"v": {ID: "v", Title: "t", Tags: tags}}}
	text := toolText(t, callTool(t, lib, "get_video", map[string]any{"id": "v"}))
	if strings.Contains(text, "tag15") || !strings.Contains(text, "tag14") {
		t.Errorf("tags not trimmed to 15: %q", text)
	}
}

func TestGetVideo_unknownIsAToolError(t *testing.T) {
	r := callTool(t, &fakeLibrary{}, "get_video", map[string]any{"id": "nope"})
	if !isError(r) {
		t.Fatalf("want a tool error, got %s", r.raw)
	}
}

func transcriptFixture(n int) *fakeLibrary {
	cues := make([]subtitles.Cue, n)
	for i := range cues {
		cues[i] = subtitles.Cue{StartSeconds: i * 3, Text: fmt.Sprintf("line %d of the talk", i)}
	}
	return &fakeLibrary{transcripts: map[string]*Transcript{"v": {VideoID: "v", Title: "Talk", Cues: cues}}}
}

func TestGetTranscript_window(t *testing.T) {
	r := callTool(t, transcriptFixture(100), "get_transcript", map[string]any{"id": "v", "start_seconds": 60, "end_seconds": 90})
	text := toolText(t, r)
	if !strings.Contains(text, "[01:00] line 20 of the talk") {
		t.Errorf("text lacks the window start\n%s", text)
	}
	if strings.Contains(text, "line 19 ") || strings.Contains(text, "line 30 ") {
		t.Errorf("text leaks outside the window\n%s", text)
	}
	if strings.Contains(text, "truncated") {
		t.Errorf("a small window must not be truncated\n%s", text)
	}
}

func TestGetTranscript_truncatesAndSaysWhereToContinue(t *testing.T) {
	r := callTool(t, transcriptFixture(5000), "get_transcript", map[string]any{"id": "v"})
	text := toolText(t, r)
	if len(text) > maxToolTextBytes {
		t.Fatalf("text is %d bytes", len(text))
	}
	idx := strings.Index(text, "start_seconds=")
	if idx < 0 {
		t.Fatalf("no continuation hint in\n%s", text[len(text)-300:])
	}
	var next int
	fmt.Sscanf(text[idx+len("start_seconds="):], "%d", &next)
	if next <= 0 {
		t.Fatalf("next start = %d", next)
	}
	// The continuation starts exactly after the last line shown.
	r2 := callTool(t, transcriptFixture(5000), "get_transcript", map[string]any{"id": "v", "start_seconds": next})
	first := strings.SplitN(toolText(t, r2), "\n", 3)[1]
	if !strings.Contains(first, fmt.Sprintf("[%s]", clock(next))) {
		t.Errorf("continuation starts with %q, want [%s]", first, clock(next))
	}
	if strings.Contains(text, fmt.Sprintf("[%s]", clock(next))) {
		t.Errorf("the continuation start was already shown")
	}
}

// Several cues share each second. Paging through the transcript by the
// advertised start_seconds must show every cue exactly once.
func TestGetTranscript_pagingNeverRepeatsACue(t *testing.T) {
	var cues []subtitles.Cue
	for i := 0; i < 6000; i++ {
		cues = append(cues, subtitles.Cue{StartSeconds: i / 3, Text: fmt.Sprintf("cue%05d %s", i, strings.Repeat("x", 40))})
	}
	lib := &fakeLibrary{transcripts: map[string]*Transcript{"v": {VideoID: "v", Cues: cues}}}
	seen := map[string]int{}
	start, pages := 0, 0
	for {
		pages++
		if pages > 50 {
			t.Fatal("paging does not terminate")
		}
		text := toolText(t, callTool(t, lib, "get_transcript", map[string]any{"id": "v", "start_seconds": start}))
		for _, f := range strings.Fields(text) {
			if strings.HasPrefix(f, "cue") {
				seen[f]++
			}
		}
		idx := strings.Index(text, "start_seconds=")
		if idx < 0 {
			break
		}
		var next int
		fmt.Sscanf(text[idx+len("start_seconds="):], "%d", &next)
		if next <= start {
			t.Fatalf("next start %d does not advance past %d", next, start)
		}
		start = next
	}
	if pages < 2 {
		t.Fatal("fixture too small to page")
	}
	for i := 0; i < len(cues); i++ {
		key := fmt.Sprintf("cue%05d", i)
		if seen[key] != 1 {
			t.Fatalf("%s shown %d times over %d pages", key, seen[key], pages)
		}
	}
}

// One second carrying more text than the whole budget: the reply must cut it
// and point past it, never at the start it was asked for.
func TestGetTranscript_oversizedSecondStillAdvances(t *testing.T) {
	var cues []subtitles.Cue
	for i := 0; i < 2000; i++ {
		cues = append(cues, subtitles.Cue{StartSeconds: 5, Text: strings.Repeat("wörd ", 10)})
	}
	cues = append(cues, subtitles.Cue{StartSeconds: 9, Text: "after the wall"})
	lib := &fakeLibrary{transcripts: map[string]*Transcript{"v": {VideoID: "v", Cues: cues}}}
	text := toolText(t, callTool(t, lib, "get_transcript", map[string]any{"id": "v", "start_seconds": 5}))
	if len(text) > maxToolTextBytes || !utf8.ValidString(text) {
		t.Fatalf("text is %d bytes, valid utf-8 %v", len(text), utf8.ValidString(text))
	}
	if !strings.Contains(text, "start_seconds=9 ") {
		t.Fatalf("want a resume at 9, got tail %q", text[len(text)-200:])
	}
	next := toolText(t, callTool(t, lib, "get_transcript", map[string]any{"id": "v", "start_seconds": 9}))
	if !strings.Contains(next, "[00:09] after the wall") {
		t.Errorf("resume page = %q", next)
	}

	// The wall is the whole transcript: cut, with no continuation at all.
	lib.transcripts["v"].Cues = cues[:2000]
	text = toolText(t, callTool(t, lib, "get_transcript", map[string]any{"id": "v", "start_seconds": 5}))
	if strings.Contains(text, "start_seconds=") || !strings.Contains(text, "[line cut at the size limit]") {
		t.Errorf("tail = %q", text[len(text)-200:])
	}
}

func TestGetTranscript_missing(t *testing.T) {
	if r := callTool(t, &fakeLibrary{}, "get_transcript", map[string]any{"id": "x"}); !isError(r) {
		t.Fatalf("want a tool error, got %s", r.raw)
	}
}

func TestListChannels(t *testing.T) {
	lib := &fakeLibrary{channels: []Channel{
		{ID: "UCV", Name: "Veritasium", Handle: "@veritasium", Description: "An element of truth.",
			Subscribed: true, KeepReads: true, Videos: 42,
			TopCategories:        []CategoryCount{{"science", 30}, {"engineering", 8}},
			TopYouTubeCategories: []CategoryCount{{"Education", 40}}},
		{ID: "UCX", Name: "Other", Subscribed: true, Videos: 3},
	}}
	text := toolText(t, callTool(t, lib, "list_channels", map[string]any{}))
	for _, want := range []string{
		"Veritasium (@veritasium)", "id UCV", "videos: 42", "science (30), engineering (8)",
		"youtube: Education (40)", "An element of truth.", "Other", "caption-only reads kept",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q\n---\n%s", want, text)
		}
	}
}

func TestSources_carryTheYouTubeThumbnail(t *testing.T) {
	lib := searchFixture()
	lib.result.Videos[0].Video.ID = "dQw4w9WgXcQ"
	src := sourcesOf(t, callTool(t, lib, "search", map[string]any{"query": "x"}))
	if src[0].Image != "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg" {
		t.Errorf("image = %q", src[0].Image)
	}
	// "def456" is not shaped like a YouTube id: no image at all.
	if src[1].Image != "" {
		t.Errorf("image = %q, want none", src[1].Image)
	}
	r := callTool(t, lib, "search", map[string]any{"query": "x"})
	raw, _ := json.Marshal(r.body.Result["structuredContent"])
	if strings.Count(string(raw), `"image"`) != 1 {
		t.Errorf("structuredContent = %s, want image omitted when unknown", raw)
	}

	vlib := &fakeLibrary{videos: map[string]*Video{"dQw4w9WgXcQ": {ID: "dQw4w9WgXcQ", Title: "t"}}}
	vsrc := sourcesOf(t, callTool(t, vlib, "get_video", map[string]any{"id": "dQw4w9WgXcQ"}))
	if len(vsrc) != 1 || vsrc[0].Image != "https://i.ytimg.com/vi/dQw4w9WgXcQ/mqdefault.jpg" {
		t.Errorf("get_video sources = %+v", vsrc)
	}
}

func TestClock(t *testing.T) {
	for in, want := range map[int]string{0: "00:00", 59: "00:59", 754: "12:34", 3600: "1:00:00", 3725: "1:02:05"} {
		if got := clock(in); got != want {
			t.Errorf("clock(%d) = %q, want %q", in, got, want)
		}
	}
}
