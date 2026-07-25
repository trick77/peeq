package httpapi

import (
	"net/http"
	"strconv"

	"github.com/trick77/peeq/internal/activity"
)

// ActivityReader is the read side of the activity log the Activity page needs.
// Declared here (not server.go) so the activity import stays confined to this
// file, matching the SummaryLister pattern. The real *activity.Store satisfies it.
type ActivityReader interface {
	Recent(beforeID int64, limit int) (activity.Page, error)
	RetainedMax() int
}

const (
	activityDefaultLimit = 40
	activityMaxLimit     = 100
	// upcomingCap bounds the future projection: how many scheduled items Up next
	// renders, and the ceiling on each per-source query so a large subscription
	// list can't turn one request into a full table scan per source.
	upcomingCap = 20
)

type activityListResponse struct {
	Events      []activity.Event `json:"events"`
	HasMore     bool             `json:"has_more"`
	RetainedMax int              `json:"retained_max"`
}

// handleActivityList serves the past half of the agenda: a keyset page of the
// log, newest first. `before` is the id to page back from (0 = newest);
// `limit` defaults to 40 and is clamped to 100. Nil store → 503, matching every
// other optional dependency.
func (s *server) handleActivityList(w http.ResponseWriter, r *http.Request) {
	if s.activity == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "activity is not configured")
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit := activityDefaultLimit
	if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
		limit = l
	}
	if limit > activityMaxLimit {
		limit = activityMaxLimit
	}

	page, err := s.activity.Recent(before, limit)
	if err != nil {
		serverError(w, r, err, "list activity failed")
		return
	}
	events := page.Events
	if events == nil {
		events = []activity.Event{}
	}
	writeJSON(w, activityListResponse{
		Events: events, HasMore: page.HasMore, RetainedMax: s.activity.RetainedMax(),
	})
}

type upcomingResponse struct {
	Items     []activity.UpcomingItem `json:"items"`
	Truncated int                     `json:"truncated"`
}

// handleActivityUpcoming serves peeq's own timed schedule: a live projection
// over the existing schedules (never stored). It gathers the soonest channel
// scans and metadata refreshes, merges them, caps at 20, and reports how many
// were dropped for the edge label. Everything is best-effort: a store that
// errors simply contributes nothing rather than failing the whole projection.
//
// Pending downloads and summaries used to be projected here too, as ordered
// (untimed) items. Up next now renders those from the jobs and summaries the
// client already holds, with live progress the projection never had, so
// emitting them here would print every waiting download twice. Dropping them
// also hands the whole cap back to the timed items: ordered ones sorted ahead
// of every timed one in Merge and shared the same budget of 20, so a backlog of
// 20+ pending downloads used to return no scheduled items at all — the schedule
// section vanished exactly when peeq was busiest.
func (s *server) handleActivityUpcoming(w http.ResponseWriter, r *http.Request) {
	var items []activity.UpcomingItem

	if s.channels != nil {
		if scans, err := s.channels.ScanDueSoon(upcomingCap); err == nil {
			for _, c := range scans {
				items = append(items, activity.UpcomingItem{
					At: c.At, Kind: activity.KindScan, SubjectID: c.ChannelID,
					Subject: c.Name, Summary: "channel scan",
				})
			}
		}
		if metas, err := s.channels.MetaDueSoon(upcomingCap); err == nil {
			for _, c := range metas {
				items = append(items, activity.UpcomingItem{
					At: c.At, Kind: activity.KindChannelMeta, SubjectID: c.ChannelID,
					Subject: c.Name, Summary: "metadata refresh",
				})
			}
		}
	}
	merged, truncated := activity.Merge(items, upcomingCap)
	if merged == nil {
		merged = []activity.UpcomingItem{}
	}
	writeJSON(w, upcomingResponse{Items: merged, Truncated: truncated})
}
