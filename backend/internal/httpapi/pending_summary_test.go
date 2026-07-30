package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// seedInboxRead puts a video in the state the caption fetcher leaves behind: a
// pending ledger row, a videos row at StatusNew, a summary, and a .vtt under
// .summaries/. Returns the absolute path of that caption file.
func seedInboxRead(t *testing.T, h *pendingTestHarness, id string) string {
	t.Helper()
	if err := h.ledger.Insert(channelvideos.Entry{
		VideoID: id, ChannelID: "UC1", Title: "A video",
		URL: "https://www.youtube.com/watch?v=" + id, DurationSeconds: 600, State: "pending",
	}); err != nil {
		t.Fatalf("insert ledger row: %v", err)
	}
	rel := filepath.Join(ytdlp.SummaryDirName, id, id+".en.vtt")
	full := filepath.Join(h.mediaDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatalf("write vtt: %v", err)
	}
	if err := h.videos.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := h.videos.SetSubtitle(id, rel, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	if err := h.videos.SetSummaryText(id, "A summary."); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	if err := h.videos.SetStatus(id, videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	return full
}

// TestPendingIgnore_throwsAwayTheRead is the user's explicit choice made
// literal: ignoring a video means it is gone, not archived. The row that held
// the summary and the caption file that produced it both go, and the ledger row
// stays 'ignored' so the video is never read a second time.
func TestPendingIgnore_throwsAwayTheRead(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	vtt := seedInboxRead(t, h, "p1")

	if rr := postJSON(t, h, "/api/pending/p1/ignore", nil); rr.Code != http.StatusOK {
		t.Fatalf("ignore status = %d", rr.Code)
	}

	if v, err := h.videos.Get("p1"); err != nil || v != nil {
		t.Fatalf("videos row survived the ignore: %+v (err %v)", v, err)
	}
	if _, err := os.Stat(vtt); !os.IsNotExist(err) {
		t.Fatalf("caption file still on disk: stat err = %v, want not-exist", err)
	}
	e, err := h.ledger.Get("p1")
	if err != nil || e == nil {
		t.Fatalf("ledger row must survive: %+v (err %v)", e, err)
	}
	if e.State != channelvideos.StateIgnored {
		t.Fatalf("ledger state = %q, want ignored — it is what stops a re-read", e.State)
	}
}

// TestPendingIgnore_sparesADownloadedVideo is the guard that makes the deletion
// above safe. StatusNew is the videos.status column DEFAULT and the state a
// CANCELLED download is returned to, so ignoring on status alone would let this
// endpoint destroy a real video's row and its transcript.
func TestPendingIgnore_sparesADownloadedVideo(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if err := h.ledger.Insert(channelvideos.Entry{
		VideoID: "p2", ChannelID: "UC1", Title: "A video",
		URL: "https://www.youtube.com/watch?v=p2", DurationSeconds: 600, State: "pending",
	}); err != nil {
		t.Fatalf("insert ledger row: %v", err)
	}
	// A transcript that came from a real download, and a status a cancel left.
	rel := filepath.Join("UC1", "p2", "p2.en.vtt")
	if err := h.videos.Upsert(videos.Video{ID: "p2", URL: "https://youtu.be/p2"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := h.videos.SetSubtitle("p2", rel, "en"); err != nil {
		t.Fatalf("set subtitle: %v", err)
	}
	if err := h.videos.SetStatus("p2", videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}

	if rr := postJSON(t, h, "/api/pending/p2/ignore", nil); rr.Code != http.StatusOK {
		t.Fatalf("ignore status = %d", rr.Code)
	}

	v, err := h.videos.Get("p2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v == nil {
		t.Fatal("ignoring destroyed a video whose transcript came from a download")
	}
	if v.SubtitlePath != rel {
		t.Fatalf("subtitle_path = %q, want it untouched", v.SubtitlePath)
	}
}

// TestPendingList_carriesSummaryState pins what the Inbox card reads. Both
// fields are needed and neither is enough alone: an empty summary_status means
// "not read yet" on an opted-in channel and "never will be" on an opted-out
// one, and those are different cards.
func TestPendingList_carriesSummaryState(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	seedInboxRead(t, h, "p1")
	if err := h.videos.SetSummaryStatus("p1", videos.SummaryDone, ""); err != nil {
		t.Fatalf("set summary status: %v", err)
	}

	body := getJSON(t, h, "/api/pending")
	if !strings.Contains(body, `"summary_status":"done"`) {
		t.Fatalf("pending list is missing the summary status: %s", body)
	}
	if !strings.Contains(body, `"auto_summary":true`) {
		t.Fatalf("pending list is missing the channel opt-in: %s", body)
	}
}
