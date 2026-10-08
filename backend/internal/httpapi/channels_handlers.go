package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/channelmeta"
	"github.com/trick77/peeq/internal/channels"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/store"
	"github.com/trick77/peeq/internal/ytdlp"
	"github.com/trick77/peeq/internal/ytgate"
)

// defaultResolveCap bounds a channel metadata resolve, measured from the
// moment yt-dlp starts rather than from when the call is entered — both
// handlers below arm it through ytdlp.WithStartHook. Shared by the two so the
// same work has the same bound whichever way a person triggered it.
//
// It lives on the server (Deps.ResolveCap, this when zero) rather than in a
// package variable so a test can shorten it for its own server only: the
// background resolve goroutine outlives the request that started it, and a
// package variable one test rewrote while another test's goroutine still read
// it was a data race under -race.
const defaultResolveCap = 2 * time.Minute

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
	// AutoSummary is the odd one out: it lives on channels, not subscriptions,
	// so it is settable on a channel that is merely added. See the handler for
	// why that changes when the "not subscribed" rejection applies.
	AutoSummary *bool `json:"auto_summary"`
	// KeepReads sits on channels beside AutoSummary and behaves identically:
	// whether a read this channel's videos got is kept and indexed for search
	// instead of being discarded when the video is ignored.
	KeepReads *bool `json:"keep_reads"`
}

