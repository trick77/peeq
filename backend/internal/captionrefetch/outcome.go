package captionrefetch

import "github.com/trick77/peeq/internal/activity"

// The Activity feed is where the user learns how a fetch ended: the request
// answered 202 long before. KindDownload, not a kind of its own: activity_events
// pins kind in a CHECK constraint, and this is a YouTube fetch for a library
// video, the download worker's ground.

func (s *Service) recordFetched(videoID, lang string) {
	s.record(videoID, activity.OutcomeOK, "captions fetched", lang)
}

func (s *Service) recordNoCaptions(videoID string) {
	s.record(videoID, activity.OutcomeWarn, "no captions found", "YouTube has no caption track for this video")
}

func (s *Service) recordRefused(videoID string, err error) {
	s.record(videoID, activity.OutcomeWarn, "caption fetch refused", redacted(err))
}

// recordRateLimited: a 429/5xx says "not now", not "this video has none".
func (s *Service) recordRateLimited(videoID string) {
	s.record(videoID, activity.OutcomeWarn, "caption fetch refused", "YouTube refused the captions for now; try again later")
}

func (s *Service) recordFailed(videoID string, err error) {
	s.record(videoID, activity.OutcomeFail, "caption fetch failed", redacted(err))
}

// record writes one event, named by the video's title, or its id when the
// video has none or is gone, so the row is never blank.
func (s *Service) record(videoID, outcome, summary, detail string) {
	if s.d.Activity == nil {
		return
	}
	title := videoID
	if v, err := s.d.Videos.Get(videoID); err == nil && v != nil && v.Title != "" {
		title = v.Title
	}
	activity.Record(s.d.Activity, activity.Event{
		Kind: activity.KindDownload, Outcome: outcome,
		SubjectID: videoID, Subject: title, Summary: summary, Detail: detail,
	})
}
