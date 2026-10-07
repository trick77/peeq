package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/videos"
	"github.com/trick77/peeq/internal/ytdlp"
)

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
	// SummaryStatus is the video's summary_status, or "" when peeq has not
	// created a row for it yet. AutoSummary is whether the channel is opted in
	// to being read at all. The card needs both: "" means "not read yet" on an
	// opted-in channel and "never will be" on an opted-out one, and those are
	// different cards.
	SummaryStatus string `json:"summary_status"`
	AutoSummary   bool   `json:"auto_summary"`
	// SummaryGaveUp is whether the latest summary job for this video spent every
	// attempt. SummaryStatus cannot stand in for it: 'error' is written on every
	// summary failure, retryable or not, so it says the last attempt failed and
	// nothing about whether another is coming — and the ladder now waits 15m then
	// 4h, which is hours of a card looking abandoned while the queue still has it.
	SummaryGaveUp bool `json:"summary_gave_up"`
	// HasSubtitles is whether captions are on disk. The card needs it because
	// 'no_transcript' means two things: no captions exist, or the ones that do
	// turned out to be music. Only the second leaves something to read, and
	// only a card that knows which it is can offer the right thing.
	HasSubtitles bool `json:"has_subtitles"`
	// ThumbnailVersion versions the poster URL when one has already been cached,
	// so the card can be served an immutable copy. Omitted when nothing is cached
	// yet — which is not "there is no poster": the endpoint fetches one on
	// demand, so the card asks either way and simply gets the revalidating
	// response on the unversioned URL until a poster lands.
	ThumbnailVersion string `json:"thumbnail_version,omitempty"`
}

// handlePendingCount answers the Inbox badge with just the number. The
// shell refreshes it on every activity event and used to download the
// whole pending list — thumbnails' metadata included — to take its length.
func (s *server) handlePendingCount(w http.ResponseWriter, r *http.Request) {
	if s.ledger == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "pending is not configured")
		return
	}
	n, err := s.ledger.CountPending()
	if err != nil {
		serverError(w, r, err, "count pending failed")
		return
	}
	writeJSON(w, map[string]int{"count": n})
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
			VideoID:          e.VideoID,
			ChannelID:        e.ChannelID,
			ChannelName:      e.ChannelName,
			Title:            e.Title,
			DurationSeconds:  e.DurationSeconds,
			URL:              e.URL,
			ThumbnailURL:     e.ThumbnailURL,
			PublishedAt:      e.PublishedAt,
			DiscoveredAt:     e.DiscoveredAt,
			SummaryStatus:    e.SummaryStatus,
			AutoSummary:      e.AutoSummary,
			SummaryGaveUp:    e.SummaryGaveUp,
			HasSubtitles:     e.HasSubtitles,
			ThumbnailVersion: e.ThumbnailVersion,
		})
	}
	writeJSON(w, out)
}