// channelItem is the JSON shape returned by GET /api/channels: one listed
// channel, joined with its (optional) subscription state and video counts.
type channelItem struct {
	ID           string `json:"id"`
	Handle       string `json:"handle,omitempty"`
	Name         string `json:"name"`
	Subscribed   bool   `json:"subscribed"`
	Autodownload bool   `json:"autodownload"`
	// AutoSummary is whether peeq reads this channel's new videos before the
	// user decides on them. Unlike Autodownload it lives on the channel, not
	// the subscription, so it is meaningful on an unsubscribed row too.
	AutoSummary     bool   `json:"auto_summary"`
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
	// The versions go in the artwork URLs, which is what lets those responses be
	// cached as immutable and still turn over the moment the refresher stores
	// new artwork. Omitted when there is no such image.
	AvatarVersion string `json:"avatar_version,omitempty"`
	BannerVersion string `json:"banner_version,omitempty"`
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
	if !decodeJSON(w, r, &req, maxJSONBody, "url is required") {
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		writeJSONError(w, http.StatusBadRequest, "url is required")
		return
	}
	channelURL, _, kind, err := ytdlp.Canonicalize(req.URL)
	if err != nil || kind != "channel" {
		writeJSONError(w, http.StatusBadRequest, "Paste a channel link (a /channel/, /@handle, /c/, or /user/ URL)")
		return
	}
	// Interactive: someone pasted a url and is watching a spinner, so this call
	// goes ahead of queued scans, caption fetches and downloads. It still waits
	// for the yt-dlp running now to exit (one at a time), which can be a long
	// download.
	info, err := s.channelResolver.ResolveChannel(ytdlp.WithInteractive(r.Context()), channelURL)
	if err != nil {
		if errors.Is(err, ytdlp.ErrNoCookie) {
			writeJSONError(w, http.StatusConflict, "cookie required")
			return
		}
		upstreamError(w, r, err, "resolve channel failed")
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
	if err := s.channels.SaveResolved(channels.Channel{
		ID:          ucid,
		Name:        name,
		Handle:      handle,
		Description: info.Description,
		Subscribers: info.Subscribers,
		Verified:    info.Verified,
		ResolvedAt:  store.FormatTime(time.Now()),
	}); err != nil {
		serverError(w, r, err, "adding the channel failed")
		return
	}
	// Artwork lands in the row rather than the media tree (migration 0023), and
	// only AFTER SaveResolved: channel_images has an FK to channels, so storing
	// first would be rejected for exactly the case this handler is — a channel
	// being added for the first time.
	//
	// Best-effort: a channel with no banner, or a transient fetch failure, must
	// not prevent the channel from being added.
	//
	// In the background: each image is a turn in the YouTube queue, a gap
	// after the resolve above, so fetching both on the request would hold it
	// open for a minute or more after the channel was already added.
	s.storeChannelArtAsync(ucid, info.AvatarURL, info.BannerURL)

	now := store.FormatTime(time.Now())
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
	sub, err := s.channels.GetSubscription(ucid)
	if err != nil {
		serverError(w, r, err, "load subscription state failed")
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"id": ucid, "name": name, "subscribed": sub != nil})
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
			AutoSummary:     it.AutoSummary,
			FormatOverride:  it.FormatOverride,
			PendingCount:    it.PendingCount,
			DownloadedCount: it.DownloadedCount,
			Added:           it.AddedAt != "",
			HasAvatar:       it.HasAvatar,
			HasBanner:       it.HasBanner,
			AvatarVersion:   it.AvatarVersion,
			BannerVersion:   it.BannerVersion,
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
	// See the list DTO: these version the artwork URLs so they can be cached
	// immutably without pinning a refreshed picture.
	AvatarVersion string `json:"avatar_version,omitempty"`
	BannerVersion string `json:"banner_version,omitempty"`

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

	// AutoSummary and KeepReads live on the channel, not the subscription, so
	// they are reported for any cached channel — subscribed or not. They are a
	// pair the settings UI reads together: nothing is kept for a channel that
	// is never read, so the second only means anything while the first is on.
	AutoSummary    bool   `json:"auto_summary"`
	KeepReads      bool   `json:"keep_reads"`
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
		out.HasAvatar = c.HasAvatar
		out.HasBanner = c.HasBanner
		out.AvatarVersion = c.AvatarVersion
		out.BannerVersion = c.BannerVersion
		out.Subscribers = c.Subscribers
		out.Verified = c.Verified
		out.ResolvedAt = c.ResolvedAt
		out.ResolveOk = c.ResolveOk
		out.Added = c.AddedAt != ""
		out.AddedAt = c.AddedAt
		out.AutoSummary = c.AutoSummary
		out.KeepReads = c.KeepReads
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
			n, perr := s.ledger.CountPendingForChannel(id)
			if perr != nil {
				serverError(w, r, perr, "load pending failed")
				return
			}
			out.PendingCount = n
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
		// carries a deadline, so it must go ahead of queued background work —
		// starving behind it is what let the 2-minute timeout expire and strand
		// the channel with resolve_ok=0 (the case #106 is about).
		//
		// The lane made that rarer; the cap starting at the wrong moment is
		// what made it possible at all. WithInteractive goes ahead of queued
		// background work but still waits for the running call and the gap, so
		// it can wait minutes — and a cap armed on entry counted that wait as though yt-dlp
		// were already hung. It runs from the process actually starting now.
		stalled, err := ytdlp.CallWithCap(ytdlp.WithInteractive(context.Background()), s.resolveCap,
			func(c context.Context) error { return s.metadata.Resolve(c, channelID, cached) })
		if err != nil {
			if stalled {
				slog.Warn("channel resolve stalled", "channel_id", channelID, "after", s.resolveCap)
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
// reason it did not work. The artwork follows in the background (it is two
// more turns in the YouTube queue) and shows on the next page load.
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

	// WithInteractive so this user-initiated refresh goes ahead of queued
	// background work. WithoutCancel, not r.Context() straight through: a refresh
	// can take minutes (waiting for the call running now, then the gap), and
	// cancelling it because the reader closed the tab would land in the FAILURE
	// path, which stamps resolve_ok = 0. The channel would then claim "last
	// refresh failed" — the one state peeq uses to mean "this needs your
	// attention" — because someone navigated away. The work is worth finishing
	// either way; only the response is lost.
	//
	// The cap runs from when yt-dlp starts rather than from here, for the same
	// reason: most of the elapsed time can be waiting for a turn rather than
	// work, and counting the wait against the process lands in that same
	// resolve_ok = 0 path.
	//
	// The artwork is not fetched on the request: each image is a turn in the
	// YouTube queue, so it goes to the background, as on channel add.
	var avatarURL, bannerURL string
	deferArt := func(a, b string) { avatarURL, bannerURL = a, b }
	var stalled bool
	stalled, err = ytdlp.CallWithCap(ytdlp.WithInteractive(context.WithoutCancel(r.Context())), s.resolveCap,
		func(cctx context.Context) error {
			return s.metadata.Resolve(channelmeta.WithArtDeferred(cctx, deferArt), id, c)
		})
	if err == nil {
		s.storeChannelArtAsync(id, avatarURL, bannerURL)
	}
	if err != nil {
		if errors.Is(err, ytdlp.ErrNoCookie) {
			writeJSONError(w, http.StatusConflict, "cookie required")
			return
		}
		// A fired cap surfaces as a bare "context canceled", which tells the
		// reader nothing — 504 and a sentence do. See ytdlp.CallWithCap for
		// how a stall is told apart from an ordinary resolve failure.
		if stalled {
			slog.Warn("channel refresh stalled", "channel_id", id, "after", s.resolveCap)
			writeJSONError(w, http.StatusGatewayTimeout,
				"refresh timed out: YouTube did not answer in "+s.resolveCap.String())
			return
		}
		upstreamError(w, r, err, "refresh failed")
		return
	}
	// No onChannelResolved here: this path is synchronous, so the response is
	// the signal. The background art fetch started above fires OnChannelArt.
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
	now := store.FormatTime(time.Now())
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
	now := store.FormatTime(time.Now())
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
	if !decodeJSON(w, r, &req, maxJSONBody, "invalid request body") {
		return
	}

	// format_override holds a preset id or "" (the picker sends nothing else),
	// and rows predating the picker hold a raw yt-dlp selector the download
	// worker still honours. "custom" is the one value that is neither: it is
	// ytdlp.Resolve's escape hatch, meaningful only beside a format string this
	// endpoint has nowhere to put, so it would reach yt-dlp as the literal
	// selector "custom". This route is reachable with an API token, so the read
	// side declining it (ytdlp.IsPreset) does not keep it out of the column.
	if req.FormatOverride != nil && *req.FormatOverride == "custom" {
		writeJSONError(w, http.StatusBadRequest,
			`format_override cannot be "custom"; write a custom format string in settings instead`)
		return
	}

	// The subscription-level fields go first: UpdateConfig is one atomic
	// statement that is REFUSED (not-subscribed) rather than half-applied, so
	// running it before the channel-level writes below means a refused request
	// has written nothing. The other order — the one this handler used to have
	// — saved auto_summary and keep_reads and only then discovered there was no
	// subscription, answering 400 for a change that was half on disk.
	autodownload, formatOverride, ok, err := s.channels.UpdateConfig(id, req.Autodownload, req.FormatOverride)
	if err != nil {
		serverError(w, r, err, "update config failed")
		return
	}
	// "Not subscribed" is only an error for the two fields that live on the
	// subscription. A request that carried nothing but the channel-level
	// switches goes on to do its whole job below; rejecting it here would make
	// those toggles unusable on an added-but-unsubscribed channel.
	if !ok && (req.Autodownload != nil || req.FormatOverride != nil) {
		// A channel that does not exist at all is a 404, as it is for every
		// other write on this route; only a real channel without a
		// subscription is the 400.
		c, err := s.channels.Get(id)
		if err != nil {
			serverError(w, r, err, "load channel failed")
			return
		}
		if c == nil {
			writeJSONError(w, http.StatusNotFound, "channel not found")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "channel is not subscribed")
		return
	}

	// auto_summary is written against a different table. It is a property of
	// the CHANNEL — "do I want peeq to read this channel's videos" — and
	// survives an unsubscribe/resubscribe, so it must not be gated on a
	// subscription row existing.
	autoSummary := false
	if req.AutoSummary != nil {
		v, found, err := s.channels.SetAutoSummary(id, *req.AutoSummary)
		if err != nil {
			serverError(w, r, err, "update config failed")
			return
		}
		if !found {
			writeJSONError(w, http.StatusNotFound, "channel not found")
			return
		}
		autoSummary = v
	}

	// keep_reads is auto_summary's downstream half — whether the reading is kept
	// and indexed for search once it has served the Inbox — and lives on the
	// same table for the same reason, so it is written the same way.
	keepReads := false
	if req.KeepReads != nil {
		v, found, err := s.channels.SetKeepReads(id, *req.KeepReads)
		if err != nil {
			serverError(w, r, err, "update config failed")
			return
		}
		if !found {
			writeJSONError(w, http.StatusNotFound, "channel not found")
			return
		}
		keepReads = v
	}

	// Whatever this request did not set, report as stored rather than as the
	// zero value a caller would otherwise read as "it just got turned off". One
	// read serves both fields and both responses below. A failed read is a
	// 500 even though every write above succeeded: answering 200 with made-up
	// values would tell the caller its switches had just been turned off.
	if req.AutoSummary == nil || req.KeepReads == nil {
		c, err := s.channels.Get(id)
		if err != nil {
			serverError(w, r, err, "load channel failed")
			return
		}
		if c != nil {
			if req.AutoSummary == nil {
				autoSummary = c.AutoSummary
			}
			if req.KeepReads == nil {
				keepReads = c.KeepReads
			}
		}
	}

	if !ok {
		writeJSON(w, map[string]any{"id": id, "auto_summary": autoSummary, "keep_reads": keepReads})
		return
	}
	writeJSON(w, map[string]any{
		"id": id, "autodownload": autodownload,
		"format_override": formatOverride, "auto_summary": autoSummary,
		"keep_reads": keepReads,
	})
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
	now := store.FormatTime(time.Now())
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
		paused, reason, err := s.settings.YoutubePaused(r.Context())
		if err != nil {
			serverError(w, r, err, "read youtube pause state failed")
			return
		}
		if paused {
			msg := "YouTube access is paused"
			if reason != "" {
				msg += ": " + reason
			}
			writeJSON(w, map[string]string{"status": "blocked", "reason": msg})
			return
		}
		// The scan loop's own cookie rule (ytgate.CookieAllows), escape hatch
		// included: a rule of its own here drifted from the loop's, so this
		// endpoint said "blocked" for a channel the loop was scanning.
		if !ytgate.CookieAllows(s.settings.CookieStatus(r.Context()), s.allowAnonymous) {
			writeJSON(w, map[string]string{
				"status": "blocked",
				"reason": "Your YouTube cookie needs refreshing before Peeq can scan this channel.",
			})
			return
		}
	}

	now := store.FormatTime(time.Now())
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
		// Nothing has been deleted yet, so failing here is safe — and
		// necessary: a running yt-dlp child must not outlive the rows it is
		// downloading for.
		jobIDs, err := s.jobs.ActiveIDsForVideos(vids)
		if err != nil {
			serverError(w, r, err, "delete failed")
			return
		}
		for _, jid := range jobIDs {
			s.worker.Cancel(jid)
		}
	}
	// 3. Delete rows (FK-cascades jobs, subscription, ledger).
	if err := s.channels.DeleteCascade(id); err != nil {
		serverError(w, r, err, "delete failed")
		return
	}
	// 4. Unlink each video's file — and whatever a pre-migration library still
	//    has beside it — using the refs captured in step 1. Everything else the
	//    videos owned went with their rows on the cascade above.
	for _, rf := range refs {
		media.RemoveVideoFiles(s.mediaDir, rf.MediaPath)
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}
