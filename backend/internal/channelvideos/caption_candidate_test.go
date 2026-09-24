package channelvideos

import (
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

// TestNextCaptionCandidate_skipsVideosAlreadyInThePipeline: the caption ladder
// exists to read an inbox video the user has NOT asked for. A video added by URL
// or the extension gets a videos row in queued/downloading/downloaded and never
// touches the ledger, so a ledger-only filter kept offering it — and the worker
// then reset its status and overwrote its transcript. A row that does not exist
// yet, one at 'new', or one whose download failed ('error': the inbox card
// would otherwise wait for captions forever) is a candidate; nothing else is.
func TestNextCaptionCandidate_skipsVideosAlreadyInThePipeline(t *testing.T) {
	st := newTestStore(t)
	vs := videos.New(st.db)
	seedChannel(t, st, "UC1")
	if _, err := st.db.Exec(`UPDATE channels SET auto_summary = 1 WHERE id = 'UC1'`); err != nil {
		t.Fatal(err)
	}
	if err := st.Insert(Entry{VideoID: "v1", ChannelID: "UC1", Title: "A video", URL: "https://youtu.be/v1", State: StatePending}); err != nil {
		t.Fatal(err)
	}

	c, err := st.NextCaptionCandidate()
	if err != nil || c == nil || c.VideoID != "v1" {
		t.Fatalf("no videos row: candidate = %+v, %v; want v1", c, err)
	}

	if err := vs.Upsert(videos.Video{ID: "v1", URL: "https://youtu.be/v1"}); err != nil {
		t.Fatal(err)
	}
	readable := map[string]bool{videos.StatusNew: true, videos.StatusError: true}
	for _, status := range videos.Statuses {
		if err := vs.SetStatus("v1", status, ""); err != nil {
			t.Fatalf("set status %s: %v", status, err)
		}
		c, err := st.NextCaptionCandidate()
		if err != nil {
			t.Fatal(err)
		}
		if got := c != nil; got != readable[status] {
			t.Fatalf("status %s: offered as a caption candidate = %v, want %v", status, got, readable[status])
		}
	}
}
