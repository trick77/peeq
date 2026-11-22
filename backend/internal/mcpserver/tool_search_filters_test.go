package mcpserver

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestSearchSchemaListsTheScopeArguments(t *testing.T) {
	props, _ := searchSchema()["properties"].(map[string]any)
	for k, typ := range map[string]string{"video_id": "string", "watched": "boolean", "favorite": "boolean", "channels": "array"} {
		p, ok := props[k].(map[string]any)
		if !ok {
			t.Errorf("schema lacks %s", k)
			continue
		}
		if p["type"] != typ {
			t.Errorf("%s type = %v, want %s", k, p["type"], typ)
		}
		if d, _ := p["description"].(string); d == "" {
			t.Errorf("%s has no description", k)
		}
	}
	// No lower bound: clients send [] for an optional list.
	ch, _ := props["channels"].(map[string]any)
	if _, ok := ch["minItems"]; ok || ch["maxItems"] != maxSearchChannels || maxSearchChannels != 4 {
		t.Errorf("channels bounds = %v..%v", ch["minItems"], ch["maxItems"])
	}
	if items, _ := ch["items"].(map[string]any); items["type"] != "string" {
		t.Errorf("channels items = %v", ch["items"])
	}
	// The old string argument stays: clients cache tools/list.
	if c, _ := props["channel"].(map[string]any); c["type"] != "string" {
		t.Errorf("channel = %v", props["channel"])
	}
	if !strings.Contains(searchDescription, "video_id") {
		t.Errorf("search description does not mention single-video search: %q", searchDescription)
	}
}

func TestSearch_scopeArgumentErrorsAreToolErrors(t *testing.T) {
	cases := map[string]map[string]any{
		"playlist video_id":  {"query": "x", "video_id": "https://www.youtube.com/playlist?list=PL1"},
		"channel link":       {"query": "x", "video_id": "https://www.youtube.com/@veritasium"},
		"five channels":      {"query": "x", "channels": []string{"a", "b", "c", "d", "e"}},
		"channels as string": {"query": "x", "channels": "veritasium"},
		"watched as string":  {"query": "x", "watched": "yes"},
		"favorite as number": {"query": "x", "favorite": 1},
	}
	for name, args := range cases {
		lib := searchFixture()
		r := callTool(t, lib, "search", args)
		if r.body.Error != nil || !isError(r) {
			t.Errorf("%s: want a tool error result, got %s", name, r.raw)
		}
		if lib.lastQuery != nil || len(lib.readIDs) > 0 || len(lib.gateIDs) > 0 || lib.resolveN > 0 {
			t.Errorf("%s: the library was read despite invalid arguments", name)
		}
	}
	r := callTool(t, searchFixture(), "search", map[string]any{"query": "x", "channels": []string{"a", "b", "c", "d", "e"}})
	if want := "channels takes at most 4 channels, got 5"; toolText(t, r) != want {
		t.Errorf("five channels = %q", toolText(t, r))
	}
}

// An empty list and blank entries are what clients send for an optional
// argument: absent, like a blank channel or video_id.
func TestSearch_emptyChannelsAreAbsent(t *testing.T) {
	for _, chans := range [][]string{{}, {" ", ""}} {
		lib := searchFixture()
		r := callTool(t, lib, "search", map[string]any{"query": "x", "channels": chans, "channel": " ", "video_id": ""})
		if isError(r) || lib.lastQuery == nil || lib.lastQuery.Channel.Resolved() || lib.resolveN != 0 || len(lib.gateIDs) != 0 {
			t.Errorf("%q: %v %q", chans, isError(r), toolText(t, r))
		}
	}
	// Blanks do not count toward the bound.
	lib := multiChannelFixture()
	r := callTool(t, lib, "search", map[string]any{"query": "x", "channels": []string{"vsauce", "", "legacy", " ", "veritasium", "fresh"}})
	if isError(r) || lib.lastQuery == nil {
		t.Errorf("four entries among blanks: %q", toolText(t, r))
	}
}

