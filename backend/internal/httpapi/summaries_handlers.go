package httpapi

import (
	"net/http"

	"github.com/trick77/peeq/internal/summaryjobs"
)

// SummaryLister is the read side of the summary queue the Queue page needs:
// every pending/running summary job. Declared here (rather than in server.go)
// so the summaryjobs import stays confined to this file — server.go keeps its
// deliberate decoupling from the concrete store, exactly as the enqueue side
// (SummaryEnqueuer) does. The real *summaryjobs.Store satisfies it.
type SummaryLister interface {
	ListActive() ([]summaryjobs.Job, error)
}

// summaryItem is the JSON shape returned by the summaries API: one in-flight
// summary job, joined with its video's title/channel for display. It mirrors
// downloadItem so the Queue page's two lanes read the same shape.
type summaryItem struct {
	ID          int64  `json:"id"`
	VideoID     string `json:"video_id"`
	Title       string `json:"title,omitempty"`
	ChannelName string `json:"channel_name,omitempty"`
	State       string `json:"state"`
	LastError   string `json:"last_error,omitempty"`
}

// handleSummariesList returns the in-flight summary queue (pending + running),
// joined with each job's video title/channel. Mirrors handleDownloadsList: an
// unwired store reports an empty list (200 + []), not 503 — the queue simply
// has nothing summarizing, which is what an empty list already says.
func (s *server) handleSummariesList(w http.ResponseWriter, r *http.Request) {
	if s.summaryList == nil {
		writeJSON(w, []summaryItem{})
		return
	}
	all, err := s.summaryList.ListActive()
	if err != nil {
		serverError(w, r, err, "list summaries failed")
		return
	}

	items := make([]summaryItem, 0, len(all))
	for _, j := range all {
		item := summaryItem{
			ID:        j.ID,
			VideoID:   j.VideoID,
			State:     j.State,
			LastError: j.LastError,
		}
		if s.videos != nil {
			if v, err := s.videos.Get(j.VideoID); err == nil && v != nil {
				item.Title = v.Title
				item.ChannelName = v.ChannelName
			}
		}
		items = append(items, item)
	}
	writeJSON(w, items)
}
