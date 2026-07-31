package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/videos"
)

// seedInboxRead puts a video in the state the caption fetcher leaves behind: a
// pending ledger row, a videos row at StatusNew, a summary, and a .vtt under
// .summaries/. Returns the absolute path of that caption file.
func seedInboxRead(t *testing.T, h *pendingTestHarness, id string) {
	t.Helper()
	if err := h.ledger.Insert(channelvideos.Entry{
		VideoID: id, ChannelID: "UC1", Title: "A video",
		URL: "https://www.youtube.com/watch?v=" + id, DurationSeconds: 600, State: "pending",
	}); err != nil {
		t.Fatalf("insert ledger row: %v", err)
	}
	if err := h.videos.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// source='caption' is what says this transcript was read to inform the
	// decision the card is still waiting on — the fact the ".summaries/" path
	// prefix used to carry.
	if err := h.videos.SetTranscript(id, videos.TranscriptSourceCaption, "WEBVTT\n"); err != nil {
		t.Fatalf("set transcript: %v", err)
	}
	if err := h.videos.SetSummaryText(id, "A summary."); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	if err := h.videos.SetStatus(id, videos.StatusNew, ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
}

// TestPendingIgnore_throwsAwayTheRead is the user's explicit choice made
// literal: ignoring a video means it is gone, not archived. The row that held
// the summary and the caption file that produced it both go, and the ledger row
// stays 'ignored' so the video is never read a second time.
func TestPendingIgnore_throwsAwayTheRead(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	seedInboxRead(t, h, "p1")

	if rr := postJSON(t, h, "/api/pending/p1/ignore", nil); rr.Code != http.StatusOK {
		t.Fatalf("ignore status = %d", rr.Code)
	}

	if v, err := h.videos.Get("p1"); err != nil || v != nil {
		t.Fatalf("videos row survived the ignore: %+v (err %v)", v, err)
	}
	// The transcript went with the row on the cascade.
	if tr, err := h.videos.GetTranscript("p1"); err != nil || tr != nil {
		t.Fatalf("transcript survived the ignore: %+v (err %v)", tr, err)
	}
	e, err := h.ledger.Get("p1")
	if err != nil || e == nil {
		t.Fatalf("ledger row must survive: %+v (err %v)", e, err)
	}
	if e.State != channelvideos.StateIgnored {
		t.Fatalf("ledger state = %q, want ignored — it is what stops a re-read", e.State)
	}
}

// TestPendingIgnore_keepsAnIndexedRead is the exception the keep_reads setting
// buys. A read that made it into the search index survives being ignored: the
// row, its transcript and its chunks all stay, so the video remains findable in
// Search — the only place it ever appears, since status 'new' is excluded from
// every list.
//
// Its poster stays too. The pending cache holds the only picture such a video
// has, and both the summary page and the search card render one.
func TestPendingIgnore_keepsAnIndexedRead(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	seedInboxRead(t, h, "p3")
	h.seedPendingThumbCache(t, "p3")
	seedChunks(t, h.rag, "p3", []rag.ChunkRow{
		{Ordinal: 0, Text: "the bit worth finding later", Kind: rag.KindTranscript, StartSeconds: 12},
	})

	if rr := postJSON(t, h, "/api/pending/p3/ignore", nil); rr.Code != http.StatusOK {
		t.Fatalf("ignore status = %d", rr.Code)
	}

	if v, err := h.videos.Get("p3"); err != nil || v == nil {
		t.Fatalf("an indexed read was discarded: %+v (err %v)", v, err)
	}
	if tr, err := h.videos.GetTranscript("p3"); err != nil || tr == nil {
		t.Fatalf("the indexed read's transcript went: %+v (err %v)", tr, err)
	}
	if indexed, err := h.rag.HasChunks(context.Background(), "p3"); err != nil || !indexed {
		t.Fatalf("chunks went with the ignore: indexed=%v (err %v)", indexed, err)
	}
	if got, err := h.ledger.GetThumbnail("p3"); err != nil || got == nil {
		t.Fatalf("a kept video lost the only poster it has: %+v (err %v)", got, err)
	}
	// The ledger row still has to move, or the video comes straight back.
	e, err := h.ledger.Get("p3")
	if err != nil || e == nil || e.State != channelvideos.StateIgnored {
		t.Fatalf("ledger state = %+v (err %v), want ignored", e, err)
	}
}

// TestPendingThumbnail_servesAKeptReadsPoster is the half of "the poster stays"
// that keeping the bytes does not buy on its own. The ledger row is 'ignored'
// by then, and this endpoint used to 404 every row that had left the inbox —
// so both the summary page and the search card would have fallen back to the
// gradient placeholder while the picture sat in the database.
func TestPendingThumbnail_servesAKeptReadsPoster(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	seedInboxRead(t, h, "p5")
	h.seedPendingThumbCache(t, "p5")
	seedChunks(t, h.rag, "p5", []rag.ChunkRow{
		{Ordinal: 0, Text: "worth finding later", Kind: rag.KindTranscript},
	})

	if rr := postJSON(t, h, "/api/pending/p5/ignore", nil); rr.Code != http.StatusOK {
		t.Fatalf("ignore status = %d", rr.Code)
	}

	rec := h.getRaw(t, "/api/pending/p5/thumbnail")
	if rec.Code != http.StatusOK {
		t.Fatalf("poster status = %d, want 200 — a kept read's only picture", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("poster response is empty")
	}
}

// TestPendingIgnore_stillDropsAnUnindexedRead is the other half of that rule.
// The decision is made on what the row HOLDS, not on the channel's current
// setting: a read that never made it into the index is discarded exactly as
// before, even on a channel that is opted in today.
func TestPendingIgnore_stillDropsAnUnindexedRead(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	seedInboxRead(t, h, "p4")
	if _, _, err := h.channels.SetKeepReads("UC1", true); err != nil {
		t.Fatalf("set keep_reads: %v", err)
	}

	if rr := postJSON(t, h, "/api/pending/p4/ignore", nil); rr.Code != http.StatusOK {
		t.Fatalf("ignore status = %d", rr.Code)
	}

	if v, err := h.videos.Get("p4"); err != nil || v != nil {
		t.Fatalf("an unindexed read survived: %+v (err %v)", v, err)
	}
}

// TestPendingIgnore_sparesADownloadedVideo is the guard that makes the deletion
// above safe. StatusNew is the videos.status column DEFAULT and the state a
// CANCELLED download is returned to, so ignoring on status alone would let this
// endpoint destroy a real video's row and its transcript. What tells the two
// apart is the transcript's recorded source.
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
	if err := h.videos.Upsert(videos.Video{ID: "p2", URL: "https://youtu.be/p2"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := h.videos.SetTranscript("p2", videos.TranscriptSourceDownload, "WEBVTT\n"); err != nil {
		t.Fatalf("set transcript: %v", err)
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
	if tr, terr := h.videos.GetTranscript("p2"); terr != nil || tr == nil {
		t.Fatalf("the downloaded video's transcript was thrown away: %+v (err %v)", tr, terr)
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
	if !strings.Contains(body, `"has_subtitles":true`) {
		t.Fatalf("pending list is missing the captions flag: %s", body)
	}
}

// TestPendingList_noTranscriptSplitsOnCaptions is the distinction the card
// cannot make without this field. 'no_transcript' is recorded both for a video
// YouTube has no captions for and for one whose captions turned out to be
// music: same status, but the second has a transcript worth opening and the
// first has nothing at all behind the card.
func TestPendingList_noTranscriptSplitsOnCaptions(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")

	// Captions on disk, no summary made of them — the music case.
	seedInboxRead(t, h, "music")
	if err := h.videos.SetSummaryStatus("music", videos.SummaryNoTranscript, ""); err != nil {
		t.Fatalf("set summary status: %v", err)
	}
	// No captions ever fetched.
	if err := h.ledger.Insert(channelvideos.Entry{
		VideoID: "silent", ChannelID: "UC1", Title: "No captions ever",
		URL: "https://www.youtube.com/watch?v=silent", DurationSeconds: 600, State: "pending",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := h.videos.Upsert(videos.Video{ID: "silent", URL: "https://youtu.be/silent"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := h.videos.SetSummaryStatus("silent", videos.SummaryNoTranscript, ""); err != nil {
		t.Fatalf("set summary status: %v", err)
	}

	var got []struct {
		VideoID      string `json:"video_id"`
		HasSubtitles bool   `json:"has_subtitles"`
	}
	if err := json.Unmarshal([]byte(getJSON(t, h, "/api/pending")), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	flags := map[string]bool{}
	for _, it := range got {
		flags[it.VideoID] = it.HasSubtitles
	}
	if !flags["music"] {
		t.Error("a video with captions but no summary reports no transcript to read")
	}
	if flags["silent"] {
		t.Error("a video that never got captions claims to have a transcript")
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

// TestChannelPut_keepReads covers the second channel-level switch. It defaults
// OFF — the reading is paid for either way, but keeping it costs embeddings —
// and, living on the channel like auto_summary, it is settable on a channel
// that is merely added. The last assertion is the one worth having: a request
// that sets only one of the two must not report the other as off.
func TestChannelPut_keepReads(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")

	c, err := h.channels.Get("UC1")
	if err != nil || c == nil {
		t.Fatalf("get channel: %v", err)
	}
	if c.KeepReads {
		t.Fatal("keep_reads must default to off")
	}

	rr := putJSON(t, h, "/api/channels/UC1", map[string]any{"keep_reads": true})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"keep_reads":true`) {
		t.Fatalf("body = %s, want the stored value echoed back", rr.Body.String())
	}
	if c, _ := h.channels.Get("UC1"); c == nil || !c.KeepReads {
		t.Fatal("keep_reads was not persisted")
	}

	// A request about the OTHER switch must leave this one alone, and say so.
	rr = putJSON(t, h, "/api/channels/UC1", map[string]any{"auto_summary": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"keep_reads":true`) {
		t.Fatalf("body = %s, want keep_reads reported as stored, not as its zero value", rr.Body.String())
	}
	if c, _ := h.channels.Get("UC1"); c == nil || !c.KeepReads {
		t.Fatal("a request about auto_summary turned keep_reads off")
	}
}

// TestChannelDetail_carriesBothChannelSwitches pins what the Settings tab reads.
// It needs both: keep_reads is meaningless while auto_summary is off, so the row
// disables itself rather than offering a toggle that does nothing.
func TestChannelDetail_carriesBothChannelSwitches(t *testing.T) {
	h := newPendingTestServer(t)
	h.seedChannel("UC1")
	if _, _, err := h.channels.SetKeepReads("UC1", true); err != nil {
		t.Fatalf("set keep_reads: %v", err)
	}

	body := getJSON(t, h, "/api/channels/UC1")
	if !strings.Contains(body, `"keep_reads":true`) {
		t.Fatalf("channel detail is missing keep_reads: %s", body)
	}
	if !strings.Contains(body, `"auto_summary":true`) {
		t.Fatalf("channel detail is missing auto_summary: %s", body)
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
