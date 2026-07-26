package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/channelmeta"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

// resolveCap bounds a channel metadata resolve, measured from the moment yt-dlp
// starts rather than from when the call is entered — both handlers below arm it
// through ytdlp.WithStartHook. Shared by the two so the same work has the same
// bound whichever way a person triggered it.
//
// A var rather than a const only so the tests can shorten it: the production
// value is two minutes, and what needs asserting is which side of the start
// hook the clock runs on, not how long it is.
var resolveCap = 2 * time.Minute

// ChannelResolver is channelmeta.Resolver: the yt-dlp call that turns a
// canonicalized channel url into the channel's identity and metadata. It moved
// to channelmeta when the fetch-and-store step did, since the background
// refresher needs the same interface; the alias stays so the httpapi Deps
// field and its fakes read the same as before.
type ChannelResolver = channelmeta.Resolver

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

// channelItem is the JSON shape returned by GET /api/channels: one listed
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
	// Added is true when the USER added this channel (added_at is set) and false
	// for one that is only listed because the library holds a video downloaded
	// from it. It is the bit the list was missing that the filter pills need:
	// with it the UI can tell "Not subscribed" (added, no subscription) from
	// "From downloads" (never added) off one unfiltered list, instead of asking
	// the server once per pill and getting five snapshots that can disagree.
	// Same predicate the ?filter= clauses use — see channels.Store.List.
	//
	// Named after channelDetail.Added rather than FirstSeenAt below, because it
	// carries the same meaning as the former: the user's own action.
	Added bool `json:"added"`
	// HasAvatar and HasBanner mirror the detail handler's presence flags: the
	// stored path never reaches the browser, so these tell the list row whether
	// to point an <img> at /api/channels/{id}/avatar|banner or fall back to a
	// gradient. The paths are already loaded by channels.Store.List — this just
	// stops dropping them before JSON.
	HasAvatar bool `json:"has_avatar"`
	HasBanner bool `json:"has_banner"`
	// Dormant and LastVideoAt surface channels.Store.List's dormancy
	// columns: Dormant is always present (false for a healthy or
	// unsubscribed channel), LastVideoAt is omitted when the channel has
	// never had a video discovered.
	Dormant     bool   `json:"dormant"`
	LastVideoAt string `json:"last_video_at,omitempty"`
	// FirstSeenAt is when the channel row was first created — what the Channels
	// list's "Recently added" ordering sorts on. channels.Store.List already
	// selects and scans it, so this only stops dropping it before JSON.
	//
	// Deliberately NOT the added_at column, despite the sort's label: a channel
	// listed only because a video was downloaded from it was never added, and
	// sorting on added_at would collapse every one of those to the bottom under
	// an empty value. For a channel the user did add the two are the same
	// instant anyway — the row is created and stamped in one request.
	//
	// The distinct name also keeps this apart from channelDetail.AddedAt, which
	// IS the user-added timestamp. One JSON key meaning two different things
	// across the two DTOs would be a trap.
	FirstSeenAt string `json:"first_seen_at,omitempty"`
}

