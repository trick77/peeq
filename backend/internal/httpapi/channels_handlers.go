package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// ChannelResolver resolves a canonicalized channel url to its authoritative
// UCID and display name via yt-dlp. Declaring it here (rather than depending
// on the concrete *ytdlp.Runner type) keeps the handler testable with a fake
// that never shells out to yt-dlp; the real *ytdlp.Runner satisfies it.
type ChannelResolver interface {
	ResolveChannel(ctx context.Context, url string) (ucid, name string, err error)
}

// var _ ChannelResolver = (*ytdlp.Runner)(nil) proves at compile time that
// the real Runner still satisfies ChannelResolver, so a signature drift in
// either type breaks the build immediately rather than rotting silently
// until the (currently unwired) main.go ties them together.
var _ ChannelResolver = (*ytdlp.Runner)(nil)

// channelsPostRequest is the body of POST /api/channels.
type channelsPostRequest struct {
	URL       string `json:"url"`
	Subscribe bool   `json:"subscribe"`
}

// channelsPutRequest is the body of PUT /api/channels/{id}. Pointer fields
// distinguish "omitted" from "explicitly set to the zero value".
type channelsPutRequest struct {
	Autodownload   *bool   `json:"autodownload"`
	FormatOverride *string `json:"format_override"`
}

// channelItem is the JSON shape returned by GET /api/channels: one tracked
// channel, joined with its (optional) subscription state and video counts.
type channelItem struct {
	ID              string `json:"id"`
	Handle          string `json:"handle,omitempty"`
	Name            string `json:"name"`
	Subscribed      bool   `json:"subscribed"`
	Autodownload    bool   `json:"autodownload"`
	FormatOverride  string `json:"format_override,omitempty"`
	PendingCount    int    `json:"pending_count"`
	DownloadedCount int    `json:"downloaded_count"`
}

// channelHandleFromURL extracts the @handle from a pasted channel url, if
// any, trimming any trailing path segment a user's paste often carries
// (e.g. "https://www.youtube.com/@Handle/videos" or "/@Handle/featured").
// Query strings/fragments are stripped the same way. Returns "" if the url
// has no /@ segment or the handle portion is empty.
func channelHandleFromURL(rawURL string) string {
	i := strings.Index(rawURL, "/@")
	if i < 0 {
		return ""
	}
	rest := rawURL[i+2:]
	if j := strings.IndexAny(rest, "/?#"); j >= 0 {
		rest = rest[:j]
	}
	if rest == "" {
		return ""
	}
	return "@" + rest
}

// handleChannelsPost tracks a channel (and optionally subscribes it). Flow:
// canonicalize the pasted url (rejecting anything that is not a channel
// link) → resolve the authoritative UCID via yt-dlp (surfacing a missing/
// invalid cookie as 409) → upsert the channel row → optionally subscribe.
func (s *server) handleChannelsPost(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil || s.channelResolver == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	var req channelsPostRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		writeJSONError(w, http.StatusBadRequest, "url is required")
		return
	}
	channelURL, _, kind, err := ytdlp.Canonicalize(req.URL)
	if err != nil || kind != "channel" {
		writeJSONError(w, http.StatusBadRequest, "Paste a channel link (a /channel/, /@handle, /c/, or /user/ URL)")
		return
	}
	ucid, name, err := s.channelResolver.ResolveChannel(r.Context(), channelURL)
	if err != nil {
		if errors.Is(err, ytdlp.ErrNoCookie) {
			writeJSONError(w, http.StatusConflict, "cookie required")
			return
		}
		writeJSONError(w, http.StatusBadGateway, "resolve channel failed: "+err.Error())
		return
	}
	// ResolveChannel is the authoritative source of the UCID; the handle is
	// best-effort from the pasted url only (never derived from the UCID).
	handle := channelHandleFromURL(req.URL)
	if err := s.channels.Upsert(channels.Channel{ID: ucid, Name: name, Handle: handle}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "track channel failed")
		return
	}
	if req.Subscribe {
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		if err := s.channels.Subscribe(ucid, now); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "subscribe failed")
			return
		}
	}
	// Report the real post-condition, not req.Subscribe. Upsert and Subscribe
	// are both idempotent, so re-adding an ALREADY-subscribed channel with
	// subscribe=false succeeds and leaves the existing subscription intact —
	// echoing the request would tell the caller "not subscribed" about a
	// channel that is subscribed and will keep being scanned.
	subscribed, err := s.channelSubscribed(ucid)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load subscription state failed")
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": ucid, "name": name, "subscribed": subscribed})
}

// channelSubscribed reports whether channelID currently has a subscription
// row. It reuses the List("subscribed") + scan pattern handleChannelsPut
// already relies on rather than adding a store method for one caller.
func (s *server) channelSubscribed(channelID string) (bool, error) {
	items, err := s.channels.List("subscribed")
	if err != nil {
		return false, err
	}
	for i := range items {
		if items[i].ID == channelID {
			return true, nil
		}
	}
	return false, nil
}

