package mcpserver

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// channelLine returns the list's line for one channel.
func channelLine(t *testing.T, text, name string) string {
	t.Helper()
	i := slices.IndexFunc(strings.Split(text, "\n"), func(l string) bool { return strings.HasPrefix(l, "- "+name+" ") })
	if i < 0 {
		t.Fatalf("no line for %s:\n%s", name, text)
	}
	return strings.Split(text, "\n")[i]
}

// The searchable number shows only when it is smaller: a client that read
// "videos: 9" and found 5 by search kept looking for the other 4.
func TestListYouTubeChannels_searchableCount(t *testing.T) {
	lib := &fakeLibrary{
		channels: []Channel{
			{ID: "UCs", Name: "Some", Subscribed: true, Videos: 9},
			{ID: "UCa", Name: "All", Subscribed: true, Videos: 3},
			{ID: "UCn", Name: "None", Videos: 4},
			// The two counts are separate reads: one racing a new video is no news.
			{ID: "UCr", Name: "Raced", Subscribed: true, KeepReads: true, Videos: 2},
		},
		searchable: map[string]int{"UCs": 5, "UCa": 3, "UCr": 3},
	}
	text := toolText(t, callTool(t, lib, "list_youtube_channels", map[string]any{}))
	for name, want := range map[string]string{
		"Some":  "- Some · id UCs · videos: 9 (5 searchable)",
		"All":   "- All · id UCa · videos: 3",
		"None":  "- None · id UCn · videos: 4 (0 searchable) · not subscribed",
		"Raced": "- Raced · id UCr · videos: 2 · caption-only reads kept",
	} {
		if got := channelLine(t, text, name); got != want {
			t.Errorf("%s: line = %q, want %q", name, got, want)
		}
	}
	if strings.Contains(text, "unavailable") {
		t.Errorf("counts known, yet the list says they are not:\n%s", text)
	}
}

// Without the searchable counts a plain "videos: 9" would read as "all
// searchable": the list says once, up front, that it cannot tell.
func TestListYouTubeChannels_searchableCountUnavailable(t *testing.T) {
	logs := captureLog(t)
	lib := &fakeLibrary{
		channels:      []Channel{{ID: "UCs", Name: "Some", Subscribed: true, Videos: 9}, {ID: "UCa", Name: "All", Subscribed: true, Videos: 3}},
		searchableErr: errors.New("channel searchable counts: sqlite3: interrupted"),
	}
	r := callTool(t, lib, "list_youtube_channels", map[string]any{})
	text := toolText(t, r)
	const note = "Searchable counts are unavailable right now; a channel's video count may include videos search cannot find."
	lines := strings.Split(text, "\n")
	if isError(r) || strings.Count(text, note) != 1 || !slices.Contains(lines, note) {
		t.Errorf("isError=%v, want the note once on its own line:\n%s", isError(r), text)
	}
	if i := slices.Index(lines, note); i < 0 || i > slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "- ") }) {
		t.Errorf("the note must come before the channels:\n%s", text)
	}
	if got := channelLine(t, text, "Some"); got != "- Some · id UCs · videos: 9" {
		t.Errorf("line = %q", got)
	}
	if !strings.Contains(logs.String(), `level=WARN msg="mcp: channel searchable counts unavailable" err="channel searchable counts: sqlite3: interrupted"`) {
		t.Errorf("log = %q", logs.String())
	}
}

// The count scans the whole library: only list_youtube_channels pays for it,
// never the hint or a channel-scoped search or list_videos.
func TestSearchableCounts_readOnlyByTheChannelList(t *testing.T) {
	lib := &fakeLibrary{
		channels: []Channel{{ID: "UCV", Name: "Veritasium", Subscribed: true, Videos: 1}},
		scopes:   map[string]ChannelScope{"veritasium": {IDs: []string{"UCV"}, Matched: []string{"Veritasium"}}},
	}
	h := newTestHandler(lib)
	modernCall(t, h, "server/discover", nil)
	modernCall(t, h, "tools/list", nil)
	callTool(t, lib, "search", map[string]any{"query": "x", "channel": "Veritasium"})
	callTool(t, lib, "list_videos", map[string]any{"channel": "Veritasium"})
	if lib.searchableN != 0 {
		t.Fatalf("searchable counts read %d times outside list_youtube_channels", lib.searchableN)
	}
	callTool(t, lib, "list_youtube_channels", map[string]any{})
	if lib.searchableN != 1 {
		t.Errorf("list_youtube_channels read the counts %d times, want 1", lib.searchableN)
	}
}

