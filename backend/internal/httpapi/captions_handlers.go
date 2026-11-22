package httpapi

import (
	"net/http"

	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytgate"
	"github.com/trick77/ship/kit/httpx"
)

// handleFetchCaptions answers POST /api/videos/{id}/fetch-captions: fetches
// the captions of a library video that has none and, on a track, re-runs the
// analysis. The Player's fix for a download whose caption call failed or found
// nothing, short of deleting and re-downloading the media.
//
// 202 as soon as the fetch is handed off: it is a turn in the YouTube queue,
// and a handler never waits on one. How it ended lands in the Activity feed,
// and a summary event tells an open Player the transcript arrived.
func (s *server) handleFetchCaptions(w http.ResponseWriter, r *http.Request) {
	if s.videos == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "videos are not configured")
		return
	}
	id := r.PathValue("id")
	v, err := s.videos.Get(id)
	if err != nil {
		httpx.ServerError(w, r, err, "get video failed")
		return
	}
	if v == nil {
		httpx.WriteError(w, http.StatusNotFound, "video not found")
		return
	}
	if v.HasTranscript {
		httpx.WriteError(w, http.StatusConflict, "this video already has a transcript")
		return
	}
	// A download brings its own captions and runs the pipeline itself, so the
	// same rule as Reprocess.
	if v.Status == videos.StatusQueued || v.Status == videos.StatusDownloading {
		httpx.WriteError(w, http.StatusConflict, "download in progress; it fetches the captions itself")
		return
	}
	// A video never downloaded is the Inbox's: its caption fetcher reads it as
	// a truncated 'caption' transcript, which a 'download' one here would
	// silently replace. DownloadedAt, not status: stamped on the first success
	// and never cleared, so a tombstoned or failed re-download still counts.
	if v.DownloadedAt == "" {
		httpx.WriteError(w, http.StatusConflict, "video is not in the library")
		return
	}
	// summary_jobs has no unique active-job index: a download that landed
	// without captions still has its analysis queued, and a second job would
	// run it twice. The fetch re-checks inside its write transaction too.
	if v.SummaryStatus == videos.SummaryPending || v.SummaryStatus == videos.SummaryRunning {
		httpx.WriteError(w, http.StatusConflict, "a summary is already queued; try again when it finishes")
		return
	}
	if s.captions == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "caption fetching is not configured")
		return
	}
	// The Runner's gates, asked now rather than answered 202 and refused
	// later in Activity, where nobody clicking is looking.
	if s.settings != nil {
		paused, reason, err := s.settings.YoutubePaused(r.Context())
		if err != nil {
			httpx.ServerError(w, r, err, "read youtube pause state failed")
			return
		}
		if paused {
			msg := "YouTube access is paused"
			if reason != "" {
				msg += ": " + reason
			}
			httpx.WriteError(w, http.StatusConflict, msg)
			return
		}
		if !ytgate.CookieAllows(s.settings.CookieStatus(r.Context()), s.allowAnonymous) {
			httpx.WriteError(w, http.StatusConflict, "Your YouTube cookie needs refreshing before Peeq can fetch captions.")
			return
		}
	}
	if !s.captions.Start(v.ID, v.URL) {
		httpx.WriteError(w, http.StatusConflict, "captions are already being fetched")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