// autoUnsubscribedItem is the JSON shape returned by GET
// /api/channels/auto-unsubscribed: one channel peeq unsubscribed on its own,
// with the reason and when.
type autoUnsubscribedItem struct {
	ID     string `json:"id"`
	Handle string `json:"handle,omitempty"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
	At     string `json:"at"`
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

// handleChannelsPost adds a channel (and optionally subscribes it). Flow:
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
	// Interactive: someone pasted a url and is watching a spinner, so this call
	// skips the pacer's background queue instead of waiting behind the download
	// worker, the scan scheduler and the metadata refresher.
	info, err := s.channelResolver.ResolveChannel(ytdlp.WithInteractive(r.Context()), channelURL)
	if err != nil {
		if errors.Is(err, ytdlp.ErrNoCookie) {
			writeJSONError(w, http.StatusConflict, "cookie required")
			return
		}
		writeJSONError(w, http.StatusBadGateway, "resolve channel failed: "+err.Error())
		return
	}
	ucid, name := info.UCID, info.Name
	// The pasted url wins for the handle — it is what the user typed and what
	// they expect to see back. yt-dlp's uploader_id is the fallback, which is
	// what gives a /channel/UC... paste (no @handle in it at all) a handle.
	handle := channelHandleFromURL(req.URL)
	if handle == "" {
		handle = info.Handle
	}
	// Images are best-effort: a channel with no banner, or a transient fetch
	// failure, must not prevent the channel from being added.
	avatarPath, err := media.FetchImage(r.Context(), info.AvatarURL, s.mediaDir, ".channels/"+ucid+"/avatar")
	if err != nil {
		slog.Warn("channel avatar fetch failed", "channel_id", ucid, "err", err)
	}
	bannerPath, err := media.FetchImage(r.Context(), info.BannerURL, s.mediaDir, ".channels/"+ucid+"/banner")
	if err != nil {
		slog.Warn("channel banner fetch failed", "channel_id", ucid, "err", err)
	}
	if err := s.channels.SaveResolved(channels.Channel{
		ID:          ucid,
		Name:        name,
		Handle:      handle,
		Description: info.Description,
		AvatarPath:  avatarPath,
		BannerPath:  bannerPath,
		Subscribers: info.Subscribers,
		Verified:    info.Verified,
		ResolvedAt:  time.Now().UTC().Format("2006-01-02 15:04:05"),
	}); err != nil {
		serverError(w, r, err, "adding the channel failed")
		return
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := s.channels.MarkAdded(ucid, now); err != nil {
		serverError(w, r, err, "adding the channel failed")
		return
	}
	if req.Subscribe {
		if err := s.channels.Subscribe(ucid, now); err != nil {
			serverError(w, r, err, "subscribe failed")
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

// handleChannelsList returns the channels worth showing, optionally narrowed
// by the ?filter= query param: "all" (default), "subscribed",
// "notsubscribed", "downloaded" or "autodownload". See channels.Store.List
// for what each one means.
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
	case "all", "subscribed", "notsubscribed", "downloaded", "autodownload":
		// valid
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid filter: "+filter)
		return
	}
	items, err := s.channels.List(filter)
	if err != nil {
		serverError(w, r, err, "list channels failed")
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
			Added:           it.AddedAt != "",
			HasAvatar:       it.AvatarPath != "",
			HasBanner:       it.BannerPath != "",
			Dormant:         it.Dormant,
			LastVideoAt:     it.LastVideoAt,
			FirstSeenAt:     it.FirstSeenAt,
		})
	}
	writeJSON(w, out)
}

// handleChannelsAutoUnsubscribedList returns every channel peeq has
// auto-unsubscribed on its own, most recent first.
func (s *server) handleChannelsAutoUnsubscribedList(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	items, err := s.channels.AutoUnsubscribedList()
	if err != nil {
		serverError(w, r, err, "list auto-unsubscribed failed")
		return
	}
	out := make([]autoUnsubscribedItem, 0, len(items))
	for _, it := range items {
		out = append(out, autoUnsubscribedItem{
			ID:     it.ID,
			Handle: it.Handle,
			Name:   it.Name,
			Reason: it.Reason,
			At:     it.At,
		})
	}
	writeJSON(w, out)
}

// channelDetail is the JSON shape returned by GET /api/channels/{id}. It
// covers both an added channel and one the user has merely visited: Added
// and Subscribed are the flags the page branches on, and the subscription
// fields are zero when Subscribed is false.
type channelDetail struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Handle      string `json:"handle,omitempty"`
	Description string `json:"description,omitempty"`
	HasAvatar   bool   `json:"has_avatar"`
	HasBanner   bool   `json:"has_banner"`

	// What YouTube publishes about the channel, as of the last successful
	// resolve. Subscribers is omitted when unknown — 0 subscribers is not a
	// thing YouTube reports, so a zero here means "hidden or never read" and
	// must not be rendered as a count.
	Subscribers int64 `json:"subscribers,omitempty"`
	Verified    bool  `json:"verified"`
	// ResolvedAt is when metadata was last FETCHED, successfully or not, and
	// ResolveOk says which. The pair is what lets the page distinguish fresh
	// metadata from a failed attempt that has been stuck ever since.
	ResolvedAt string `json:"resolved_at,omitempty"`
	ResolveOk  bool   `json:"resolve_ok"`
	// Gone is set when peeq auto-unsubscribed this channel because YouTube
	// reported it deleted — its most confident "this channel no longer
	// exists", since it takes several consecutive dead scans to record.
	// The channel's videos are untouched by it.
	Gone bool `json:"gone"`

	Added   bool   `json:"added"`
	AddedAt string `json:"added_at,omitempty"`

	ArchivedCount     int    `json:"archived_count"`
	RuntimeSeconds    int64  `json:"runtime_seconds"`
	DiskBytes         int64  `json:"disk_bytes"`
	NewestPublishedAt string `json:"newest_published_at,omitempty"`

	Subscribed     bool   `json:"subscribed"`
	Autodownload   bool   `json:"autodownload"`
	FormatOverride string `json:"format_override,omitempty"`
	LastScannedAt  string `json:"last_scanned_at,omitempty"`
	NextScanAt     string `json:"next_scan_at,omitempty"`
	PendingCount   int    `json:"pending_count"`
}

// handleChannelDetail returns the data behind the channel page: identity,
// the four header stats, and (if added) the subscription/schedule state.
// It serves both an added channel AND one the user never added but whose
// videos live in the library (added by URL) — videos.channel_id has no
// foreign key to channels, so that case is real. 404 is reserved for an id
// that names nothing at all: neither a cached channels row nor any video.
func (s *server) handleChannelDetail(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")

	c, err := s.channels.Get(id)
	if err != nil {
		serverError(w, r, err, "load channel failed")
		return
	}

	// No cached row: fall back to what this channel's own videos say, and
	// find out whether the channel has any videos at all (existence, not
	// downloaded-ness, is what decides the 404 below).
	name := ""
	found := true
	if c != nil {
		name = c.Name
	}
	if c == nil || name == "" {
		var videoName string
		videoName, found, err = s.channels.NameFromVideos(id)
		if err != nil {
			serverError(w, r, err, "load channel failed")
			return
		}
		if name == "" {
			name = videoName
		}
	}
	stats, err := s.channels.Stats(id, name)
	if err != nil {
		serverError(w, r, err, "load channel failed")
		return
	}
	if c == nil && !found {
		writeJSONError(w, http.StatusNotFound, "channel not found")
		return
	}

	out := channelDetail{
		ID:                id,
		Name:              name,
		ArchivedCount:     stats.ArchivedCount,
		RuntimeSeconds:    stats.RuntimeSeconds,
		DiskBytes:         stats.DiskBytes,
		NewestPublishedAt: stats.NewestPublishedAt,
	}
	if c != nil {
		out.Handle = c.Handle
		out.Description = c.Description
		out.HasAvatar = c.AvatarPath != ""
		out.HasBanner = c.BannerPath != ""
		out.Subscribers = c.Subscribers
		out.Verified = c.Verified
		out.ResolvedAt = c.ResolvedAt
		out.ResolveOk = c.ResolveOk
		out.Added = c.AddedAt != ""
		out.AddedAt = c.AddedAt
	}

	// "Gone" is asked for regardless of whether the channel is still added
	// or subscribed: auto-unsubscribe REMOVES the subscription row, so by the
	// time a channel is gone it is exactly the kind of channel the added/
	// subscribed branches below would skip.
	au, aerr := s.channels.AutoUnsubscribeFor(id)
	if aerr != nil {
		serverError(w, r, aerr, "load channel failed")
		return
	}
	out.Gone = au != nil && au.Reason == channels.ReasonDeleted

	if out.Added {
		sub, serr := s.channels.GetSubscription(id)
		if serr != nil {
			serverError(w, r, serr, "load subscription failed")
			return
		}
		if sub != nil {
			out.Subscribed = true
			out.Autodownload = sub.Autodownload
			out.FormatOverride = sub.FormatOverride
			out.LastScannedAt = sub.LastScannedAt
			out.NextScanAt = sub.NextScanAt
		}
		if s.ledger != nil {
			pending, perr := s.ledger.ListPendingForChannel(id)
			if perr != nil {
				serverError(w, r, perr, "load pending failed")
				return
			}
			out.PendingCount = len(pending)
		}
	}

	s.maybeResolveChannel(id, c)
	writeJSON(w, out)
}

// maybeResolveChannel kicks off a one-shot background metadata fetch for a
// channel peeq has never resolved. It deliberately does NOT block the
// response: the page renders from what is already in the database and the
// header fills in on the next load.
//
// resolved_at is written whether the fetch succeeds or fails, so a channel
// that cannot be resolved — a stale cookie, a deleted channel — is not
// re-fetched on every single visit.
//
// The gate reads the row snapshotted before the goroutine launches, so two
// near-simultaneous first visits to the same unresolved channel can both
// fetch. Left as-is deliberately: peeq is single-user, the window is one
// page load wide, and the cost of losing that race is one redundant yt-dlp
// call — not worth a dedup map or a queue.
func (s *server) maybeResolveChannel(channelID string, cached *channels.Channel) {
	if s.metadata == nil {
		return
	}
	if cached != nil && cached.ResolvedAt != "" {
		return
	}
	go func() {
		defer func() {
			// This goroutine parses yt-dlp output and remote HTTP responses,
			// both of which are external input. An unrecovered panic here
			// would take down the whole server, so it is contained the same
			// way every other background worker in peeq contains one.
			if r := recover(); r != nil {
				slog.Error("channel resolve: recovered from panic", "channel_id", channelID, "panic", r)
			}
			if s.onChannelResolved != nil {
				s.onChannelResolved(channelID)
			}
		}()
		// Detached from the request: the browser has its response already.
		// Interactive lane: this fetch is triggered by a real page visit and
		// carries a deadline, so it must skip the background reservation queue —
		// starving behind it is what let the 2-minute timeout expire and strand
		// the channel with resolve_ok=0 (the case #106 is about).
		//
		// The lane made that rarer; the cap starting at the wrong moment is
		// what made it possible at all. WithInteractive skips the background
		// reservation queue but not the throttle, so an interactive call still
		// waits — and a cap armed on entry counted that wait as though yt-dlp
		// were already hung. It runs from the process actually starting now.
		rctx, cancel := context.WithCancel(ytdlp.WithInteractive(context.Background()))
		defer cancel()
		bound := ytdlp.NewDeferredTimer(resolveCap, cancel)
		err := s.metadata.Resolve(ytdlp.WithStartHook(rctx, bound.Start), channelID, cached)
		stoppedInTime := bound.Stop()
		if err != nil {
			// rctx.Err() alongside stoppedInTime: Stop reports false both for a
			// timer that fired and for one never armed, and never-armed is the
			// ordinary case when the call returns before reaching exec. Only
			// the timer cancels rctx, so that is what tells the two apart.
			if !stoppedInTime && rctx.Err() != nil {
				slog.Warn("channel resolve stalled", "channel_id", channelID, "after", resolveCap)
			}
			slog.Warn("channel resolve failed", "channel_id", channelID, "err", err)
		}
	}()
}

// handleChannelRefresh re-reads a channel's metadata from YouTube on demand,
// ignoring the resolved_at gate that maybeResolveChannel obeys. That gate is
// what makes this endpoint necessary: it treats a FAILED resolve as final, so
// a channel whose one attempt failed (no cookie at the time, a network blip
// during an import) keeps its blank avatar, banner and description forever with
// no way back — and for an UNSUBSCRIBED added channel there is no weekly
// rotation to retry it either. This is that way back, and it is deliberately
// manual: nothing re-resolves a failed unsubscribed channel on its own (#106).
//
// It runs while the caller waits rather than in the background: the user
// pressed a button and the answer is either new metadata to re-render or a
// reason it did not work.
func (s *server) handleChannelRefresh(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil || s.metadata == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	c, err := s.channels.Get(id)
	if err != nil {
		serverError(w, r, err, "load channel failed")
		return
	}
	// Same existence rule the detail endpoint applies, and for the same
	// reason: an id that names nothing must not become something. Without
	// this, refreshing a made-up id CREATES a row for it — on the failure
	// path too, since that path writes a bare row to remember the failed
	// attempt — and an id the detail endpoint 404s starts returning 200 with
	// an empty channel behind it.
	if c == nil {
		_, found, nerr := s.channels.NameFromVideos(id)
		if nerr != nil {
			serverError(w, r, nerr, "load channel failed")
			return
		}
		if !found {
			writeJSONError(w, http.StatusNotFound, "channel not found")
			return
		}
	}

	// WithInteractive so this user-initiated refresh skips the background pacer
	// queue. WithoutCancel, not r.Context() straight through: a refresh takes
	// tens of seconds (yt-dlp's throttle, then two image fetches), and
	// cancelling it because the reader closed the tab would land in the FAILURE
	// path, which stamps resolve_ok = 0. The channel would then claim "last
	// refresh failed" — the one state peeq uses to mean "this needs your
	// attention" — because someone navigated away. The work is worth finishing
	// either way; only the response is lost.
	//
	// The cap runs from when yt-dlp starts rather than from here, for the same
	// reason: "yt-dlp's throttle, then two image fetches" says outright that
	// most of the elapsed time can be wait rather than work, and counting the
	// wait against the process lands in that same resolve_ok = 0 path.
	rctx, cancel := context.WithCancel(
		ytdlp.WithInteractive(context.WithoutCancel(r.Context())))
	defer cancel()
	bound := ytdlp.NewDeferredTimer(resolveCap, cancel)
	err = s.metadata.Resolve(ytdlp.WithStartHook(rctx, bound.Start), id, c)
	stoppedInTime := bound.Stop()
	if err != nil {
		if errors.Is(err, ytdlp.ErrNoCookie) {
			writeJSONError(w, http.StatusConflict, "cookie required")
			return
		}
		// A fired cap surfaces as a bare "context canceled", which tells the
		// reader nothing — 504 and a sentence do.
		//
		// rctx.Err() alongside stoppedInTime: Stop reports false both for a
		// timer that fired and for one never armed, and never-armed is the
		// ordinary case for any resolve that failed before reaching exec. Only
		// the timer cancels rctx. Without that term every ordinary resolve
		// failure would be reported as a timeout.
		if !stoppedInTime && rctx.Err() != nil {
			slog.Warn("channel refresh stalled", "channel_id", id, "after", resolveCap)
			writeJSONError(w, http.StatusGatewayTimeout,
				"refresh timed out: YouTube did not answer in "+resolveCap.String())
			return
		}
		writeJSONError(w, http.StatusBadGateway, "refresh failed: "+err.Error())
		return
	}
	// No onChannelResolved here: that hook exists so a test can await the
	// BACKGROUND goroutine (see Deps.OnChannelResolved). This path is
	// synchronous, so the response itself is the signal, and firing it would
	// hand waiting tests a second, unrelated wakeup.
	writeJSON(w, map[string]any{"status": "ok"})
}

// handleChannelsDismissDormant suppresses a channel's dormancy flag until it
// next posts and then goes quiet again. 404s for a channel with no
// subscription (unknown, or added-but-unsubscribed) rather than the
// silent no-op DismissDormant used to return — a 200 there would tell the
// caller its dismissal took effect when nothing was flagged in the first
// place (Task 2 review finding).
func (s *server) handleChannelsDismissDormant(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	ok, err := s.channels.DismissDormant(id, now)
	if err != nil {
		serverError(w, r, err, "dismiss dormant failed")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "channel not subscribed")
		return
	}
	writeJSON(w, map[string]string{"status": "dismissed"})
}

// handleChannelsResubscribe restores a subscription peeq auto-unsubscribed:
// clear the auto-unsubscribe record, THEN subscribe with next_scan_at = now
// so the channel is picked up on the next tick rather than waiting a full
// scan interval. The order matters: a crash between the two steps leaves the
// channel merely added, with its auto-unsubscribe record already cleared —
// a clean state a retried resubscribe finishes correctly. The reverse order
// would risk the opposite: a channel that looks subscribed again while it
// still carries a stale auto-unsubscribe record, which is the confusing
// half-state worth avoiding here. (AutoUnsubscribe's own ON CONFLICT DO
// UPDATE is what keeps a later re-death clean regardless — this ordering is
// only about not leaving a misleading intermediate state visible to a user
// who checks between the two writes.)
//
// It also dismisses any dormancy flag on the fresh subscription row. Without
// this, a channel that was dead for a long time (which is exactly the kind
// of channel that gets auto-unsubscribed) comes back with a last-video-at
// far older than DormantAfter and would show up in the dormant-review band
// INSTANTLY — suggesting the user unsubscribe from the channel they just
// went out of their way to restore. DismissDormant's own re-arm rule (it
// re-flags automatically once a newer discovery arrives and the channel goes
// quiet again) means this is a one-time clean slate, not a permanent
// silencing of real future dormancy.
func (s *server) handleChannelsResubscribe(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	c, err := s.channels.Get(id)
	if err != nil {
		serverError(w, r, err, "get channel failed")
		return
	}
	// A row in channels no longer means the user added the channel — it may
	// be a cache-only row written when the channel page was merely visited.
	// Resubscribing one would conjure a subscription for a channel the user
	// never added, so added_at is what decides.
	if c == nil || c.AddedAt == "" {
		writeJSONError(w, http.StatusNotFound, "channel not added")
		return
	}
	if err := s.channels.ClearAutoUnsubscribe(id); err != nil {
		serverError(w, r, err, "clear auto-unsubscribe failed")
		return
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if err := s.channels.Subscribe(id, now); err != nil {
		serverError(w, r, err, "subscribe failed")
		return
	}
	// Best-effort in the sense that a "not found" result is impossible here
	// (Subscribe above just created the row), but a real error still needs
	// to surface: silently leaving a resubscribed channel dormant-flagged
	// would defeat the whole point of this call.
	if _, err := s.channels.DismissDormant(id, now); err != nil {
		serverError(w, r, err, "dismiss dormant failed")
		return
	}
	writeJSON(w, map[string]string{"status": "subscribed"})
}

// handleChannelsPut updates a subscribed channel's autodownload flag and/or
// format override. Only subscribed channels have a config to update; a
// merely-added channel yields a clean 400 rather than a silent no-op.
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

	autodownload, formatOverride, ok, err := s.channels.UpdateConfig(id, req.Autodownload, req.FormatOverride)
	if err != nil {
		serverError(w, r, err, "update config failed")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "channel is not subscribed")
		return
	}
	writeJSON(w, map[string]any{"id": id, "autodownload": autodownload, "format_override": formatOverride})
}

// handleChannelsSubscribe subscribes a channel, scheduling its first scan
// immediately.
//
// A channel listed only because the library holds a downloaded video from it
// has never been added, and subscriptions.channel_id references channels.id
// with "subscribed implies added" as the standing invariant. Rather than
// 404ing the star on those rows, subscribing adds the channel first — one
// request, so there is no window where the add succeeded and the subscribe
// did not. Genuine cache-only rows (visited, nothing downloaded) still 404:
// nothing about a page visit says the user wants the channel.
func (s *server) handleChannelsSubscribe(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	c, err := s.channels.Get(id)
	if err != nil {
		serverError(w, r, err, "get channel failed")
		return
	}
	if c == nil {
		writeJSONError(w, http.StatusNotFound, "channel not added")
		return
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	if c.AddedAt == "" {
		hasDownloads, err := s.channels.HasDownloads(id)
		if err != nil {
			serverError(w, r, err, "get channel failed")
			return
		}
		if !hasDownloads {
			writeJSONError(w, http.StatusNotFound, "channel not added")
			return
		}
		if err := s.channels.MarkAdded(id, now); err != nil {
			serverError(w, r, err, "adding the channel failed")
			return
		}
	}
	if err := s.channels.Subscribe(id, now); err != nil {
		serverError(w, r, err, "subscribe failed")
		return
	}
	writeJSON(w, map[string]string{"status": "subscribed"})
}

// handleChannelsUnsubscribe removes a channel's subscription, leaving it
// added. 404s if the channel was never subscribed.
func (s *server) handleChannelsUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	ok, err := s.channels.Unsubscribe(id)
	if err != nil {
		serverError(w, r, err, "unsubscribe failed")
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "channel not subscribed")
		return
	}
	writeJSON(w, map[string]string{"status": "unsubscribed"})
}

// handleChannelScan schedules a scan of one channel by moving its
// next_scan_at into the past. The scheduler holds no in-memory schedule — it
// polls ClaimDue(now) — so this single update IS the mechanism, and the scan
// runs on the scheduler's next poll rather than immediately. The UI must say
// "checking soon", never imply the scan is happening this instant.
//
// Two gates in the scheduler's own loop can still delay it indefinitely: an
// invalid YouTube cookie and the global pause flag. When either is set, say
// so rather than reporting a success the user will never see the result of.
func (s *server) handleChannelScan(w http.ResponseWriter, r *http.Request) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	id := r.PathValue("id")
	sub, err := s.channels.GetSubscription(id)
	if err != nil {
		serverError(w, r, err, "schedule scan failed")
		return
	}
	if sub == nil {
		writeJSONError(w, http.StatusBadRequest, "channel is not subscribed")
		return
	}

	if s.settings != nil {
		if paused, reason := s.settings.YoutubePaused(r.Context()); paused {
			msg := "YouTube access is paused"
			if reason != "" {
				msg += ": " + reason
			}
			writeJSON(w, map[string]string{"status": "blocked", "reason": msg})
			return
		}
		// Mirror the scan loop's own cookie gate, AllowAnonymous included: without
		// that escape hatch this endpoint reports "blocked" on a missing cookie
		// while the loop behind it is scanning anonymously without complaint.
		if status := s.settings.CookieStatus(r.Context()); status != "valid" && !s.allowAnonymous {
			writeJSON(w, map[string]string{
				"status": "blocked",
				"reason": "Your YouTube cookie needs refreshing before peeq can check this channel.",
			})
			return
		}
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	// RequestScan, not Backoff: besides pulling the schedule into the past it
	// records that someone is waiting, which is what earns this pass an activity
	// row even when it finds nothing new.
	if err := s.channels.RequestScan(id, now); err != nil {
		serverError(w, r, err, "schedule scan failed")
		return
	}
	writeJSON(w, map[string]string{"status": "scheduled"})
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
	// A cache-only row (visited, never added, nothing downloaded) must not be
	// deletable: DeleteCascade destroys every video belonging to the channel,
	// and that row is one the user has no idea exists.
	//
	// A download-only row is different — it lists under "From downloads" with
	// its own ⋮ menu, so deleting it is a deliberate act on something visible,
	// and refusing would leave it the one row in the list you cannot remove.
	c, err := s.channels.Get(id)
	if err != nil {
		serverError(w, r, err, "delete failed")
		return
	}
	if c == nil {
		writeJSONError(w, http.StatusNotFound, "channel not added")
		return
	}
	if c.AddedAt == "" {
		hasDownloads, herr := s.channels.HasDownloads(id)
		if herr != nil {
			serverError(w, r, herr, "delete failed")
			return
		}
		if !hasDownloads {
			writeJSONError(w, http.StatusNotFound, "channel not added")
			return
		}
	}
	// 1. Read refs BEFORE deleting (we need media paths after the rows are gone).
	refs, rerr := s.channels.VideoRefs(id)
	if rerr != nil {
		serverError(w, r, rerr, "delete failed")
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
		serverError(w, r, err, "delete failed")
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
	// PublishedAt is YYYY-MM-DD, omitted when the scan never learned a date.
	// It is yt-dlp's approximate tab date, not the exact upload_date a
	// downloaded video carries. DiscoveredAt is when the scan first saw the
	// upload; the client sorts on it as a fallback but must not render it as
	// a publish date.
	PublishedAt  string `json:"published_at,omitempty"`
	DiscoveredAt string `json:"discovered_at"`
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
	var items []channelvideos.Entry
	var err error
	if channelID := r.URL.Query().Get("channel"); channelID != "" {
		items, err = s.ledger.ListPendingForChannel(channelID)
	} else {
		items, err = s.ledger.ListPending()
	}
	if err != nil {
		serverError(w, r, err, "list pending failed")
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
			PublishedAt:     e.PublishedAt,
			DiscoveredAt:    e.DiscoveredAt,
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
	if err != nil || e == nil || e.State != channelvideos.StatePending {
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
	if v, verr := s.videos.Get(e.VideoID); verr == nil && v != nil && v.Status == videos.StatusDownloaded {
		if err := s.ledger.SetState(e.VideoID, channelvideos.StateQueued); err != nil {
			serverError(w, r, err, "update pending failed")
			return
		}
		writeJSON(w, map[string]string{"status": "already_downloaded"})
		return
	}
	// The title is normalised again on the way out of the ledger. Entries
	// discovered before title normalisation existed still hold the raw yt-dlp
	// string, and this is where such an entry becomes a video row — the row is
	// new here, so cleaning it stays within "new videos only". The ledger row
	// itself is left as it was.
	if err := s.videos.Upsert(videos.Video{
		ID:              e.VideoID,
		URL:             e.URL,
		Title:           ytdlp.NormalizeTitle(e.Title),
		ChannelID:       e.ChannelID,
		DurationSeconds: int64(e.DurationSeconds),
	}); err != nil {
		serverError(w, r, err, "save video failed")
		return
	}
	if err := s.videos.SetStatus(e.VideoID, videos.StatusQueued, ""); err != nil {
		serverError(w, r, err, "save video failed")
		return
	}
	if _, err := s.jobs.Enqueue(e.VideoID, downloadPriority); err != nil {
		serverError(w, r, err, "enqueue failed")
		return
	}
	if err := s.ledger.SetState(e.VideoID, channelvideos.StateQueued); err != nil {
		serverError(w, r, err, "update pending failed")
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
	if err := s.ledger.SetState(id, channelvideos.StateIgnored); err != nil {
		serverError(w, r, err, "ignore failed")
		return
	}
	writeJSON(w, map[string]string{"status": "ignored"})
}

// handleChannelAvatar and handleChannelBanner serve a cached channel image
// off local disk. Like video thumbnails, the stored path never reaches the
// browser — only these endpoints do — and it is resolved through
// media.SafeMediaPath so a crafted stored value cannot escape the media dir.
func (s *server) handleChannelAvatar(w http.ResponseWriter, r *http.Request) {
	s.serveChannelImage(w, r, func(c *channels.Channel) string { return c.AvatarPath })
}

func (s *server) handleChannelBanner(w http.ResponseWriter, r *http.Request) {
	s.serveChannelImage(w, r, func(c *channels.Channel) string { return c.BannerPath })
}

func (s *server) serveChannelImage(w http.ResponseWriter, r *http.Request, pick func(*channels.Channel) string) {
	if s.channels == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "channels are not configured")
		return
	}
	c, err := s.channels.Get(r.PathValue("id"))
	if err != nil {
		serverError(w, r, err, "load channel failed")
		return
	}
	if c == nil {
		http.NotFound(w, r)
		return
	}
	stored := pick(c)
	if stored == "" {
		http.NotFound(w, r)
		return
	}
	path, err := media.SafeMediaPath(s.mediaDir, stored)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, filepath.Base(path), fi.ModTime(), f)
}
