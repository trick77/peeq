package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

func listFixture() *fakeLibrary {
	return &fakeLibrary{
		scopes: map[string]ChannelScope{
			"veritasium": {IDs: []string{"UCV"}, Names: []string{"Veritasium"}, Matched: []string{"Veritasium"}},
			"fresh":      {IDs: []string{"UCF"}, Matched: []string{"Fresh Channel"}, NoVideos: true},
		},
		list: VideoList{Total: 3, Videos: []ListedVideo{
			{ID: "abc123", Title: "Why bikes stay upright", Channel: "Veritasium", PublishedAt: "2026-05-02",
				DurationSeconds: 754, Status: videos.StatusDownloaded, SummaryStatus: videos.SummaryDone,
				HasTranscript: true, Searchable: true},
			{ID: "inbox01", Title: "Inbox read", Channel: "Veritasium", PublishedAt: "2026-04-01",
				DurationSeconds: 3725, Status: videos.StatusNew, SummaryStatus: videos.SummaryPending},
			{ID: "nodate1", Title: "", Channel: "", Status: videos.StatusQueued, SummaryStatus: videos.SummaryPending,
				HasTranscript: true},
		}},
	}
}

func TestListVideos_rendersEveryVideoWithItsState(t *testing.T) {
	lib := listFixture()
	r := callTool(t, lib, "list_videos", map[string]any{})
	if isError(r) {
		t.Fatalf("error: %s", toolText(t, r))
	}
	text := toolText(t, r)
	for _, want := range []string{
		"Videos 1–3 of 3 · sorted by release date, newest first\n",
		"- Why bikes stay upright — Veritasium, 2026-05-02, 12:34\n   id: abc123\n   peeq: https://peeq.example.com/video/abc123\n   searchable\n",
		"- Inbox read — Veritasium, 2026-04-01, 1:02:05\n   id: inbox01\n   peeq: https://peeq.example.com/video/inbox01\n   not searchable: no transcript stored\n",
		"- nodate1\n   id: nodate1\n   peeq: https://peeq.example.com/video/nodate1\n   not searchable: not analysed yet",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q\n---\n%s", want, text)
		}
	}
	if strings.Contains(text, "offset=") {
		t.Errorf("a final page offers a continuation:\n%s", text)
	}
	// Text only: the sources contract (sources[i] = i-th item) is search's.
	if sc := r.body.Result["structuredContent"]; sc != nil {
		t.Errorf("structuredContent = %v, want none", sc)
	}
	if q := lib.lastList; q == nil || q.Sort != "newest" || q.Limit != defaultListVideos || q.Offset != 0 || q.Channel.Resolved() {
		t.Errorf("query = %+v", lib.lastList)
	}
}

func TestListVideos_passesFiltersAndEchoesThem(t *testing.T) {
	lib := listFixture()
	r := callTool(t, lib, "list_videos", map[string]any{"channel": "Veritasium", "title": "  bikes ",
		"after": "2025-01-01", "before": "2026-12-31", "sort": "added_newest", "limit": 3, "offset": 0})
	text := toolText(t, r)
	if !strings.HasPrefix(text, `Videos 1–3 of 3 · channel: Veritasium · title contains "bikes" · released 2025-01-01 to 2026-12-31 · sorted by date added, newest first`+"\n") {
		t.Errorf("header:\n%s", text)
	}
	q := lib.lastList
	if q == nil || q.Title != "bikes" || q.After != "2025-01-01" || q.Before != "2026-12-31" || q.Sort != "added_newest" ||
		q.Limit != 3 || !slices.Equal(q.Channel.IDs, []string{"UCV"}) || !slices.Equal(q.Channel.Names, []string{"Veritasium"}) {
		t.Errorf("query = %+v", q)
	}
	for args, want := range map[string]string{
		"after":  "released 2025-01-01 or later",
		"before": "released 2026-12-31 or earlier",
	} {
		val := map[string]string{"after": "2025-01-01", "before": "2026-12-31"}[args]
		if text := toolText(t, callTool(t, lib, "list_videos", map[string]any{args: val})); !strings.Contains(text, want) {
			t.Errorf("%s: header lacks %q:\n%s", args, want, text)
		}
	}
	for sort, want := range map[string]string{"oldest": "sorted by release date, oldest first", "title": "sorted by title"} {
		if text := toolText(t, callTool(t, lib, "list_videos", map[string]any{"sort": sort})); !strings.Contains(text, want) {
			t.Errorf("%s: header lacks %q:\n%s", sort, want, text)
		}
	}
}