// A video_id that reduces to no id names the argument, and neither reaches the
// library nor a log: it may be a URL with a query.
func TestSearch_badVideoIDNamesTheArgument(t *testing.T) {
	logs := captureLog(t)
	r := callTool(t, searchFixture(), "search", map[string]any{"query": "x", "video_id": "https://www.youtube.com/playlist?list=PLsecret"})
	text := toolText(t, r)
	if !isError(r) || !strings.HasPrefix(text, `video_id "https://www.youtube.com/playlist?list=PLsecret" is not a video id or video link.`) {
		t.Errorf("text = %q", text)
	}
	if strings.Contains(logs.String(), "PLsecret") || !strings.Contains(logs.String(), "video_id=invalid") {
		t.Errorf("log:\n%s", logs.String())
	}
}

func TestSearch_videoIDTakesTheGetVideoForms(t *testing.T) {
	for _, arg := range []string{"dQw4w9WgXcQ", " https://youtu.be/dQw4w9WgXcQ?t=12 ", "https://peeq.example.com/video/dQw4w9WgXcQ?t=754",
		"<https://www.youtube.com/watch?v=dQw4w9WgXcQ>"} {
		lib := searchFixture()
		r := callTool(t, lib, "search", map[string]any{"query": "x", "video_id": arg, "k": 50})
		if isError(r) {
			t.Fatalf("%q: %s", arg, toolText(t, r))
		}
		if lib.lastQuery.VideoID != "dQw4w9WgXcQ" || !slices.Equal(lib.gateIDs, []string{"dQw4w9WgXcQ"}) {
			t.Errorf("%q: VideoID = %q, gate asked %q", arg, lib.lastQuery.VideoID, lib.gateIDs)
		}
	}
	lib := searchFixture()
	callTool(t, lib, "search", map[string]any{"query": "x", "video_id": "  "})
	if lib.lastQuery == nil || lib.lastQuery.VideoID != "" || len(lib.gateIDs) != 0 {
		t.Errorf("a blank video_id is no filter: %+v", lib.lastQuery)
	}
}

// Unknown and not searchable are two different answers, both naming the id,
// and neither runs a search.
func TestSearch_videoMissTexts(t *testing.T) {
	logs := captureLog(t)
	lib := searchFixture()
	lib.gateErr = ErrVideoNotFound
	r := callTool(t, lib, "search", map[string]any{"query": "x", "video_id": "nope123"})
	if want := `No video with id "nope123" in peeq. Use the id from a search result.`; !isError(r) || toolText(t, r) != want {
		t.Errorf("unknown = %v %q", isError(r), toolText(t, r))
	}
	lib.gateErr = ErrVideoNotSearchable
	r = callTool(t, lib, "search", map[string]any{"query": "x", "video_id": "inbox12"})
	if want := `Video "inbox12" is in peeq but not searchable: nothing of it is indexed yet. ` +
		`Call get_video for its details or get_transcript for its transcript.`; !isError(r) || toolText(t, r) != want {
		t.Errorf("not searchable = %v %q", isError(r), toolText(t, r))
	}
	if lib.lastQuery != nil {
		t.Error("searched a video the gate refused")
	}
	for _, want := range []string{`msg="mcp: search video miss" video_id=nope123 reason=unknown_video`,
		`msg="mcp: search video miss" video_id=inbox12 reason=not_indexed`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, logs.String())
		}
	}
	if strings.Contains(logs.String(), "search failed") {
		t.Errorf("a miss logged as a failure:\n%s", logs.String())
	}
	lib.gateErr = errors.New("disk on fire")
	r = callTool(t, lib, "search", map[string]any{"query": "x", "video_id": "inbox12"})
	if text := toolText(t, r); !isError(r) || !strings.HasPrefix(text, "search failed (") || strings.Contains(text, "disk") {
		t.Errorf("gate failure = %v %q", isError(r), text)
	}
}

// The video is checked before any channel: a channel without videos must not
// hide an unknown video_id.
func TestSearch_videoGateRunsBeforeChannels(t *testing.T) {
	lib := multiChannelFixture()
	lib.gateErr = ErrVideoNotFound
	r := callTool(t, lib, "search", map[string]any{"query": "x", "video_id": "nope123", "channel": "fresh"})
	if !isError(r) || !strings.HasPrefix(toolText(t, r), `No video with id "nope123"`) || lib.resolveN != 0 {
		t.Errorf("%v %q, resolves %d", isError(r), toolText(t, r), lib.resolveN)
	}
}