// handlePendingDownload promotes a pending ledger entry to a real download:
// upsert the videos row from the ledger's metadata (deliberately leaving
// ThumbnailPath empty — the ledger's thumbnail_url is a remote url, not a
// locally-downloaded file path), mark it queued, enqueue a job at the
// standard manual priority, and flip the ledger row out of 'pending' so it
// no longer shows up in the pending list. 404s if the ledger row doesn't exist
// or is in a state no page offers this action from.
//
// 'ignored' is accepted alongside 'pending', because a kept read is reachable
// from Search long after it left the Inbox and the page it opens still offers
// Download. Changing one's mind about a video is not the thing 'ignored' is
// terminal FOR: what stops a video being read a second time is Ledger.Exists
// matching on video_id whatever the state, and the row lands on 'queued' here
// either way. Refusing would leave a button that can only ever fail on the one
// page this feature exists to make reachable.
func (s *server) handlePendingDownload(w http.ResponseWriter, r *http.Request) {
	if s.ledger == nil || s.videos == nil || s.jobs == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "pending is not configured")
		return
	}
	id := r.PathValue("id")
	e, err := s.ledger.Get(id)
	if err != nil {
		serverError(w, r, err, "load pending failed")
		return
	}
	if e == nil || (e.State != channelvideos.StatePending && e.State != channelvideos.StateIgnored) {
		writeJSONError(w, http.StatusNotFound, "pending item not found")
		return
	}
	// Format decision: a manual "Download now" from Pending deliberately uses
	// the GLOBAL format preset (RequestedFormat left empty below), NOT the
	// channel's format_override. The per-channel override is an autodownload
	// policy; a manual pick from Pending is a one-off that follows the global
	// preset.
	//
	// If this video is already in the pipeline while still sitting on the
	// Pending list (queued or downloading after an add by URL or from the
	// extension, or downloaded outright), do NOT enqueue a duplicate: just
	// clear it from Pending and report where it is. The queued/downloading
	// half of this guard is also what makes a retry of THIS request safe: for
	// an 'ignored' row the ledger write below runs after the enqueue has
	// committed, and a retry after that write failed lands here.
	v, err := s.videos.Get(e.VideoID)
	if err != nil {
		// A store fault must not read as "not in the pipeline": that path
		// would overwrite the row with 'queued' and enqueue a duplicate.
		serverError(w, r, err, "load video failed")
		return
	}
	if v != nil && alreadyInQueue(v.Status) {
		if err := s.ledger.SetState(e.VideoID, channelvideos.StateQueued); err != nil {
			serverError(w, r, err, "update pending failed")
			return
		}
		s.removePendingThumbnail(e.VideoID)
		status := "queued"
		if v.Status == videos.StatusDownloaded {
			status = "already_downloaded"
		}
		writeJSON(w, map[string]string{"status": status})
		return
	}
	// The title is normalised again on the way out of the ledger. Entries
	// discovered before title normalisation existed still hold the raw yt-dlp
	// string, and this is where such an entry becomes a video row — the row is
	// new here, so cleaning it stays within "new videos only". The ledger row
	// itself is left as it was.
	// Row, status and job in one transaction (videos.Store.EnqueueDownload);
	// a pending ledger row moves to 'queued' inside it. An 'ignored' row is
	// left alone by the transaction on purpose (it is a user decision) and
	// is moved separately below.
	if _, err := s.videos.UpsertAndEnqueueDownload(videos.Video{
		ID:              e.VideoID,
		URL:             e.URL,
		Title:           ytdlp.NormalizeTitle(e.Title),
		ChannelID:       e.ChannelID,
		DurationSeconds: int64(e.DurationSeconds),
	}, downloadPriority); err != nil {
		serverError(w, r, err, "enqueue failed")
		return
	}
	if e.State == channelvideos.StateIgnored {
		if err := s.ledger.SetState(e.VideoID, channelvideos.StateQueued); err != nil {
			serverError(w, r, err, "update pending failed")
			return
		}
	}
	s.removePendingThumbnail(e.VideoID)
	writeJSON(w, map[string]string{"status": "queued"})
}

