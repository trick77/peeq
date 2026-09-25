package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/jobs"
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
	ChannelID   string `json:"channel_id,omitempty"`
	State       string `json:"state"`
	Priority    int    `json:"priority"`
	Attempts    int    `json:"attempts"`
	LastError   string `json:"last_error,omitempty"`
	EnqueuedAt  string `json:"enqueued_at,omitempty"`
	// NextAttemptAt is set on a pending job that is waiting out a retry
	// backoff (SQLite datetime text, UTC); later jobs run ahead of it.
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
}

// handleDownloadsPost is the session-authenticated entry point that adds a
// video to the download queue (the peeq web UI's "Add" view). It schedules
// instantly and never waits on a network call; see enqueueDownloadByURL for
// the shared mechanics. A human deliberately pasting a link may re-add a video
// they already have, so this route always re-queues (requeueExisting=true) —
// unchanged from before the machine route was extracted out of it.
func (s *server) handleDownloadsPost(w http.ResponseWriter, r *http.Request) {
	var req downloadsPostRequest
	if !decodeJSON(w, r, &req, maxJSONBody, "url is required") {
		return
	}

	item, _, ee := s.enqueueDownloadByURL(req.URL, true)
	if ee != nil {
		s.writeEnqueueError(w, r, ee)
		return
	}
	writeJSONStatus(w, http.StatusCreated, item)
}

// handleMachineDownloadsPost is the token-authenticated add-a-video path, used
// by the peeq browser extension (Safari/Chrome). It mirrors the machine cookie
// route: token-gated, no session, a narrow surface. It differs from the
// session route in one way — requeueExisting=false — because a one-click
// toolbar button invites double-taps: re-adding a video already queued,
// downloading, or downloaded must NOT reset it to 'queued' and enqueue a second
// job. Such a request is a no-op that returns 200 with the existing item
// (duplicate) rather than 201.
func (s *server) handleMachineDownloadsPost(w http.ResponseWriter, r *http.Request) {
	var req downloadsPostRequest
	if !decodeJSON(w, r, &req, maxJSONBody, "url is required") {
		return
	}

	item, duplicate, ee := s.enqueueDownloadByURL(req.URL, false)
	if ee != nil {
		s.writeEnqueueError(w, r, ee)
		return
	}
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	writeJSONStatus(w, status, item)
}

// enqueueError carries a failed enqueue's HTTP status and client-facing
// message. A non-nil cause marks a 500 that must be logged (and its message
// kept server-side) via serverError, rather than echoed to the client.
type enqueueError struct {
	status  int
	message string
	cause   error
}

// writeEnqueueError renders an enqueueError: a 500 (cause != nil) goes through
// serverError so it is logged with the request; every other status is a
// client-safe writeJSONError.
func (s *server) writeEnqueueError(w http.ResponseWriter, r *http.Request, ee *enqueueError) {
	if ee.cause != nil {
		serverError(w, r, ee.cause, ee.message)
		return
	}
	writeJSONError(w, ee.status, ee.message)
}

// alreadyInQueue reports whether a video's status means it is already in peeq's
// pipeline — queued, actively downloading, or downloaded. The machine route
// treats these as "nothing to do" (a duplicate) rather than re-queueing. Other
// states (new, error, tombstoned) are re-queueable: adding them by explicit
// button click is a legitimate (re)start.
func alreadyInQueue(status string) bool {
	switch status {
	case videos.StatusQueued, videos.StatusDownloading, videos.StatusDownloaded:
		return true
	default:
		return false
	}
}