func TestListVideos_validatesArguments(t *testing.T) {
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"sort":         {map[string]any{"sort": "best"}, `sort must be one of newest, oldest, added_newest, title, got "best"`},
		"after":        {map[string]any{"after": "May 2026"}, `after must be a date as YYYY-MM-DD, got "May 2026"`},
		"before":       {map[string]any{"before": "2026-13-01"}, `before must be a date as YYYY-MM-DD, got "2026-13-01"`},
		"offset":       {map[string]any{"offset": -1}, "offset must be 0 or more"},
		"unknown":      {map[string]any{"query": "x"}, "invalid arguments"},
		"channel miss": {map[string]any{"channel": "nobody"}, `No channel in peeq matches "nobody". Closest: Veritasium, Vsauce. Call list_youtube_channels for every channel, or call list_videos without a channel.`},
	} {
		lib := listFixture()
		r := callTool(t, lib, "list_videos", tc.args)
		if !isError(r) || !strings.HasPrefix(toolText(t, r), tc.want) {
			t.Errorf("%s: isError=%v %q, want %q", name, isError(r), toolText(t, r), tc.want)
		}
		if lib.lastList != nil {
			t.Errorf("%s: listed despite a refused argument", name)
		}
	}
}

func TestListVideos_limitIsClamped(t *testing.T) {
	for limit, want := range map[int]int{0: 1, -3: 1, 7: 7, 500: videos.BrowseMaxLimit} {
		lib := listFixture()
		callTool(t, lib, "list_videos", map[string]any{"limit": limit})
		if lib.lastList == nil || lib.lastList.Limit != want {
			t.Errorf("limit %d: query = %+v, want %d", limit, lib.lastList, want)
		}
	}
}

func TestListVideos_channelWithoutVideos(t *testing.T) {
	lib := listFixture()
	r := callTool(t, lib, "list_videos", map[string]any{"channel": "fresh"})
	if isError(r) || toolText(t, r) != "No videos yet in Fresh Channel: peeq follows the channel but holds none of its videos so far." {
		t.Errorf("isError=%v %q", isError(r), toolText(t, r))
	}
	if sc := r.body.Result["structuredContent"]; sc != nil {
		t.Errorf("structuredContent = %v, want none", sc)
	}
	if lib.lastList != nil {
		t.Error("listed a channel known to hold no videos")
	}
}

func TestListVideos_failures(t *testing.T) {
	lib := listFixture()
	lib.resolveErr = errors.New("db down")
	r := callTool(t, lib, "list_videos", map[string]any{"channel": "veritasium"})
	if !isError(r) || toolText(t, r) != "list_videos failed: the channel list could not be read" {
		t.Errorf("resolve failure: %v %q", isError(r), toolText(t, r))
	}
	lib = listFixture()
	lib.listErr = errors.New("db down")
	r = callTool(t, lib, "list_videos", map[string]any{})
	if !isError(r) || toolText(t, r) != "listing videos failed; try again" {
		t.Errorf("list failure: %v %q", isError(r), toolText(t, r))
	}
	lib.listErr = fmt.Errorf("browse: %w", context.DeadlineExceeded)
	r = callTool(t, lib, "list_videos", map[string]any{})
	if !isError(r) || toolText(t, r) != "listing videos failed (timed out); try again" {
		t.Errorf("timeout: %v %q", isError(r), toolText(t, r))
	}
}

func TestListVideos_emptyAndPastTheEnd(t *testing.T) {
	lib := &fakeLibrary{}
	r := callTool(t, lib, "list_videos", map[string]any{})
	if isError(r) || toolText(t, r) != "peeq holds no videos yet." {
		t.Errorf("empty library: %v %q", isError(r), toolText(t, r))
	}
	r = callTool(t, lib, "list_videos", map[string]any{"title": "zebra"})
	if text := toolText(t, r); isError(r) || text != `No videos in peeq match (title contains "zebra"). Try again without some of the filters.` {
		t.Errorf("no match: %v %q", isError(r), text)
	}
	if sc := r.body.Result["structuredContent"]; sc != nil {
		t.Errorf("structuredContent = %v, want none", sc)
	}
	lib.list = VideoList{Total: 9}
	r = callTool(t, lib, "list_videos", map[string]any{"offset": 40})
	if text := toolText(t, r); isError(r) || text != "offset 40 is past the end: 9 videos match. Call list_videos with an offset below 9." {
		t.Errorf("past the end: %v %q", isError(r), text)
	}
}

// A page the limit cut ends with where to continue; the last page does not.
func TestListVideos_limitBoundPageContinues(t *testing.T) {
	lib := &fakeLibrary{list: VideoList{Total: 30}}
	for i := range 25 {
		lib.list.Videos = append(lib.list.Videos, ListedVideo{ID: fmt.Sprintf("vid%02d", i), Title: "t", Searchable: true})
	}
	text := toolText(t, callTool(t, lib, "list_videos", map[string]any{}))
	if !strings.HasPrefix(text, "Videos 1–25 of 30 ") ||
		!strings.HasSuffix(text, "\n[5 more videos; keep the other arguments and call list_videos again with offset=25 to continue]") {
		t.Errorf("text:\n%s", text)
	}
	lib.list.Videos = lib.list.Videos[:5]
	text = toolText(t, callTool(t, lib, "list_videos", map[string]any{"offset": 25}))
	if !strings.HasPrefix(text, "Videos 26–30 of 30 ") || strings.Contains(text, "offset=") {
		t.Errorf("last page:\n%s", text)
	}
}

