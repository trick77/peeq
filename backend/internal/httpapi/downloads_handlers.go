package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/sse"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// downloadPriority is the priority every manually-added download is
// enqueued at. There is no UI (yet) for other priorities; channel-scan
// discovered videos will use a lower value in a later task.
const downloadPriority = 10

// DownloadsRunner is the subset of *ytdlp.Runner the downloads API needs:
// fetching metadata for a single already-canonicalized video url before it
// is enqueued. Declaring it here (rather than depending on the concrete
// type) keeps the handler testable with a fake that never shells out to
// yt-dlp; the real *ytdlp.Runner satisfies it.
type DownloadsRunner interface {
	Metadata(ctx context.Context, url string) (*ytdlp.Meta, error)
}

// DownloadsWorker is the subset of *download.Worker the downloads API and the
// settings cookie handler need. It is optional — when unset, cancel falls back
// to marking a pending job canceled directly in the store (see
// handleDownloadsCancel), and Resume/Paused/LowDisk have no effect.
//
//   - Cancel reports whether a pending/running job was actually cancelled, so
//     the handler can distinguish an unknown or already-finished job (404)
//     from a real cancel (200).
//   - Resume clears a cookie-block pause so a freshly re-pasted valid cookie
//     un-wedges the queue (see handlePutSettingsCookie).
//   - Paused / LowDisk surface the worker's stalled-queue state so the UI can
//     tell the user why nothing is downloading (see handleDownloadsStatus).
type DownloadsWorker interface {
	Cancel(jobID int64) bool
	Resume()
	Paused() bool
	LowDisk() bool
}

// downloadsPostRequest is the body of POST /api/downloads.
type downloadsPostRequest struct {
	URL string `json:"url"`
}

// downloadItem is the JSON shape returned by the downloads API: one queue
// entry, optionally joined with its video's title/channel for display.
type downloadItem struct {
	JobID       int64  `json:"job_id"`
	VideoID     string `json:"video_id"`
	Title       string `json:"title,omitempty"`
	ChannelName string `json:"channel_name,omitempty"`
	State       string `json:"state"`
	Priority    int    `json:"priority"`
	Attempts    int    `json:"attempts"`
	LastError   string `json:"last_error,omitempty"`
	EnqueuedAt  string `json:"enqueued_at,omitempty"`
}

// handleDownloadsPost is the only entry point that adds a video to the
// download queue. Flow: canonicalize the pasted url (rejecting playlists
// and live/premiere content up front, before any network call) → fetch
// metadata (surfacing a missing/invalid cookie as 409, never a silent
// failure) → upsert the video row as 'queued' → enqueue a download job at
// the standard priority.
func (s *server) handleDownloadsPost(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil || s.videos == nil || s.runner == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "downloads are not configured")
		return
	}

	var req downloadsPostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		writeJSONError(w, http.StatusBadRequest, "url is required")
		return
	}

	watchURL, id, kind, err := ytdlp.Canonicalize(req.URL)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid url: "+err.Error())
		return
	}
	switch kind {
	case "playlist":
		writeJSONError(w, http.StatusBadRequest, "Paste a single video link, not a playlist")
		return
	case "live":
		writeJSONError(w, http.StatusBadRequest, "Live videos and premieres aren't supported; paste the link again once it has finished and is a regular video")
		return
	case "channel":
		writeJSONError(w, http.StatusBadRequest, "That's a channel link — add it under Channels, not here")
		return
	}
	// Interactive: the user pasted this url and is waiting on the answer, so it
	// skips the pacer's background queue (see ytdlp.WithInteractive).
	meta, err := s.runner.Metadata(ytdlp.WithInteractive(r.Context()), watchURL)
	if err != nil {
		if errors.Is(err, ytdlp.ErrNoCookie) {
			writeJSONError(w, http.StatusConflict, "cookie required")
			return
		}
		writeJSONError(w, http.StatusBadGateway, "fetch metadata failed: "+err.Error())
		return
	}

	videoID := meta.ID
	if videoID == "" {
		videoID = id
	}

	if err := s.videos.Upsert(videos.Video{
		ID:              videoID,
		URL:             watchURL,
		Title:           meta.Title,
		ChannelID:       meta.ChannelID,
		ChannelName:     meta.Channel,
		DurationSeconds: int64(meta.DurationSeconds),
		PublishedAt:     meta.PublishedAt,
		ThumbnailPath:   meta.Thumbnail,
		Availability:    videos.NormalizeAvailability(meta.Availability),
	}); err != nil {
		serverError(w, r, err, "save video failed")
		return
	}
	// Upsert deliberately never touches status (so re-running metadata on an
	// already-downloaded video can't wipe its state); a fresh add must be
	// marked 'queued' explicitly.
	if err := s.videos.SetStatus(videoID, "queued", ""); err != nil {
		serverError(w, r, err, "save video failed")
		return
	}

	// Deliberately NOT tracking the video's channel here. Adding one video by
	// URL is a one-off; it must not silently populate the Channels view with
	// every channel the user has ever grabbed a single video from. Tracking
	// (and subscribing) stays an explicit action on the Channels page. The
	// video keeps meta.ChannelID on its own row — videos has no foreign key
	// to channels, so an untracked channel_id is a normal, supported state.

	jobID, err := s.jobs.Enqueue(videoID, downloadPriority)
	if err != nil {
		serverError(w, r, err, "enqueue job failed")
		return
	}

	writeJSONStatus(w, http.StatusCreated, downloadItem{
		JobID:       jobID,
		VideoID:     videoID,
		Title:       meta.Title,
		ChannelName: meta.Channel,
		State:       "pending",
		Priority:    downloadPriority,
	})
}