// handlePendingIgnore marks a pending ledger entry as ignored, removing it
// from the pending list. 404s if the ledger row doesn't exist.
//
// By default it also throws away whatever reading the video: the videos row
// created to hold its summary, and the transcript with it. Ignoring a video
// means it is gone, not archived — and the ledger row staying 'ignored' is what
// makes sure it never comes back to be read a second time.
//
// The exception is a video that was indexed for search, which only happens on a
// channel whose keep_reads is on (migration 0026). There the reading is the
// point: the row stays, unreachable from every list but findable in Search, and
// its poster stays with it because the summary page and the search card both
// render one.
//
// The deletion is narrowly guarded either way (see dropInboxRead): only a row
// that is still StatusNew AND whose transcript came from a caption fetch is
// ever touched. A video the user downloaded and then somehow ignored, or one
// whose download was cancelled back to 'new', keeps everything.
func (s *server) handlePendingIgnore(w http.ResponseWriter, r *http.Request) {
	if s.ledger == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "pending is not configured")
		return
	}
	id := r.PathValue("id")
	e, err := s.ledger.Get(id)
	if err != nil {
		serverError(w, r, err, "load pending failed")
		return
	}
	if e == nil {
		writeJSONError(w, http.StatusNotFound, "pending item not found")
		return
	}
	if err := s.ledger.SetState(id, channelvideos.StateIgnored); err != nil {
		serverError(w, r, err, "ignore failed")
		return
	}
	// The item just left the inbox, so its cached thumbnail is dead weight —
	// unless the read was kept, in which case that poster is the only picture
	// the video has and both the summary page and its search results ask for it.
	//
	// Only an entry that was still 'pending' is leaving the inbox here, and that
	// is what the reclaim is gated on. A kept read is reachable from Search, and
	// its page still offers Ignore: pressing it arrives with the ledger row
	// already 'ignored', which makes dropInboxRead decline at its first guard
	// (state is not pending) and would otherwise delete the very poster the
	// first ignore deliberately spared. Nothing is lost by the gate — 'queued'
	// reclaims at its own transition, a scan's 'seen' baseline never fetched a
	// poster, and a repeat ignore of a discarded read deletes what is gone.
	if e.State == channelvideos.StatePending && !s.dropInboxRead(r, e, id) {
		s.removePendingThumbnail(id)
	}
	writeJSON(w, map[string]string{"status": "ignored"})
}

// dropInboxRead throws away everything peeq learned by reading a video: the
// videos row holding the summary, and the transcript that goes with it on the
// cascade.
//
// It reports whether a row was deliberately KEPT — not whether it deleted
// something. Those are different questions, and the caller is asking the first
// one: a ledger row with no videos row at all deletes nothing and still wants
// its cached poster reclaimed, while a kept row is the one case where that
// poster is the only picture the video has left.
//
// Best-effort — an ignore must succeed whether or not the cleanup does, since
// the ledger row is already 'ignored' and that is what keeps the video out of
// the Inbox.
//
// The guard is the ledger's own state, and it is stronger than anything on the
// videos row. StatusNew cannot carry this on its own: it is the videos.status
// column DEFAULT, and the download worker deliberately returns a CANCELLED
// download to 'new'. But a cancelled download's LEDGER row sits at 'queued',
// never 'pending' — approving a video flips it and nothing flips it back — so
// "this row was still awaiting a decision" is exactly the set of videos that
// were only ever read, and nothing else.
//
// That also catches the case a subtitle_path check alone would miss: a video
// whose captions never arrived has a row (created before the fetch, so the
// ladder had somewhere to record no_transcript) and no .vtt at all. Keyed only
// on the path, that row would survive every ignore forever, invisible to every
// list and reachable by nothing.
//
// The path is still checked, for the one case the ledger state does not
// separate. A video added by URL and downloaded while its ledger row was never
// decided is 'pending' with a real transcript; cancel that download and it is
// also 'new'. So the rule is: still awaiting a decision, recorded but not
// requested, AND holding either nothing or a caption this feature fetched.
// Anything that has ever been downloaded fails the last clause, because
// SetDownloaded repoints subtitle_path into the media directory.
//
// One kind of read is then spared: one that made it into the search index. That
// is asked of the index itself rather than of the channel's keep_reads switch,
// deliberately. The switch says what to do with FUTURE reads and can be flipped
// at any time; the index is what a delete would actually destroy. Re-reading
// the switch here would make turning it off silently take away results the user
// can neither see going nor explain afterwards.
func (s *server) dropInboxRead(r *http.Request, e *channelvideos.Entry, id string) (kept bool) {
	if s.videos == nil || id == "" || e == nil || e.State != channelvideos.StatePending {
		return false
	}
	v, err := s.videos.Get(id)
	if err != nil || v == nil || v.Status != videos.StatusNew {
		return false
	}
	// Only a row whose transcript came from a caption READ may be discarded: a
	// downloaded video's analysis is not this endpoint's to throw away. Since
	// migration 0023 that is a recorded fact rather than a guess at a path
	// prefix.
	source, serr := s.videos.TranscriptSource(id)
	if serr != nil {
		slog.WarnContext(r.Context(), "ignore: read transcript source failed", "video_id", id, "err", serr)
		return false
	}
	if source != "" && source != videos.TranscriptSourceCaption {
		return false
	}
	// An indexed read is kept: it is searchable, and this endpoint is the only
	// thing that would ever remove it. A failure to find out falls through to
	// the discard — the same answer this code gave before there was anything to
	// ask.
	if s.rag != nil {
		indexed, ierr := s.rag.HasChunks(r.Context(), id)
		if ierr != nil {
			slog.WarnContext(r.Context(), "ignore: read chunk presence failed", "video_id", id, "err", ierr)
		} else if indexed {
			return true
		}
	}
	// Discard takes the row, and the transcript goes with it on the cascade.
	if err := s.videos.Discard(id); err != nil {
		slog.WarnContext(r.Context(), "ignore: discard inbox video row failed", "video_id", id, "err", err)
	}
	return false
}