// Past the size cap whole items are dropped, the range says what is shown
// and the continuation starts right after it.
func TestListVideos_sizeCapDropsWholeItems(t *testing.T) {
	lib := &fakeLibrary{list: VideoList{Total: 120}}
	for i := range videos.BrowseMaxLimit {
		lib.list.Videos = append(lib.list.Videos, ListedVideo{ID: fmt.Sprintf("vid%02d", i),
			Title: fmt.Sprintf("Video %d %s", i, strings.Repeat("é", 400)), Channel: strings.Repeat("ö", 400),
			PublishedAt: "2026-01-01", Searchable: i%2 == 0})
	}
	text := toolText(t, callTool(t, lib, "list_videos", map[string]any{"offset": 10, "limit": 50}))
	if len(text) > maxToolTextBytes || strings.Contains(text, "[output truncated]") {
		t.Fatalf("text is %d bytes, or cut by the generic cap", len(text))
	}
	shown := len(itemHeaders(text))
	if shown == 0 || shown == videos.BrowseMaxLimit {
		t.Fatalf("%d items shown, want some but not all", shown)
	}
	ids := strings.Count(text, "\n   id: ")
	states := strings.Count(text, "\n   searchable\n") + strings.Count(text, "\n   not searchable: ")
	if ids != shown || states != shown {
		t.Errorf("%d items, %d id lines, %d state lines: an item was cut", shown, ids, states)
	}
	if want := fmt.Sprintf("Videos 11–%d of 120 ", 10+shown); !strings.HasPrefix(text, want) {
		t.Errorf("range: want prefix %q, got %q", want, text[:60])
	}
	if want := fmt.Sprintf("[%d more videos; keep the other arguments and call list_videos again with offset=%d to continue]", 120-10-shown, 10+shown); !strings.HasSuffix(text, want) {
		t.Errorf("continuation: want %q, got %q", want, text[len(text)-160:])
	}
}

// The reason a video is out of search's reach is said in plain words, never
// as a raw status: "queued", "new" or "tombstoned" mean nothing to a client.
func TestListVideos_notSearchableReasons(t *testing.T) {
	for _, tc := range []struct {
		v    ListedVideo
		want string
	}{
		{ListedVideo{Status: videos.StatusNew, SummaryStatus: videos.SummaryPending}, "no transcript stored"},
		{ListedVideo{Status: videos.StatusError, SummaryStatus: videos.SummaryPending}, "download failed, no transcript stored"},
		{ListedVideo{Status: videos.StatusNew, SummaryStatus: videos.SummaryNoTranscript}, "no captions available"},
		{ListedVideo{Status: videos.StatusQueued, SummaryStatus: videos.SummaryRunning, HasTranscript: true}, "not analysed yet"},
		{ListedVideo{Status: videos.StatusTombstoned, SummaryStatus: videos.SummaryError, HasTranscript: true}, "analysis failed"},
		{ListedVideo{Status: videos.StatusDownloaded, SummaryStatus: videos.SummaryNoTranscript, HasTranscript: true}, "no speech in the transcript"},
		{ListedVideo{Status: videos.StatusDownloaded, SummaryStatus: videos.SummaryDone, HasTranscript: true}, "not indexed yet"},
	} {
		if got := notSearchableReason(tc.v); got != tc.want {
			t.Errorf("%+v = %q, want %q", tc.v, got, tc.want)
		}
	}
	raw := append(slices.Clone(videos.Statuses), videos.SummaryStatuses...)
	for _, st := range videos.Statuses {
		for _, ss := range videos.SummaryStatuses {
			for _, tr := range []bool{false, true} {
				got := notSearchableReason(ListedVideo{Status: st, SummaryStatus: ss, HasTranscript: tr})
				for _, w := range strings.FieldsFunc(got, func(r rune) bool { return r == ' ' || r == ',' }) {
					if slices.Contains(raw, w) || strings.Contains(w, "_") {
						t.Errorf("%s/%s/%v: reason %q carries the raw value %q", st, ss, tr, got, w)
					}
				}
			}
		}
	}
}

func TestListVideosSchemaAndDescription(t *testing.T) {
	props, _ := listVideosSchema()["properties"].(map[string]any)
	for _, k := range []string{"channel", "title", "after", "before", "sort", "limit", "offset"} {
		if _, ok := props[k]; !ok {
			t.Errorf("schema lacks %s", k)
		}
	}
	if s, _ := props["sort"].(map[string]any); !slices.Equal(s["enum"].([]string), videos.BrowseSorts) {
		t.Errorf("sort enum = %v", s["enum"])
	}
	for _, want := range []string{"instead of search", "channel", "title", "added_newest", "searchable or not"} {
		if !strings.Contains(listVideosDescription, want) {
			t.Errorf("description lacks %q", want)
		}
	}
}
