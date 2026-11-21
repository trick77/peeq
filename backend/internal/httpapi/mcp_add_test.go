package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/toolkit/mcpserver"
)

// mcpAddFixture is the /mcp handler over the downloads wiring: jobs, videos
// and a runner the enqueue never calls.
func mcpAddFixture(t *testing.T, mutate ...func(*Deps)) (http.Handler, Deps, *sql.DB) {
	t.Helper()
	deps, db := downloadsTestDepsDB(t, &fakeDownloadsRunner{})
	deps.MCPToken = mcpTestToken
	deps.PublicURL = "https://peeq.example.com"
	for _, m := range mutate {
		m(&deps)
	}
	return New(deps), deps, db
}

func addVideosCall(t *testing.T, h http.Handler, urls ...string) (string, []any) {
	t.Helper()
	vs := make([]any, len(urls))
	for i, u := range urls {
		vs[i] = map[string]any{"url": u, "title": "shown on the card only"}
	}
	r := callMCPTool(t, h, "add_videos", map[string]any{"videos": vs})
	text := mcpToolText(t, r)
	if r.Result["isError"] == true {
		t.Fatalf("isError: %s", text)
	}
	sc, _ := r.Result["structuredContent"].(map[string]any)
	if link, _ := sc["link"].(map[string]any); link["url"] != "https://peeq.example.com/up-next" {
		t.Errorf("link = %v", sc["link"])
	}
	items, _ := sc["items"].([]any)
	return text, items
}

func jobCount(t *testing.T, db *sql.DB, videoID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM download_jobs WHERE video_id = ?`, videoID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func item(status, message string) map[string]any {
	return map[string]any{"status": status, "message": message}
}

func TestMCP_addVideosQueuesThenSkips(t *testing.T) {
	h, deps, db := mcpAddFixture(t)

	text, items := addVideosCall(t, h,
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PLnoise",
		"https://www.youtube.com/playlist?list=PL123",
		"https://youtube.com/shorts/aaaaaaaaaaa",
	)
	want := "queued: https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PLnoise\n" +
		"rejected: https://www.youtube.com/playlist?list=PL123: Paste a single video link, not a playlist\n" +
		"queued: https://youtube.com/shorts/aaaaaaaaaaa"
	if text != want {
		t.Errorf("text:\n%s\nwant:\n%s", text, want)
	}
	if !reflect.DeepEqual(items, []any{
		item("done", "queued"),
		item("failed", "Paste a single video link, not a playlist"),
		item("done", "queued"),
	}) {
		t.Errorf("items = %v", items)
	}
	for _, id := range []string{"dQw4w9WgXcQ", "aaaaaaaaaaa"} {
		v, err := deps.Videos.GetCtx(context.Background(), id)
		if err != nil || v == nil || v.Status != videos.StatusQueued {
			t.Fatalf("%s: video %+v err %v, want queued", id, v, err)
		}
		if n := jobCount(t, db, id); n != 1 {
			t.Errorf("%s: %d jobs, want 1", id, n)
		}
	}

	// A client retrying the call (another link form of the same video) must
	// not queue it a second time.
	text, items = addVideosCall(t, h, "https://youtu.be/dQw4w9WgXcQ")
	if text != "already queued or downloaded: https://youtu.be/dQw4w9WgXcQ" {
		t.Errorf("retry text = %q", text)
	}
	if !reflect.DeepEqual(items, []any{item("skipped", "already queued or downloaded")}) {
		t.Errorf("retry items = %v", items)
	}
	if n := jobCount(t, db, "dQw4w9WgXcQ"); n != 1 {
		t.Errorf("retry queued again: %d jobs", n)
	}
}

// The Add view refuses a bare id, and so does add_videos: an 11-character
// word ("downloading") is not a video.
func TestMCP_addVideosRefusesABareID(t *testing.T) {
	h, _, db := mcpAddFixture(t)
	_, items := addVideosCall(t, h, "downloading")
	got, _ := items[0].(map[string]any)
	if msg, _ := got["message"].(string); got["status"] != "failed" || !strings.HasPrefix(msg, "invalid url:") {
		t.Errorf("items = %v", items)
	}
	if n := jobCount(t, db, "downloading"); n != 0 {
		t.Errorf("%d jobs for a word", n)
	}
}

// A canceled pending job leaves the video 'queued' with nothing behind it:
// adding it again queues it, it is not "already" forever.
func TestMCP_addVideosRequeuesACanceledJob(t *testing.T) {
	h, deps, db := mcpAddFixture(t)
	addVideosCall(t, h, "https://youtu.be/ddddddddddd")
	ids, err := deps.Jobs.ActiveIDsForVideos([]string{"ddddddddddd"})
	if err != nil || len(ids) != 1 {
		t.Fatalf("active jobs %v err %v", ids, err)
	}
	if _, err := deps.Jobs.Cancel(ids[0]); err != nil {
		t.Fatal(err)
	}
	_, items := addVideosCall(t, h, "https://youtu.be/ddddddddddd")
	if !reflect.DeepEqual(items, []any{item("done", "queued")}) {
		t.Errorf("items = %v", items)
	}
	if n := jobCount(t, db, "ddddddddddd"); n != 2 {
		t.Errorf("%d jobs, want the canceled one and a new one", n)
	}
}

// A downloaded video has no active job and is still left alone.
func TestMCP_addVideosSkipsADownloadedVideo(t *testing.T) {
	h, deps, db := mcpAddFixture(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "eeeeeeeeeee", URL: "u", Title: "On disk"}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE videos SET status = ? WHERE id = ?`, videos.StatusDownloaded, "eeeeeeeeeee")
	_, items := addVideosCall(t, h, "https://youtu.be/eeeeeeeeeee")
	if !reflect.DeepEqual(items, []any{item("skipped", "already queued or downloaded")}) {
		t.Errorf("items = %v", items)
	}
	if n := jobCount(t, db, "eeeeeeeeeee"); n != 0 {
		t.Errorf("%d jobs, want none", n)
	}
}