// handleChannelsList returns tracked channels, optionally narrowed by the
// ?filter= query param ("all" default, "subscribed", "tracked", or
// "autodownload").
func (s *server) handleChannelsList(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	switch filter {
	case "all", "subscribed", "tracked", "autodownload":
		// valid
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid filter: "+filter)
		return
	}
	items, err := s.channels.List(filter)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list channels failed")
		return
	}
	out := make([]channelItem, 0, len(items))
	for _, it := range items {
		out = append(out, channelItem{
			ID:              it.ID,
			Handle:          it.Handle,
			Name:            it.Name,
			Subscribed:      it.Subscribed,
			Autodownload:    it.Autodownload,
			FormatOverride:  it.FormatOverride,
			PendingCount:    it.PendingCount,
			DownloadedCount: it.DownloadedCount,
		})
	}
	writeJSON(w, out)
}

// handleChannelsPut updates a subscribed channel's autodownload flag and/or
// format override. Only subscribed channels have a config to update; a
// merely-tracked channel yields a clean 400 rather than a silent no-op.
func (s *server) handleChannelsPut(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	var req channelsPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Look up the current config so unset fields are left untouched rather
	// than being reset to the zero value by a partial PUT.
	items, err := s.channels.List("subscribed")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "load channel config failed")
		return
	}
	var current *channels.ListItem
	for i := range items {
		if items[i].ID == id {
			current = &items[i]
			break
		}
	}
	if current == nil {
		writeJSONError(w, http.StatusBadRequest, "channel is not subscribed")
		return
	}

	autodownload := current.Autodownload
	if req.Autodownload != nil {
		autodownload = *req.Autodownload
	}
	formatOverride := current.FormatOverride
	if req.FormatOverride != nil {
		formatOverride = *req.FormatOverride
	}

	ok, err := s.channels.UpdateConfig(id, autodownload, formatOverride)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "update config failed")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "channel is not subscribed")
		return
	}
	writeJSON(w, map[string]any{"id": id, "autodownload": autodownload, "format_override": formatOverride})
}

// handleChannelsSubscribe subscribes an already-tracked channel, scheduling
// its first scan immediately.
func (s *server) handleChannelsSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	c, err := s.channels.Get(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "get channel failed")
		return
	}
	if c == nil {
		writeJSONError(w, http.StatusNotFound, "channel not tracked")
		return
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := s.channels.Subscribe(id, now); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "subscribe failed")
		return
	}
	writeJSON(w, map[string]string{"status": "subscribed"})
}

// handleChannelsUnsubscribe removes a channel's subscription, leaving it
// tracked. 404s if the channel was never subscribed.
func (s *server) handleChannelsUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	ok, err := s.channels.Unsubscribe(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "unsubscribe failed")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "channel not subscribed")
		return
	}
	writeJSON(w, map[string]string{"status": "unsubscribed"})
}