// watched and favorite are three-valued: absent is no constraint, not false.
func TestSearch_watchedAndFavoriteAreThreeValued(t *testing.T) {
	lib := searchFixture()
	callTool(t, lib, "search", map[string]any{"query": "x"})
	if q := lib.lastQuery; q.Watched != nil || q.Favorite != nil {
		t.Errorf("absent: %+v", q)
	}
	for _, v := range []bool{true, false} {
		lib = searchFixture()
		callTool(t, lib, "search", map[string]any{"query": "x", "watched": v, "favorite": !v})
		q := lib.lastQuery
		if q.Watched == nil || *q.Watched != v || q.Favorite == nil || *q.Favorite != !v {
			t.Errorf("watched=%v favorite=%v: %+v", v, !v, q)
		}
	}
}

func multiChannelFixture() *fakeLibrary {
	lib := searchFixture()
	lib.scopes["vsauce"] = ChannelScope{IDs: []string{"UCS"}, Matched: []string{"Vsauce"}}
	lib.scopes["legacy"] = ChannelScope{Names: []string{"Old Name"}, Matched: []string{"Old Name"}}
	lib.scopes["fresh"] = ChannelScope{IDs: []string{"UCF"}, Matched: []string{"Fresh"}, NoVideos: true}
	lib.scopes["fresher"] = ChannelScope{IDs: []string{"UCG"}, Matched: []string{"Fresher"}, NoVideos: true}
	return lib
}

// Every name resolves against one directory read.
func TestSearch_channelsAndChannelAreUnioned(t *testing.T) {
	lib := multiChannelFixture()
	r := callTool(t, lib, "search", map[string]any{"query": "x", "channel": "Veritasium",
		"channels": []string{"vsauce", "legacy", "veritasium"}})
	if isError(r) {
		t.Fatal(toolText(t, r))
	}
	q := lib.lastQuery
	if !slices.Equal(q.Channel.IDs, []string{"UCV", "UCS"}) || !slices.Equal(q.Channel.Names, []string{"Old Name"}) ||
		!slices.Equal(q.Channel.Matched, []string{"Veritasium", "Vsauce", "Old Name"}) {
		t.Errorf("scope = %+v", q.Channel)
	}
	if lib.resolveN != 1 {
		t.Errorf("%d directory reads for one call", lib.resolveN)
	}
	lib = multiChannelFixture()
	callTool(t, lib, "search", map[string]any{"query": "x", "channels": []string{"vsauce"}})
	if !slices.Equal(lib.lastQuery.Channel.IDs, []string{"UCS"}) {
		t.Errorf("channels alone: %+v", lib.lastQuery.Channel)
	}
}

// One entry that resolves to nothing fails the call with that entry's miss:
// never a silent search over fewer channels.
func TestSearch_channelsEntryMissNamesIt(t *testing.T) {
	lib := multiChannelFixture()
	r := callTool(t, lib, "search", map[string]any{"query": "x", "channels": []string{"vsauce", "nobody", "veritasium"}})
	if want := `No channel in peeq matches "nobody". Closest: Veritasium, Vsauce. Call list_youtube_channels for every channel, or search without a channel.`; !isError(r) || toolText(t, r) != want {
		t.Errorf("miss = %v %q", isError(r), toolText(t, r))
	}
	if lib.lastQuery != nil {
		t.Error("searched fewer channels than asked")
	}
}