// enqueueDownloadByURL is the shared add-a-video mechanic behind both the
// session route (handleDownloadsPost) and the machine route
// (handleMachineDownloadsPost). It canonicalizes the pasted url (rejecting
// playlists and live/premiere content up front, all network-free) → optionally
// short-circuits an already-present video → upserts a minimal video row (id +
// url only) if the video is new → marks it 'queued' → enqueues a download job
// at the standard priority. It never fetches metadata: the worker's preflight
// resolves title/channel and surfaces any missing cookie or unavailable video
// as a paused/failed job on the Activity page, so the caller is never blocked.
//
// requeueExisting selects the re-add behavior. true (session route) always
// re-queues, preserving the web UI's long-standing "paste it again and it goes
// back in the queue" behavior. false (machine route) returns an existing
// in-pipeline video untouched, with duplicate=true, so a stray button tap can't
// resurrect an already-downloaded video.
func (s *server) enqueueDownloadByURL(rawURL string, requeueExisting bool) (item downloadItem, duplicate bool, ee *enqueueError) {
	if s.jobs == nil || s.videos == nil || s.runner == nil {
		return downloadItem{}, false, &enqueueError{status: http.StatusServiceUnavailable, message: "downloads are not configured"}
	}
	if strings.TrimSpace(rawURL) == "" {
		return downloadItem{}, false, &enqueueError{status: http.StatusBadRequest, message: "url is required"}
	}

	watchURL, id, kind, err := ytdlp.Canonicalize(rawURL)
	if err != nil {
		return downloadItem{}, false, &enqueueError{status: http.StatusBadRequest, message: "invalid url: " + err.Error()}
	}
	switch kind {
	case "playlist":
		return downloadItem{}, false, &enqueueError{status: http.StatusBadRequest, message: "Paste a single video link, not a playlist"}
	case "live":
		return downloadItem{}, false, &enqueueError{status: http.StatusBadRequest, message: "Live videos and premieres aren't supported; paste the link again once it has finished and is a regular video"}
	case "channel":
		return downloadItem{}, false, &enqueueError{status: http.StatusBadRequest, message: "That's a channel link — add it under Channels, not here"}
	}

	// Guard with a Get: Upsert overwrites title/channel/thumbnail with the empty
	// values we have now, so a blind Upsert would wipe metadata when a known
	// video is re-added. An existing row keeps its metadata untouched.
	existing, err := s.videos.Get(id)
	if err != nil {
		return downloadItem{}, false, &enqueueError{status: http.StatusInternalServerError, message: "load video failed", cause: err}
	}

	// Machine-route re-queue guard: a video already in the pipeline is returned
	// as a duplicate no-op, echoing its current state so the caller can tell the
	// user "already downloaded" vs "already queued". The session route passes
	// requeueExisting=true and never reaches this branch.
	if existing != nil && !requeueExisting && alreadyInQueue(existing.Status) {
		return downloadItem{
			VideoID:     id,
			Title:       existing.Title,
			ChannelName: existing.ChannelName,
			ChannelID:   existing.ChannelID,
			State:       existing.Status,
		}, true, nil
	}

	// One transaction for the status flip, the job row and any pending Inbox
	// row for this video (see videos.Store.EnqueueDownload): a failed insert
	// used to leave a 'queued' video with no job, and the Inbox row stayed
	// 'pending' forever because this path never touched the ledger. Upsert
	// rules are unchanged: an existing row keeps its metadata, and neither
	// form touches status except to set 'queued'.
	//
	// Deliberately NOT adding the video's channel here. Adding one video by
	// URL is a one-off; adding (and subscribing) a channel stays an explicit
	// action, and only an added channel is ever scanned for new videos.
	//
	// The channel is not invisible, though: once the worker resolves the
	// metadata it caches a channels row (added_at NULL), which puts the
	// channel in the list under the "From downloads" filter — reachable and
	// subscribable, but never scanned. The video keeps its channel_id on its
	// own row either way; videos has no foreign key to channels, so a
	// channel_id with no added channel behind it is a normal, supported state.
	var jobID int64
	if existing == nil {
		jobID, err = s.videos.UpsertAndEnqueueDownload(videos.Video{ID: id, URL: watchURL}, downloadPriority)
	} else {
		jobID, err = s.videos.EnqueueDownload(id, downloadPriority)
	}
	if err != nil {
		return downloadItem{}, false, &enqueueError{status: http.StatusInternalServerError, message: "enqueue failed", cause: err}
	}
	// A pending Inbox row for this video just left the Inbox inside that
	// transaction; its cached poster is reclaimed here, as the Inbox's own
	// approve does. A no-op when there was no such row.
	s.removePendingThumbnail(id)

	// Title/channel are intentionally absent: they are not known until the
	// worker's metadata preflight runs. The UI shows a generic "added to the
	// queue" confirmation and the title fills in once the job starts.
	return downloadItem{
		JobID:    jobID,
		VideoID:  id,
		State:    jobs.StatePending,
		Priority: downloadPriority,
	}, false, nil
}

// finishedJobsWindow is how many terminal (done, failed, canceled) jobs the
// queue list still carries. The page renders pending and running; this is
// a margin for what just finished.
const finishedJobsWindow = 20

// handleDownloadsList returns the download queue — every pending and running
// job, plus the newest finishedJobsWindow terminal ones — joined with each
// job's video title/channel for display.
func (s *server) handleDownloadsList(w http.ResponseWriter, r *http.Request) {
	if s.jobs == nil {
		writeJSON(w, []downloadItem{})
		return
	}
	all, err := s.jobs.ListQueue(finishedJobsWindow)
	if err != nil {
		serverError(w, r, err, "list downloads failed")
		return
	}

	index, err := s.videoIndex(idsOf(all, func(j jobs.Job) string { return j.VideoID }))
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

			NextAttemptAt: j.NextAttemptAt,
		}
		if v := index[j.VideoID]; v != nil {
			item.Title = v.Title
			item.ChannelName = v.ChannelName
			item.ChannelID = v.ChannelID
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
		paused, reason, err := s.settings.YoutubePaused(r.Context())
		if err != nil {
			serverError(w, r, err, "read youtube pause state failed")
			return
		}
		resp.YoutubePaused, resp.YoutubePauseReason = paused, reason
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