// removePendingThumbnail drops a pending video's cached poster. Best-effort: it
// is called when the item leaves the inbox (ignored, or promoted to a real
// download that will write its own poster), so leaving the cache behind would
// only accumulate. A failure here must never fail the request that triggered it.
//
// This is the PRIMARY reclaim path, not a nicety. The ledger row survives every
// one of those transitions — only its state flips — so the ON DELETE CASCADE on
// pending_thumbnails never fires here; the cascade covers channel deletion.
func (s *server) removePendingThumbnail(id string) {
	if s.ledger == nil || id == "" {
		return
	}
	if err := s.ledger.DeleteThumbnail(id); err != nil {
		slog.Warn("remove pending thumbnail failed", "video_id", id, "err", err)
	}
}

// handlePendingThumbnail serves a pending (inbox) video's poster from its
// ledger row, and queues a background fetch for one it does not have yet.
// This is what keeps an inbox card from loading i.ytimg.com directly in the
// browser, and — via the hqdefault fallback and the UI's gradient placeholder
// on a 404 — from ever showing a broken-image glyph. See
// media.FetchPendingThumbnail.
func (s *server) handlePendingThumbnail(w http.ResponseWriter, r *http.Request) {
	if s.ledger == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "pending is not configured")
		return
	}
	id := r.PathValue("id")
	e, err := s.ledger.Get(id)
	if err != nil {
		serverError(w, r, err, "load pending failed")
		return
	}
	if e == nil {
		notFoundCached(w, r)
		return
	}
	t, err := s.ledger.GetThumbnail(id)
	if err != nil {
		serverError(w, r, err, "load pending thumbnail failed")
		return
	}
	if t == nil {
		// Only 'pending' items appear in the inbox. Gating the FETCH on state
		// stops a request for a decided item (ignored/queued/seen) from driving
		// an outbound fetch and re-creating a cache that leaving the inbox
		// deliberately removed.
		//
		// The gate is here rather than on the whole request because an ignored
		// video whose reading was kept still has a poster, and this is the only
		// endpoint that serves it: the videos row has no poster of its own, so
		// the summary page and its search results both ask here. Serving bytes
		// that are already stored fetches nothing and re-creates nothing.
		if e.State != channelvideos.StatePending {
			notFoundCached(w, r)
			return
		}
		// Not cached yet. The scan prefetches these, so this is the fill-in for
		// an item the prefetch missed or lost — queued, not fetched here: every
		// poster is a turn in the YouTube queue, and a page of uncached cards
		// would hold a request open per card for as long as that takes. The UI
		// renders its gradient placeholder on the uncached 404 meanwhile, and
		// the poster shows on the next page load after it has arrived.
		if s.queueThumb != nil {
			s.queueThumb(id, e.ThumbnailURL)
		}
		notFoundPending(w, r)
		return
	}
	imageOwnedDay.apply(w, r)
	serveStoredImage(w, r, t.Mime, t.Bytes, t.UpdatedAt)
}