// The first line sizes the library: every video peeq holds, the searchable
// ones with their runtime, and the release dates.
func TestListYouTubeChannels_totalsLine(t *testing.T) {
	for name, tc := range map[string]struct {
		totals LibraryTotals
		listed int
		want   string
	}{
		"full": {LibraryTotals{Videos: 412, Searchable: 380, SearchableSeconds: 212*3600 + 1000,
			Oldest: "2019-03-02", Newest: "2026-11-20"}, 9,
			"Library: 412 videos, 380 searchable (212 h), released 2019-03-02 to 2026-11-20."},
		"short runtime, one video": {LibraryTotals{Videos: 1, Searchable: 1, SearchableSeconds: 1790,
			Oldest: "2026-01-01", Newest: "2026-01-01"}, 1,
			"Library: 1 video, 1 searchable (30 min), released 2026-01-01 to 2026-01-01."},
		"no runtime, no release dates": {LibraryTotals{Videos: 5, Searchable: 3}, 4,
			"Library: 5 videos, 3 searchable."},
		"nothing searchable": {LibraryTotals{Videos: 2, Oldest: "2024-03-03", Newest: "2026-06-01"}, 2,
			"Library: 2 videos, none searchable yet, released 2024-03-03 to 2026-06-01."},
		"empty": {LibraryTotals{}, 0, "Library: no videos yet."},
		// Separate reads racing a write: the total is raised to what the
		// other numbers prove, never shown below them.
		"searchable above the total": {LibraryTotals{Videos: 3, Searchable: 4, SearchableSeconds: 7200}, 2,
			"Library: 4 videos, 4 searchable (2 h)."},
		"total below the listed channels": {LibraryTotals{Videos: 0}, 9,
			"Library: 9 videos, none searchable yet."},
	} {
		lib := &fakeLibrary{channels: []Channel{{ID: "UCs", Name: "Some", Subscribed: true, Videos: tc.listed}},
			totals: tc.totals, searchable: map[string]int{"UCs": tc.listed}}
		text := toolText(t, callTool(t, lib, "list_youtube_channels", map[string]any{}))
		first, rest, _ := strings.Cut(text, "\n")
		if first != tc.want {
			t.Errorf("%s: first line = %q, want %q", name, first, tc.want)
		}
		if !strings.HasPrefix(rest, "1 channels.") {
			t.Errorf("%s: the channel list does not follow:\n%s", name, text)
		}
	}
}

// Hours from the point where minutes would round to 60.
func TestRuntimeText(t *testing.T) {
	for sec, want := range map[int64]string{
		0: "", 1: "1 min", 89: "1 min", 3569: "59 min", 3570: "1 h", 3599: "1 h", 3600: "1 h", 5399: "1 h", 5400: "2 h",
	} {
		if got := runtimeText(sec); got != want {
			t.Errorf("runtimeText(%d) = %q, want %q", sec, got, want)
		}
	}
}

// Totals are an extra on the list: a failed read drops the line, logged.
func TestListYouTubeChannels_totalsFailureKeepsTheList(t *testing.T) {
	logs := captureLog(t)
	lib := &fakeLibrary{channels: []Channel{{ID: "UCs", Name: "Some", Subscribed: true, Videos: 9}},
		searchable: map[string]int{"UCs": 9}, totalsErr: errors.New("library span: sqlite3: interrupted")}
	r := callTool(t, lib, "list_youtube_channels", map[string]any{})
	text := toolText(t, r)
	if isError(r) || !strings.HasPrefix(text, "1 channels.") || !strings.Contains(text, "- Some · id UCs · videos: 9") {
		t.Errorf("isError=%v text:\n%s", isError(r), text)
	}
	if !strings.Contains(logs.String(), `level=WARN msg="mcp: library totals unavailable" err="library span: sqlite3: interrupted"`) {
		t.Errorf("log = %q", logs.String())
	}
}

// The description says what "searchable" means once, and where the rest is.
func TestListYouTubeChannels_descriptionExplainsSearchable(t *testing.T) {
	var desc string
	for _, tool := range (&handler{lib: &fakeLibrary{}}).tools() {
		if tool.Name == "list_youtube_channels" {
			desc = tool.Description
		}
	}
	for _, want := range []string{"searchable", "Only searchable videos can be found by search; list_videos lists all of them."} {
		if !strings.Contains(desc, want) {
			t.Errorf("description lacks %q: %q", want, desc)
		}
	}
}

// A library without listed channels (videos added by link only) still says
// how big it is, and reads no per-channel counts.
func TestListYouTubeChannels_totalsWithoutChannels(t *testing.T) {
	lib := &fakeLibrary{totals: LibraryTotals{Videos: 2, Oldest: "2024-03-03", Newest: "2026-06-01"}}
	text := toolText(t, callTool(t, lib, "list_youtube_channels", map[string]any{}))
	if want := "Library: 2 videos, none searchable yet, released 2024-03-03 to 2026-06-01.\npeeq has no channels yet."; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
	if lib.searchableN != 0 {
		t.Errorf("searchable counts read for an empty channel list")
	}
}
