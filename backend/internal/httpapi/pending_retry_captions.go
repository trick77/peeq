package httpapi

import (
	"net/http"

	"github.com/trick77/peeq/internal/channelvideos"
)

// handlePendingRetryCaptions answers POST /api/pending/{id}/retry-captions: it
// puts an inbox video whose caption fetch gave up back at the start of the
// ladder. Nothing else revisits such a video, so without this a failure that
// has since been fixed stays a blank card for good.
//
// It talks to the ledger only. The fetch itself happens on the caption
// fetcher's next tick, behind the cookie gate, the kill switch and the
// throttle — a handler that called YouTube would be a click-driven way round
// all three.
//
// 404 when the video is not in the Inbox, 409 when it is but has nothing to
// retry (see channelvideos.Store.RetryCaptions for the conditions).
func (s *server) handlePendingRetryCaptions(w http.ResponseWriter, r *http.Request) {
	if s.ledger == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "pending is not configured")
		return
	}
	id := r.PathValue("id")
	e, err := s.ledger.Get(id)
	if err != nil {
		serverError(w, r, err, "load pending failed")
		return
	}
	if e == nil || e.State != channelvideos.StatePending {
		writeJSONError(w, http.StatusNotFound, "pending item not found")
		return
	}
	ok, err := s.ledger.RetryCaptions(id)
	if err != nil {
		serverError(w, r, err, "retry captions failed")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusConflict, "captions cannot be retried for this video")
		return
	}
	writeJSON(w, map[string]string{"status": "retrying"})
}
