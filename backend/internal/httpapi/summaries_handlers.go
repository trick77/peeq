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
// ListFailed is on the same interface because it answers the same question from
// the other end: what the queue is doing now, and what it gave up on. RetryFailed
// is the one write, kept here rather than on a second interface because a list of
// failures the user cannot act on is only half a feature.
type SummaryLister interface {
	ListActive() ([]summaryjobs.Job, error)
	ListFailed() ([]summaryjobs.Job, error)
	RetryFailed() (int64, error)
}

// summaryItem is the JSON shape returned by the summaries API: one in-flight
// summary job, joined with its video's title/channel for display. It mirrors
// downloadItem so the Queue page's two lanes read the same shape.
type summaryItem struct {
	ID          int64  `json:"id"`
	VideoID     string `json:"video_id"`
	Title       string `json:"title,omitempty"`
	ChannelName string `json:"channel_name,omitempty"`
	ChannelID   string `json:"channel_id,omitempty"`
	State       string `json:"state"`
	LastError   string `json:"last_error,omitempty"`
}

// handleSummariesList returns the in-flight summary queue (pending + running),
// joined with each job's video title/channel. Mirrors handleDownloadsList: an
// unwired store reports an empty list (200 + []), not 503 — the queue simply
// has nothing summarizing, which is what an empty list already says.
func (s *server) handleSummariesList(w http.ResponseWriter, r *http.Request) {
	s.writeSummaryList(w, r, func() ([]summaryjobs.Job, error) { return s.summaryList.ListActive() }, "list summaries failed")
}

// handleSummariesFailedList answers GET /api/summaries/failed: the jobs that
// exhausted their attempts.
//
// A separate route rather than a section inside handleSummariesList, so the
// existing response stays the bare array the Queue page already consumes.
//
// These jobs are invisible without it. They are gone from ListActive, the boot
// sweep skips them on purpose, and a job that died after the summary step left
// summary_status='done' behind — so the video reads as complete everywhere in
// the UI while its highlights or its search index are permanently missing.
// LastError is what tells the two apart.
func (s *server) handleSummariesFailedList(w http.ResponseWriter, r *http.Request) {
	s.writeSummaryList(w, r, func() ([]summaryjobs.Job, error) { return s.summaryList.ListFailed() }, "list failed summaries failed")
}

// handleSummariesRetryFailed answers POST /api/summaries/retry-failed: put every
// failed job back in the queue with a fresh attempt budget, and report how many
// moved. This is the recovery path after the thing that broke them — usually a
// chat endpoint that was down or crawling — has been fixed.
func (s *server) handleSummariesRetryFailed(w http.ResponseWriter, r *http.Request) {
	if s.summaryList == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "summaries are not configured")
		return
	}
	n, err := s.summaryList.RetryFailed()
	if err != nil {
		serverError(w, r, err, "retry failed summaries failed")
		return
	}
	writeJSON(w, map[string]int64{"requeued": n})
}

// writeSummaryList renders a job list, joining each row with its video's
// title/channel for display. An unwired store reports an empty list (200 + []),
// not 503 — mirroring handleDownloadsList: the queue simply has nothing in it,
// which is what an empty list already says.
func (s *server) writeSummaryList(w http.ResponseWriter, r *http.Request, list func() ([]summaryjobs.Job, error), errMsg string) {
	if s.summaryList == nil {
		writeJSON(w, []summaryItem{})
		return
	}
	all, err := list()
	if err != nil {
		serverError(w, r, err, errMsg)
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
				item.ChannelID = v.ChannelID
			}
		}
		items = append(items, item)
	}
	writeJSON(w, items)
}
