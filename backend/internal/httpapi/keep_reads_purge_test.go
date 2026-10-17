package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/trick77/peeq/internal/videos"
)

// The dialog's counts come from /keep-reads/impact, and the switch-off PUT then
// purges exactly those videos in the background.
func TestKeepReadsOff_impactThenPurge(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	purged := make(chan string, 1)
	deps.OnKeptPurged = func(id string) { purged <- id }
	db := deps.Channels.DB()
	if _, err := db.Exec(`INSERT INTO channels (id, name, added_at) VALUES ('UCk', 'k', '2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO channel_videos (video_id, channel_id, title, state) VALUES ('tomb', 'UCk', 't', 'queued')`); err != nil {
		t.Fatal(err)
	}
	if err := deps.Videos.Upsert(videos.Video{ID: "tomb", URL: "u", ChannelID: "UCk"}); err != nil {
		t.Fatal(err)
	}
	if err := deps.Videos.Tombstone("tomb"); err != nil {
		t.Fatal(err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)

	rr := doReq(t, h, cookie, http.MethodGet, "/api/channels/UCk/keep-reads/impact", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("impact status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got videos.KeptCounts
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != (videos.KeptCounts{Deleted: 1}) {
		t.Fatalf("impact = %+v, want 1 deleted", got)
	}

	rr = putJSONWithCookie(t, h, cookie, "/api/channels/UCk", map[string]any{"keep_reads": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case <-purged:
	case <-time.After(5 * time.Second):
		t.Fatal("purge did not settle")
	}
	if v, err := deps.Videos.Get("tomb"); err != nil || v != nil {
		t.Fatalf("tomb = %+v err=%v after switch-off, want purged", v, err)
	}
}

// Turning the switch on starts nothing.
func TestKeepReadsOn_purgesNothing(t *testing.T) {
	deps := channelsTestDeps(t, &testResolver{})
	purged := make(chan string, 1)
	deps.OnKeptPurged = func(id string) { purged <- id }
	db := deps.Channels.DB()
	if _, err := db.Exec(`INSERT INTO channels (id, name, added_at, keep_reads) VALUES ('UCk', 'k', '2026-01-01', 0)`); err != nil {
		t.Fatal(err)
	}
	h := New(deps)
	cookie := loginAndGetCookie(t, h)
	if rr := putJSONWithCookie(t, h, cookie, "/api/channels/UCk", map[string]any{"keep_reads": true}); rr.Code != http.StatusOK {
		t.Fatalf("PUT status = %d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case <-purged:
		t.Fatal("a purge ran on switch-on")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestKeepReadsImpact_unknownChannel_404(t *testing.T) {
	h := newChannelsTestServer(t, &testResolver{})
	cookie := loginAndGetCookie(t, h)
	if rr := doReq(t, h, cookie, http.MethodGet, "/api/channels/UCnope/keep-reads/impact", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}
