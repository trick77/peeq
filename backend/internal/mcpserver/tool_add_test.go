package mcpserver

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func videoArgsOf(urls ...string) map[string]any {
	vs := make([]any, len(urls))
	for i, u := range urls {
		vs[i] = map[string]any{"url": u}
	}
	return map[string]any{"videos": vs}
}

func structuredOf(t *testing.T, r rpcReply) map[string]any {
	t.Helper()
	sc, ok := r.body.Result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("no structuredContent: %v", r.body.Result)
	}
	return sc
}

func TestAddVideos_reportsEachVideoInOrder(t *testing.T) {
	lib := &fakeLibrary{added: map[string]AddedVideo{
		"https://youtu.be/bbbbbbbbbbb": {VideoID: "bbbbbbbbbbb", Outcome: AddAlready},
		"https://www.youtube.com/playlist?list=PL1": {Outcome: AddRejected,
			Message: "Paste a single video link, not a playlist"},
	}}
	r := callTool(t, lib, "add_videos", map[string]any{"videos": []any{
		map[string]any{"url": "https://youtu.be/aaaaaaaaaaa", "title": "First"},
		map[string]any{"url": "https://youtu.be/bbbbbbbbbbb"},
		map[string]any{"url": "https://www.youtube.com/playlist?list=PL1", "title": "A playlist"},
	}})
	if isError(r) {
		t.Fatalf("isError: %s", toolText(t, r))
	}
	want := "queued: https://youtu.be/aaaaaaaaaaa\n" +
		"already queued or downloaded: https://youtu.be/bbbbbbbbbbb\n" +
		"rejected: https://www.youtube.com/playlist?list=PL1: Paste a single video link, not a playlist"
	if got := toolText(t, r); got != want {
		t.Errorf("text:\n%s\nwant:\n%s", got, want)
	}

	sc := structuredOf(t, r)
	wantItems := []any{
		map[string]any{"status": "done", "message": "queued"},
		map[string]any{"status": "skipped", "message": "already queued or downloaded"},
		map[string]any{"status": "failed", "message": "Paste a single video link, not a playlist"},
	}
	if !reflect.DeepEqual(sc["items"], wantItems) {
		t.Errorf("items = %v", sc["items"])
	}
	wantLink := map[string]any{"url": "https://peeq.example.com/up-next", "label": "peeq"}
	if !reflect.DeepEqual(sc["link"], wantLink) {
		t.Errorf("link = %v", sc["link"])
	}
	if _, ok := sc["sources"]; ok {
		t.Errorf("add_videos cites nothing, sources = %v", sc["sources"])
	}
}

// No bare-id shortcut: any 11-character word would pass as an id. The url
// reaches the library as sent (trimmed), which refuses a non-URL as the Add
// view does.
func TestAddVideos_passesTheURLAsSent(t *testing.T) {
	lib := &fakeLibrary{}
	callTool(t, lib, "add_videos", videoArgsOf(" downloading ", " https://youtu.be/dQw4w9WgXcQ"))
	if got := strings.Join(lib.addedURLs, ","); got != "downloading,https://youtu.be/dQw4w9WgXcQ" {
		t.Errorf("library got %q", got)
	}
}

// A long reason (yt-dlp's parse error echoes the url) is one capped line.
func TestAddVideos_capsTheReason(t *testing.T) {
	lib := &fakeLibrary{added: map[string]AddedVideo{
		"https://example.com/x": {Outcome: AddRejected, Message: "invalid url:\n" + strings.Repeat("x", 1000)},
	}}
	r := callTool(t, lib, "add_videos", videoArgsOf("https://example.com/x"))
	items, _ := structuredOf(t, r)["items"].([]any)
	msg, _ := items[0].(map[string]any)["message"].(string)
	if len([]rune(msg)) > addReasonRunes+1 || strings.Contains(msg, "\n") {
		t.Errorf("reason not capped to one line: %d runes", len([]rune(msg)))
	}
	if strings.Count(toolText(t, r), "\n") != 0 {
		t.Errorf("text spans lines: %q", toolText(t, r))
	}
}

func TestAddVideos_refusesBadArguments(t *testing.T) {
	tooMany := make([]string, maxAddVideos+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("https://youtu.be/vid%08d", i)
	}
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"no videos": {map[string]any{"videos": []any{}}, "at least one video"},
		"missing":   {map[string]any{}, "at least one video"},
		"too many":  {videoArgsOf(tooMany...), "at most 10 videos"},
		"blank url": {videoArgsOf("https://youtu.be/aaaaaaaaaaa", "  "), "videos[1].url is required"},
		"duplicate": {videoArgsOf("https://youtu.be/aaaaaaaaaaa", "https://youtu.be/aaaaaaaaaaa"), "videos[1] repeats videos[0]"},
		"dup by link form": {videoArgsOf("https://youtu.be/aaaaaaaaaaa", "https://example.com/x",
			"https://www.youtube.com/watch?v=aaaaaaaaaaa&t=5"), "videos[2] repeats videos[0]"},
		"dup shorts form": {videoArgsOf("https://youtube.com/shorts/aaaaaaaaaaa", "https://youtu.be/aaaaaaaaaaa"),
			"videos[1] repeats videos[0]"},
		"dup non-url":    {videoArgsOf("not a url", "not a url"), "videos[1] repeats videos[0]"},
		"long url":       {videoArgsOf("https://youtu.be/" + strings.Repeat("a", maxAddURLBytes)), "videos[0].url is too long"},
		"newline in url": {videoArgsOf("https://youtu.be/aaaaaaaaaaa\nqueued: x"), "videos[0].url contains control characters"},
		"unknown field":  {map[string]any{"videos": []any{map[string]any{"url": "https://youtu.be/aaaaaaaaaaa", "x": 1}}}, "invalid arguments"},
	} {
		t.Run(name, func(t *testing.T) {
			lib := &fakeLibrary{}
			r := callTool(t, lib, "add_videos", tc.args)
			if !isError(r) || !strings.Contains(toolText(t, r), tc.want) {
				t.Errorf("isError %v, text %q, want %q", isError(r), toolText(t, r), tc.want)
			}
			if lib.addedN != 0 {
				t.Errorf("library called on a refused call")
			}
		})
	}
}

// items[i] is the i-th video: a library answering fewer outcomes than urls
// would shift every status onto the wrong card row.
func TestAddVideos_libraryOutcomeCountMismatchIsAnError(t *testing.T) {
	lib := &fakeLibrary{addShort: true}
	r := callTool(t, lib, "add_videos", videoArgsOf("https://youtu.be/aaaaaaaaaaa", "https://youtu.be/bbbbbbbbbbb"))
	if !isError(r) {
		t.Errorf("want isError, text %q", toolText(t, r))
	}
}

func TestAddVideosSchema(t *testing.T) {
	s := addVideosSchema()
	if req, _ := s["required"].([]string); strings.Join(req, ",") != "videos" {
		t.Errorf("required = %v", s["required"])
	}
	props, _ := s["properties"].(map[string]any)
	videos, _ := props["videos"].(map[string]any)
	if videos["type"] != "array" || videos["minItems"] != 1 || videos["maxItems"] != maxAddVideos {
		t.Errorf("videos = %v", videos)
	}
	item, _ := videos["items"].(map[string]any)
	itemProps, _ := item["properties"].(map[string]any)
	if _, ok := itemProps["title"]; !ok {
		t.Errorf("item lacks title: %v", item)
	}
	if req, _ := item["required"].([]string); strings.Join(req, ",") != "url" {
		t.Errorf("item required = %v", item["required"])
	}
}
