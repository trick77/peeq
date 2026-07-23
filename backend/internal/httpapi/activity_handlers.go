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

// handleActivityUpcoming serves the future half: a live projection over the
// existing schedules and queues (never stored). It gathers the soonest channel
// scans and metadata refreshes (timed) plus the PENDING downloads and summaries
// (ordered, not timed — a running job renders at the *now* marker on the client,
// so it must not appear here too), merges them, caps at 20, and reports how many
// were dropped for the top-edge label. Everything is best-effort: a store that
// errors simply contributes nothing rather than failing the whole projection.
func (s *server) handleActivityUpcoming(w http.ResponseWriter, r *http.Request) {
	var items []activity.UpcomingItem

	if s.channels != nil {
		if scans, err := s.channels.ScanDueSoon(20); err == nil {
			for _, c := range scans {
				items = append(items, activity.UpcomingItem{
					At: c.At, Kind: activity.KindScan, Subject: c.Name, Summary: "channel scan",
				})
			}
		}
		if metas, err := s.channels.MetaDueSoon(20); err == nil {
			for _, c := range metas {
				items = append(items, activity.UpcomingItem{
					At: c.At, Kind: activity.KindChannelMeta, Subject: c.Name, Summary: "metadata refresh",
				})
			}
		}
	}
	if s.jobs != nil {
		if all, err := s.jobs.List(); err == nil {
			for _, j := range all {
				if j.State != "pending" {
					continue
				}
				items = append(items, activity.UpcomingItem{
					Kind: activity.KindDownload, Approx: true,
					Subject: s.videoTitle(j.VideoID), Summary: "download",
				})
			}
		}
	}
	if s.summaryList != nil {
		if all, err := s.summaryList.ListActive(); err == nil {
			for _, j := range all {
				if j.State != "pending" {
					continue // running summaries render at *now* on the client, not here
				}
				items = append(items, activity.UpcomingItem{
					Kind: activity.KindSummary, Approx: true,
					Subject: s.videoTitle(j.VideoID), Summary: "summary",
				})
			}
		}
	}

	merged, truncated := activity.Merge(items, 20)
	if merged == nil {
		merged = []activity.UpcomingItem{}
	}
	writeJSON(w, upcomingResponse{Items: merged, Truncated: truncated})
}

// videoTitle resolves a video's display title for a projection row, falling
// back to the id. Best-effort — a missing/errored row just shows the id.
func (s *server) videoTitle(videoID string) string {
	if s.videos != nil {
		if v, err := s.videos.Get(videoID); err == nil && v != nil && v.Title != "" {
			return v.Title
		}
	}
	return videoID
}
