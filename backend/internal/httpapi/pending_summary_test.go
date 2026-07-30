package httpapi

import (
	"net/http"
	"net/http/httptest"
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

// putJSON is putJSONWithCookie with a freshly-minted session, mirroring the
// postJSON helper these tests already use.
func putJSON(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return putJSONWithCookie(t, h, loginAndGetCookie(t, h), path, body)
}

// TestChannelPut_autoSummary covers the per-channel opt-out over HTTP. The
// interesting case is the last one: auto_summary lives on the channel, not the
// subscription, so a request carrying only it must not be rejected for a
// channel that is merely added.
func TestChannelPut_autoSummary(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")

	rr := putJSON(t, h, "/api/channels/UC1", map[string]any{"auto_summary": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"auto_summary":false`) {
		t.Fatalf("body = %s, want the stored value echoed back", rr.Body.String())
	}

	c, err := h.channels.Get("UC1")
	if err != nil || c == nil {
		t.Fatalf("get channel: %v", err)
	}
	if c.AutoSummary {
		t.Fatal("auto_summary was not persisted")
	}

	// And back on again, so the toggle is not one-way.
	if rr := putJSON(t, h, "/api/channels/UC1", map[string]any{"auto_summary": true}); rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if c, _ := h.channels.Get("UC1"); c == nil || !c.AutoSummary {
		t.Fatal("auto_summary did not go back on")
	}
}

func TestChannelPut_autoSummary_unknownChannel404s(t *testing.T) {
	h := newPendingTestServer(t)
	rr := putJSON(t, h, "/api/channels/UCnope", map[string]any{"auto_summary": false})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// The two subscription fields keep their old rejection: they genuinely need a
// subscription row, and widening that would silently no-op them.
func TestChannelPut_subscriptionFieldsStillNeedASubscription(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	rr := putJSON(t, h, "/api/channels/UC1", map[string]any{"autodownload": true})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// A video peeq never got round to reading has a ledger row and no videos row.
// Ignoring it must be an ordinary success, not a 500 on a missing row.
func TestPendingIgnore_unreadVideoIsFine(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if err := h.ledger.Insert(channelvideos.Entry{
		VideoID: "p3", ChannelID: "UC1", Title: "Never read",
		URL: "https://www.youtube.com/watch?v=p3", DurationSeconds: 600, State: "pending",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if rr := postJSON(t, h, "/api/pending/p3/ignore", nil); rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	e, err := h.ledger.Get("p3")
	if err != nil || e == nil || e.State != channelvideos.StateIgnored {
		t.Fatalf("ledger row = %+v (err %v), want ignored", e, err)
	}
}

// A video whose captions never arrived has a row and no .vtt. Keyed on the
// subtitle path alone that row would survive every ignore forever, invisible to
// every list and reachable by nothing.
func TestPendingIgnore_collectsARowWithNoCaptions(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if err := h.ledger.Insert(channelvideos.Entry{
		VideoID: "p4", ChannelID: "UC1", Title: "No captions ever",
		URL: "https://www.youtube.com/watch?v=p4", DurationSeconds: 600, State: "pending",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := h.videos.Upsert(videos.Video{ID: "p4", URL: "https://youtu.be/p4"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := h.videos.SetSummaryStatus("p4", videos.SummaryNoTranscript, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}

	if rr := postJSON(t, h, "/api/pending/p4/ignore", nil); rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if v, err := h.videos.Get("p4"); err != nil || v != nil {
		t.Fatalf("row survived: %+v (err %v)", v, err)
	}
}
