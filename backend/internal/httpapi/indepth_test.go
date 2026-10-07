package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

// The video page carries the in-depth summary; a video with none (analysed
// before the step existed) carries the field empty, which hides the card.
func TestGetVideo_carriesTheInDepthSummary(t *testing.T) {
	h := newPendingTestServer(t)
	for _, id := range []string{"d1", "d2"} {
		if err := h.videos.Upsert(videos.Video{ID: id, URL: "https://youtu.be/" + id}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := h.videos.SetSummaryStatus(id, videos.SummaryDone, ""); err != nil {
			t.Fatalf("set status: %v", err)
		}
	}
	if err := h.videos.SetInDepth("d1", "Lead.\n\n### A point [0:00]\n\nBody."); err != nil {
		t.Fatalf("set in-depth: %v", err)
	}

	for id, want := range map[string]string{"d1": "Lead.\n\n### A point [0:00]\n\nBody.", "d2": ""} {
		var got struct {
			InDepth string `json:"in_depth"`
		}
		if err := json.Unmarshal([]byte(getJSON(t, h, "/api/videos/"+id)), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.InDepth != want {
			t.Errorf("%s in_depth = %q, want %q", id, got.InDepth, want)
		}
	}
}