// Overlapping calls (a client retrying before the first answer came back)
// queue a video once.
func TestMCP_addVideosConcurrentCallsQueueOnce(t *testing.T) {
	h, _, db := mcpAddFixture(t)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = rawAddVideos(h, "https://youtu.be/fffffffffff")
		}()
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("call %d: status %d", i, c)
		}
	}
	if n := jobCount(t, db, "fffffffffff"); n != 1 {
		t.Errorf("%d jobs, want 1", n)
	}
}

// rawAddVideos is one add_videos call without *testing.T, for goroutines.
func rawAddVideos(h http.Handler, url string) int {
	params := map[string]any{
		"name":      "add_videos",
		"arguments": map[string]any{"videos": []any{map[string]any{"url": url}}},
		"_meta":     map[string]any{"io.modelcontextprotocol/protocolVersion": mcpserver.ModernProtocolVersion},
	}
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params})
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(raw))
	req.Header.Set("MCP-Protocol-Version", mcpserver.ModernProtocolVersion)
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("Mcp-Name", "add_videos")
	req.Header.Set("Authorization", "Bearer "+mcpTestToken)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

// A video peeq knows but does not hold (an inbox read, a failed download)
// is queued, as the Add view would.
func TestMCP_addVideosRequeuesAVideoNotInThePipeline(t *testing.T) {
	h, deps, db := mcpAddFixture(t)
	if err := deps.Videos.Upsert(videos.Video{ID: "bbbbbbbbbbb", URL: "u", Title: "Read from the inbox"}); err != nil {
		t.Fatal(err)
	}
	_, items := addVideosCall(t, h, "https://youtu.be/bbbbbbbbbbb")
	if !reflect.DeepEqual(items, []any{item("done", "queued")}) {
		t.Errorf("items = %v", items)
	}
	if n := jobCount(t, db, "bbbbbbbbbbb"); n != 1 {
		t.Errorf("%d jobs, want 1", n)
	}
	v, _ := deps.Videos.GetCtx(context.Background(), "bbbbbbbbbbb")
	if v == nil || v.Title != "Read from the inbox" {
		t.Errorf("metadata lost: %+v", v)
	}
}

// A server-side failure never leaks its cause: one generic line per video.
func TestMCP_addVideosServerFailureIsGeneric(t *testing.T) {
	t.Run("store", func(t *testing.T) {
		h, _, db := mcpAddFixture(t)
		mustExec(t, db, `DROP TABLE download_jobs`)
		_, items := addVideosCall(t, h, "https://youtu.be/ccccccccccc")
		if !reflect.DeepEqual(items, []any{item("failed", "could not queue; try again")}) {
			t.Errorf("items = %v", items)
		}
	})
}

// No download wiring is permanent, not worth a retry: its own message.
func TestMCP_addVideosWithoutDownloadsConfigured(t *testing.T) {
	h, _, _ := mcpAddFixture(t, func(d *Deps) { d.Jobs = nil })
	_, items := addVideosCall(t, h, "https://youtu.be/ccccccccccc")
	if !reflect.DeepEqual(items, []any{item("failed", "downloads are not configured")}) {
		t.Errorf("items = %v", items)
	}
}

// A rejection's text reaches the client: a URL's query string never does.
func TestMCP_addVideosRedactsTheRejection(t *testing.T) {
	h, _, _ := mcpAddFixture(t)
	// The bad escape fails the parse, whose error quotes the whole URL.
	_, items := addVideosCall(t, h, "https://www.youtube.com/wa%zztch?token=secret123")
	got, _ := items[0].(map[string]any)
	if msg, _ := got["message"].(string); got["status"] != "failed" || !strings.HasPrefix(msg, "invalid url:") ||
		strings.Contains(msg, "secret123") {
		t.Errorf("items = %v", items)
	}
}