// handleChannelsDelete destructively removes a channel and EVERYTHING
// belonging to it: its subscription, its scan-ledger rows, and all of its
// downloaded videos (their jobs and on-disk media files included) — even
// favorited "Kept forever" ones. This intentionally overrides the Phase-1
// retention invariant for this one explicit, user-confirmed action.
//
// Order matters. Worker.Cancel settles asynchronously, so the steps are:
//  1. Read the video refs BEFORE deleting — once the rows are gone their
//     media paths are unrecoverable.
//  2. Cancel any active (pending/running) jobs for those videos, killing a
//     live download child. The worker's late settle-write is harmless: we
//     delete the rows next, so it hits zero rows.
//  3. Delete the rows (one tx; FK-cascades jobs, subscription, ledger).
//  4. Unlink the media/thumbnail files using the refs captured in step 1.
func (s *server) handleChannelsDelete(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	// 1. Read refs BEFORE deleting (we need media paths after the rows are gone).
	refs, err := s.channels.VideoRefs(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	// 2. Cancel any active jobs for those videos (kills a live child). The
	//    worker settles asynchronously; that's fine — we delete the rows next,
	//    and its late settle-write hits zero rows.
	if s.worker != nil && s.jobs != nil {
		vids := make([]string, len(refs))
		for i, rf := range refs {
			vids[i] = rf.VideoID
		}
		if jobIDs, err := s.jobs.ActiveIDsForVideos(vids); err == nil {
			for _, jid := range jobIDs {
				s.worker.Cancel(jid)
			}
		}
	}
	// 3. Delete rows (FK-cascades jobs, subscription, ledger).
	if err := s.channels.DeleteCascade(id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	// 4. Unlink media/thumbnail files (plus subtitle sidecars) using the refs
	//    captured in step 1, via the same path-safe helper handleDeleteVideo
	//    uses so the two deletion paths can never diverge.
	for _, rf := range refs {
		media.RemoveVideoFiles(s.mediaDir, rf.MediaPath, rf.ThumbnailPath, rf.SubtitlePath)
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

// pendingItem is the JSON shape returned by GET /api/pending: one ledger
// entry awaiting a keep/ignore decision. It has no local media yet — a
// pending item lives only in the channel_videos ledger, never in the videos
// table, so there is no thumbnail_path here, only the remote thumbnail_url.
type pendingItem struct {
	VideoID         string `json:"video_id"`
	ChannelID       string `json:"channel_id"`
	ChannelName     string `json:"channel_name"`
	Title           string `json:"title"`
	DurationSeconds int    `json:"duration_seconds"`
	URL             string `json:"url"`
	ThumbnailURL    string `json:"thumbnail_url"`
}

// handlePendingList returns every ledger entry in state 'pending'. Mirrors
// handleChannelsList's nil-503 behavior: an unconfigured ledger must report
// unavailable, not silently return an empty list (a 200+[] response is
// indistinguishable from "genuinely nothing pending").
func (s *server) handlePendingList(w http.ResponseWriter, r *http.Request) {
	if s.ledger == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "pending is not configured")
		return
	}
	items, err := s.ledger.ListPending()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "list pending failed")
		return
	}
	out := make([]pendingItem, 0, len(items))
	for _, e := range items {
		out = append(out, pendingItem{
			VideoID:         e.VideoID,
			ChannelID:       e.ChannelID,
			ChannelName:     e.ChannelName,
			Title:           e.Title,
			DurationSeconds: e.DurationSeconds,
			URL:             e.URL,
			ThumbnailURL:    e.ThumbnailURL,
		})
	}
	writeJSON(w, out)
}

// handlePendingDownload promotes a pending ledger entry to a real download:
// upsert the videos row from the ledger's metadata (deliberately leaving
// ThumbnailPath empty — the ledger's thumbnail_url is a remote url, not a
// locally-downloaded file path), mark it queued, enqueue a job at the
// standard manual priority, and flip the ledger row out of 'pending' so it
// no longer shows up in the pending list. 404s if the ledger row doesn't
// exist or is no longer pending (e.g. already downloaded or ignored).
func (s *server) handlePendingDownload(w http.ResponseWriter, r *http.Request) {
	if s.ledger == nil || s.videos == nil || s.jobs == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "pending is not configured")
		return
	}
	id := r.PathValue("id")
	e, err := s.ledger.Get(id)
	if err != nil || e == nil || e.State != "pending" {
		writeJSONError(w, http.StatusNotFound, "pending item not found")
		return
	}
	// Format decision: a manual "Download now" from Pending deliberately uses
	// the GLOBAL format preset (RequestedFormat left empty below), NOT the
	// channel's format_override. The per-channel override is an autodownload
	// policy; a manual pick from Pending is a one-off that follows the global
	// preset.
	//
	// If this video was already downloaded while still sitting on the Pending
	// list (e.g. added manually via the video URL), do NOT re-enqueue a
	// duplicate: just clear it from Pending and report it back as already
	// downloaded.
	if v, verr := s.videos.Get(e.VideoID); verr == nil && v != nil && v.Status == "downloaded" {
		if err := s.ledger.SetState(e.VideoID, "queued"); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "update pending failed")
			return
		}
		writeJSON(w, map[string]string{"status": "already_downloaded"})
		return
	}
	if err := s.videos.Upsert(videos.Video{
		ID:              e.VideoID,
		URL:             e.URL,
		Title:           e.Title,
		ChannelID:       e.ChannelID,
		DurationSeconds: int64(e.DurationSeconds),
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save video failed")
		return
	}
	if err := s.videos.SetStatus(e.VideoID, "queued", ""); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "save video failed")
		return
	}
	if _, err := s.jobs.Enqueue(e.VideoID, downloadPriority); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "enqueue failed")
		return
	}
	if err := s.ledger.SetState(e.VideoID, "queued"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "update pending failed")
		return
	}
	writeJSON(w, map[string]string{"status": "queued"})
}

// handlePendingIgnore marks a pending ledger entry as ignored, removing it
// from the pending list without ever creating a videos row. 404s if the
// ledger row doesn't exist.
func (s *server) handlePendingIgnore(w http.ResponseWriter, r *http.Request) {
	if s.ledger == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "pending is not configured")
		return
	}
	id := r.PathValue("id")
	e, err := s.ledger.Get(id)
	if err != nil || e == nil {
		writeJSONError(w, http.StatusNotFound, "pending item not found")
		return
	}
	if err := s.ledger.SetState(id, "ignored"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "ignore failed")
		return
	}
	writeJSON(w, map[string]string{"status": "ignored"})
}