// handleDownloadsList returns the whole download queue (every state, not
// just pending), joined with each job's video title/channel for display.
func (s *server) handleDownloadsList(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeJSON(w, []downloadItem{})
		return
	}
	all, err := s.jobs.List()
	if err != nil {
		serverError(w, r, err, "list downloads failed")
		return
	}

	items := make([]downloadItem, 0, len(all))
	for _, j := range all {
		item := downloadItem{
			JobID:      j.ID,
			VideoID:    j.VideoID,
			State:      j.State,
			Priority:   j.Priority,
			Attempts:   j.Attempts,
			LastError:  j.LastError,
			EnqueuedAt: j.EnqueuedAt,
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

// downloadsStatusResponse reports why the download queue may be stalled, so
// the UI can show a diagnostic banner instead of leaving the user staring at
// a frozen queue with no explanation.
type downloadsStatusResponse struct {
	Paused             bool   `json:"paused"`
	LowDisk            bool   `json:"low_disk"`
	YoutubePaused      bool   `json:"youtube_paused"`
	YoutubePauseReason string `json:"youtube_pause_reason"`
}

// handleDownloadsStatus surfaces the worker's paused (cookie-blocked) and
// low-disk state, plus the global YouTube kill-switch. When no worker is
// wired it reports the not-stalled default (200, both false) rather than
// 503 — the queue simply has no worker to be stalled.
func (s *server) handleDownloadsStatus(w http.ResponseWriter, r *http.Request) {
	resp := downloadsStatusResponse{}
	if s.worker != nil {
		resp.Paused = s.worker.Paused()
		resp.LowDisk = s.worker.LowDisk()
	}
	if s.settings != nil {
		resp.YoutubePaused, resp.YoutubePauseReason = s.settings.YoutubePaused(r.Context())
	}
	writeJSON(w, resp)
}

// handlePauseYoutube engages the kill-switch manually (reason ”).
func (s *server) handlePauseYoutube(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings not configured")
		return
	}
	if err := s.settings.SetYoutubePaused(r.Context(), true, ""); err != nil {
		serverError(w, r, err, "pause failed")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleResumeYoutube clears the kill-switch and resets the failure monitor
// so the user gets a fresh auto-pause window.
func (s *server) handleResumeYoutube(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings not configured")
		return
	}
	if err := s.settings.SetYoutubePaused(r.Context(), false, ""); err != nil {
		serverError(w, r, err, "resume failed")
		return
	}
	if s.onResumeYoutube != nil {
		s.onResumeYoutube()
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleDownloadsCancel cancels one job by id. If a worker is wired, it owns
// the cancel (it knows whether the job is currently running and needs its
// context killed vs. merely pending); otherwise this falls back to marking
// a pending job canceled directly in the store. Either path reports whether
// anything was actually cancelled; an unknown or already-finished job id
// yields 404 rather than a false-positive 200.
func (s *server) handleDownloadsCancel(w http.ResponseWriter, r *http.Request) {
	jobID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid job id")
		return
	}

	var canceled bool
	switch {
	case s.worker != nil:
		canceled = s.worker.Cancel(jobID)
	case s.jobs != nil:
		canceled, err = s.jobs.Cancel(jobID)
		if err != nil {
			serverError(w, r, err, "cancel failed")
			return
		}
	default:
		writeJSONError(w, http.StatusServiceUnavailable, "downloads are not configured")
		return
	}
	if !canceled {
		writeJSONError(w, http.StatusNotFound, "job not found or not cancelable")
		return
	}
	writeJSON(w, map[string]string{"status": "canceled"})
}

// sseHeartbeatInterval keeps the stream alive through idle reverse proxies.
const sseHeartbeatInterval = 15 * time.Second

// handleDownloadsStream is an SSE feed of download progress/queue events,
// fanned out from the worker's progress callback via the shared Hub (see
// main.go's wiring). Each connection gets its own subscription; it sees
// only events published after it connects.
func (s *server) handleDownloadsStream(w http.ResponseWriter, r *http.Request) {
	if s.sseHub == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "downloads stream is not configured")
		return
	}
	writer, err := sse.NewWriter(w)
	if err != nil {
		serverError(w, r, err, "streaming not supported")
		return
	}

	ch, unsubscribe := s.sseHub.Subscribe()
	defer unsubscribe()
	stopHeartbeat := writer.Heartbeat(r.Context(), sseHeartbeatInterval)
	defer stopHeartbeat()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := writer.Send(ev.Name, ev.Data); err != nil {
				return
			}
		}
	}
}