// A channel without videos among others is searched past but said, results or
// not; only when every channel is empty is the answer the no-videos sentence.
func TestSearch_channelsWithoutVideos(t *testing.T) {
	lib := multiChannelFixture()
	r := callTool(t, lib, "search", map[string]any{"query": "x", "channels": []string{"fresh", "vsauce"}})
	if isError(r) || lib.lastQuery == nil || !slices.Contains(lib.lastQuery.Channel.IDs, "UCS") {
		t.Fatalf("one empty channel among others: %v %q", isError(r), toolText(t, r))
	}
	if text := toolText(t, r); !strings.HasPrefix(text, "No videos yet in Fresh.\n\n- Why bikes stay upright") {
		t.Errorf("results:\n%s", text)
	}
	lib.result = SearchResult{}
	r = callTool(t, lib, "search", map[string]any{"query": "x", "channels": []string{"fresh", "vsauce"}})
	if text := toolText(t, r); !strings.HasPrefix(text, "No videos yet in Fresh.\n\nNo videos in peeq match this search.") {
		t.Errorf("empty result:\n%s", text)
	}

	lib = multiChannelFixture()
	r = callTool(t, lib, "search", map[string]any{"query": "x", "channel": "fresh", "channels": []string{"fresher"}})
	if isError(r) || toolText(t, r) != "No videos yet in Fresh, Fresher: peeq follows these channels but holds none of their videos so far." {
		t.Errorf("all empty = %v %q", isError(r), toolText(t, r))
	}
	if lib.lastQuery != nil {
		t.Error("searched channels known to hold no videos")
	}
	if src, ok := r.body.Result["structuredContent"].(map[string]any)["sources"].([]any); !ok || len(src) != 0 {
		t.Errorf("structuredContent = %v, want sources: []", r.body.Result["structuredContent"])
	}
	// One channel keeps its text byte for byte.
	r = callTool(t, multiChannelFixture(), "search", map[string]any{"query": "x", "channels": []string{"fresh"}})
	if toolText(t, r) != "No videos yet in Fresh: peeq follows the channel but holds none of its videos so far." {
		t.Errorf("one empty = %q", toolText(t, r))
	}
}

func TestSearch_headerShowsWatchedAndFavorite(t *testing.T) {
	lib := searchFixture()
	lib.result.Videos[0].Video.Watched = true
	lib.result.Videos[0].Video.Favorite = true
	lib.result.Videos[1].Video.Favorite = true
	text := toolText(t, callTool(t, lib, "search", map[string]any{"query": "x"}))
	for _, want := range []string{
		"- Why bikes stay upright — Veritasium, 2026-05-02, category science · youtube: Science & Technology, Education · watched · favorite\n",
		"- Summary only — Vsauce, category education · favorite\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q\n---\n%s", want, text)
		}
	}
	lib = searchFixture()
	if text := toolText(t, callTool(t, lib, "search", map[string]any{"query": "x"})); strings.Contains(text, "watched") || strings.Contains(text, "favorite") {
		t.Errorf("unset state shown:\n%s", text)
	}
}

func TestGetVideo_showsWatchedAndFavorite(t *testing.T) {
	lib := &fakeLibrary{videos: map[string]*Video{"abc123": {ID: "abc123", Title: "T", Channel: "Veritasium",
		Watched: true, Favorite: true}}}
	if text := toolText(t, callTool(t, lib, "get_video", map[string]any{"id": "abc123"})); !strings.Contains(text, "channel: Veritasium · watched · favorite\n") {
		t.Errorf("text = %q", text)
	}
}

func TestSearch_noResultsHintCoversTheScopeArguments(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"watched":  {"watched": false},
		"favorite": {"favorite": true},
		"channels": {"channels": []string{"vsauce"}},
	} {
		args["query"] = "x"
		lib := multiChannelFixture()
		lib.result = SearchResult{}
		if text := toolText(t, callTool(t, lib, "search", args)); !strings.Contains(text, "Try again without some of the filters.") {
			t.Errorf("%s: %q", name, text)
		}
	}
}

// Nothing matching inside one video is said of that video, and never tells
// the model to drop video_id.
func TestSearch_noMatchInOneVideo(t *testing.T) {
	lib := multiChannelFixture()
	lib.result = SearchResult{}
	r := callTool(t, lib, "search", map[string]any{"query": "x", "video_id": "abc123"})
	if want := `Nothing in video "abc123" matches this search. Call get_transcript to read it, or get_video for its summary.`; isError(r) || toolText(t, r) != want {
		t.Errorf("alone = %v %q", isError(r), toolText(t, r))
	}
	r = callTool(t, lib, "search", map[string]any{"query": "x", "video_id": "abc123", "watched": true})
	if want := `Nothing in video "abc123" matches this search. Call get_transcript to read it, or get_video for its summary. ` +
		`Or keep video_id and try again without the other filters.`; toolText(t, r) != want {
		t.Errorf("with a filter = %q", toolText(t, r))
	}
}
