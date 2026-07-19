package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/trick77/vark/internal/channels"
	"github.com/trick77/vark/internal/ytdlp"
)

// ChannelResolver resolves a canonicalized channel url to its authoritative
// UCID and display name via yt-dlp. Declaring it here (rather than depending
// on the concrete *ytdlp.Runner type) keeps the handler testable with a fake
// that never shells out to yt-dlp; the real *ytdlp.Runner satisfies it.
type ChannelResolver interface {
	ResolveChannel(ctx context.Context, url string) (ucid, name string, err error)
}

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
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": ucid, "name": name, "subscribed": req.Subscribe})
}

// handleChannelsList returns tracked channels, optionally narrowed by the
// ?filter= query param ("all" default, "subscribed", or "tracked").
func (s *server) handleChannelsList(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSON(w, []channelItem{})
		return
	}
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	items, err := s.channels.List(filter)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "list channels failed: "+err.Error())
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
