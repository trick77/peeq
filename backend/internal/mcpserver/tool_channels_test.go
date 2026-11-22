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

// The searchable number shows only when it is known and smaller: a client
// that read "videos: 9" and found 5 by search kept looking for the other 4.
func TestListYouTubeChannels_searchableCount(t *testing.T) {
	lib := &fakeLibrary{channels: []Channel{
		{ID: "UCs", Name: "Some", Subscribed: true, Videos: 9, Searchable: 5, SearchableKnown: true},
		{ID: "UCa", Name: "All", Subscribed: true, Videos: 3, Searchable: 3, SearchableKnown: true},
		{ID: "UCn", Name: "None", Videos: 4, SearchableKnown: true},
		{ID: "UCu", Name: "Unknown", Subscribed: true, Videos: 7},
		// The two counts are separate reads: one racing a new video is no news.
		{ID: "UCr", Name: "Raced", Subscribed: true, KeepReads: true, Videos: 2, Searchable: 3, SearchableKnown: true},
	}}
	text := toolText(t, callTool(t, lib, "list_youtube_channels", map[string]any{}))
	for name, want := range map[string]string{
		"Some":    "- Some · id UCs · videos: 9 (5 searchable)",
		"All":     "- All · id UCa · videos: 3",
		"None":    "- None · id UCn · videos: 4 (0 searchable) · not subscribed",
		"Unknown": "- Unknown · id UCu · videos: 7",
		"Raced":   "- Raced · id UCr · videos: 2 · caption-only reads kept",
	} {
		if got := channelLine(t, text, name); got != want {
			t.Errorf("%s: line = %q, want %q", name, got, want)
		}
	}
}

// The first line sizes the library: every video peeq holds, the searchable
// ones with their runtime and channels, and the release dates.
func TestListYouTubeChannels_totalsLine(t *testing.T) {
	chans := []Channel{{ID: "UCs", Name: "Some", Subscribed: true, Videos: 9}}
	for name, tc := range map[string]struct {
		totals LibraryTotals
		want   string
	}{
		"full": {LibraryTotals{Videos: 412, Searchable: 380, SearchableSeconds: 212*3600 + 1000, SearchableChannels: 37,
			Oldest: "2019-03-02", Newest: "2026-11-20"},
			"Library: 412 videos, 380 searchable (212 h from 37 channels), released 2019-03-02 to 2026-11-20."},
		"short runtime, one of each": {LibraryTotals{Videos: 1, Searchable: 1, SearchableSeconds: 1790, SearchableChannels: 1,
			Oldest: "2026-01-01", Newest: "2026-01-01"},
			"Library: 1 video, 1 searchable (30 min from 1 channel), released 2026-01-01 to 2026-01-01."},
		"no runtime recorded": {LibraryTotals{Videos: 5, Searchable: 3, SearchableChannels: 2, Oldest: "2024-03-03", Newest: "2026-06-01"},
			"Library: 5 videos, 3 searchable (from 2 channels), released 2024-03-03 to 2026-06-01."},
		"nothing searchable": {LibraryTotals{Videos: 2, Oldest: "2024-03-03", Newest: "2026-06-01"},
			"Library: 2 videos, none searchable yet, released 2024-03-03 to 2026-06-01."},
		"empty": {LibraryTotals{}, "Library: no videos yet."},
	} {
		lib := &fakeLibrary{channels: chans, totals: tc.totals}
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

// Totals are an extra on the list: a failed read drops the line, logged.
func TestListYouTubeChannels_totalsFailureKeepsTheList(t *testing.T) {
	logs := captureLog(t)
	lib := &fakeLibrary{channels: []Channel{{ID: "UCs", Name: "Some", Subscribed: true, Videos: 9}},
		totalsErr: errors.New("library span: sqlite3: interrupted")}
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
// how big it is.
func TestListYouTubeChannels_totalsWithoutChannels(t *testing.T) {
	lib := &fakeLibrary{totals: LibraryTotals{Videos: 2, Oldest: "2024-03-03", Newest: "2026-06-01"}}
	text := toolText(t, callTool(t, lib, "list_youtube_channels", map[string]any{}))
	if want := "Library: 2 videos, none searchable yet, released 2024-03-03 to 2026-06-01.\npeeq has no channels yet."; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}
