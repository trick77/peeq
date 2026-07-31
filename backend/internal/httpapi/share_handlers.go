package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/sharelink"
	"github.com/trick77/peeq/internal/videos"
)

// ShareLinkStore is the slice of sharelink.Store the share endpoints use —
// declared here at the consumer, following DownloadsRunner and PlaybackStore, so
// the error branches can be driven by a fake instead of by dropping share_links
// out from under a real store mid-test. *sharelink.Store satisfies it, and as
// with playback the interface happens to cover the store's whole surface.
//
// The same typed-nil caveat as PlaybackStore applies: the handlers nil-check the
// INTERFACE, so a (*sharelink.Store)(nil) placed in it would panic rather than
// produce the documented 503 (owner routes) or 404 (public routes).
// sharelink.New never returns nil.
type ShareLinkStore interface {
	Upsert(ctx context.Context, videoID string, ttl time.Duration) (sharelink.Link, error)
	Resolve(ctx context.Context, token string) (videoID string, ok bool, err error)
	GetByVideo(ctx context.Context, videoID string) (*sharelink.Link, error)
	DeleteByVideo(ctx context.Context, videoID string) error
}

// shareStatusResponse is what the owner-facing share endpoints return: whether
// a live link exists and, if so, the copyable URL, the raw token, and when it
// expires ("" = never). POST (create/replace) and GET (status) share this shape.
type shareStatusResponse struct {
	Shared    bool   `json:"shared"`
	URL       string `json:"url,omitempty"`
	Token     string `json:"token,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// publicVideoDTO is the trimmed video shape served to an unauthenticated share
// viewer. It deliberately omits everything owner-only — media_path (never
// exposed anywhere), watched/favorite/category/status, the resume position — and
// carries only what the public page renders: the video, its summary, chapters
// and highlights, whether a thumbnail and captions exist, and when the link
// expires.
//
// It also omits the video's ID and URL, and that omission is load-bearing rather
// than incidental: peeq's video id IS the YouTube id, so shipping either one
// would name the source video to a recipient who was handed a link to a single
// video, and would hand the public page an identifier it could aim at the
// session-gated /api/videos/{id}/... routes. The share token is the only public
// identifier — share_links maps it to the video id server-side, and that mapping
// never crosses the wire. TestShare_publicVideoNeverLeaksVideoID guards this.
type publicVideoDTO struct {
	Title           string          `json:"title"`
	ChannelName     string          `json:"channel_name"`
	DurationSeconds int64           `json:"duration_seconds,omitempty"`
	Summary         string          `json:"summary"`
	SummaryStatus   string          `json:"summary_status"`
	Chapters        json.RawMessage `json:"chapters,omitempty"`
	KeyPoints       json.RawMessage `json:"key_points,omitempty"`
	HasThumbnail    bool            `json:"has_thumbnail"`
	// ThumbnailVersion is a unix stamp, exactly as on the owner's DTO. It names
	// nothing about the video — the token stays the only public identifier — and
	// it buys the public page the same immutable poster cache the app gets.
	ThumbnailVersion string `json:"thumbnail_version,omitempty"`
	HasSubtitles     bool   `json:"has_subtitles"`
	AudioLanguage    string `json:"audio_language"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	// SponsorblockSegments carries the same crowd-sourced segment list the owner's
	// player gets, so the public page can skip ads and draw the bands. It names
	// nothing about the video — a category, a start and an end — and it is public
	// data on SponsorBlock's side to begin with, so it does not widen what a
	// recipient learns. Absent (not []) when the video has none, matching
	// parseSponsorblockSegments' nil + omitempty contract in videos_handlers.go.
	SponsorblockSegments []sponsorblockSegmentDTO `json:"sponsorblock_segments,omitempty"`
}

// shareTTLs maps the fixed set of lifetimes the UI offers to a duration. The
// empty string and "never" mean the link never expires (zero duration). Any
// other value is rejected — the API accepts only the presets the popover shows.
var shareTTLs = map[string]time.Duration{
	"":      0,
	"never": 0,
	"24h":   24 * time.Hour,
	"7d":    7 * 24 * time.Hour,
	"30d":   30 * 24 * time.Hour,
}

// shareURL builds the absolute link handed to a recipient. It prefers the
// configured external base (BACKEND_PUBLIC_URL); with none set (typical in dev)
// it returns the relative /s/<token> path and lets the browser resolve it
// against the origin the owner is already on.
func (s *server) shareURL(token string) string {
	path := "/s/" + token
	if s.publicURL == "" {
		return path
	}
	return strings.TrimRight(s.publicURL, "/") + path
}

// handleCreateShare creates or updates the share link for a video. The body is
// {"ttl": "24h"|"7d"|"30d"|"never"|""}. Re-sharing a video that already has a
// LIVE link keeps that link's token and only re-stamps its expiry — so an owner
// adjusting the lifetime doesn't invalidate a link already handed out — and a
// new token is minted only when there is no live link. To kill a link
// immediately (e.g. after a leak) use DELETE / Stop sharing, not a re-share.
func (s *server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	if s.shareLinks == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "sharing is not configured")
		return
	}
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	var req struct {
		TTL string `json:"ttl"`
	}
	// An empty body is allowed and means the default (never expires).
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	ttl, known := shareTTLs[req.TTL]
	if !known {
		writeJSONError(w, http.StatusBadRequest, "ttl must be one of 24h, 7d, 30d, never")
		return
	}
	link, err := s.shareLinks.Upsert(r.Context(), v.ID, ttl)
	if err != nil {
		serverError(w, r, err, "create share link failed")
		return
	}
	writeJSON(w, shareStatusResponse{
		Shared:    true,
		URL:       s.shareURL(link.Token),
		Token:     link.Token,
		ExpiresAt: link.ExpiresAt,
	})
}

// handleGetShare reports whether a video currently has a live share link, so
// the player can show the "Shared · N days left" chip and the popover can
// redisplay the link for copying.
func (s *server) handleGetShare(w http.ResponseWriter, r *http.Request) {
	if s.shareLinks == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "sharing is not configured")
		return
	}
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	link, err := s.shareLinks.GetByVideo(r.Context(), v.ID)
	if err != nil {
		serverError(w, r, err, "get share link failed")
		return
	}
	if link == nil {
		writeJSON(w, shareStatusResponse{Shared: false})
		return
	}
	writeJSON(w, shareStatusResponse{
		Shared:    true,
		URL:       s.shareURL(link.Token),
		Token:     link.Token,
		ExpiresAt: link.ExpiresAt,
	})
}

// handleDeleteShare turns off sharing for a video ("Stop sharing"). Idempotent:
// stopping a video that isn't shared still succeeds.
func (s *server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	if s.shareLinks == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "sharing is not configured")
		return
	}
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	if err := s.shareLinks.DeleteByVideo(r.Context(), v.ID); err != nil {
		serverError(w, r, err, "stop sharing failed")
		return
	}
	writeJSON(w, shareStatusResponse{Shared: false})
}

// resolveShare turns a /s/{token} path token into the video it grants access
// to. It is the single gate on every public share route: an unknown, expired,
// revoked, or unconfigured token all produce the SAME 404 with no body detail,
// so a dead link is indistinguishable from one that never existed and the
// existence of any given video is never revealed. Returns nil when it has
// already written the 404 (the caller must return immediately).
func (s *server) resolveShare(w http.ResponseWriter, r *http.Request) *videos.Video {
	if s.shareLinks == nil || s.videos == nil {
		http.NotFound(w, r)
		return nil
	}
	token := r.PathValue("token")
	videoID, ok, err := s.shareLinks.Resolve(r.Context(), token)
	if err != nil {
		// A DB fault is ours, not the caller's: answer 500. That is not an
		// existence signal (a healthy server never 500s here), so it still
		// doesn't leak whether the token is valid — unlike a 200/404 split.
		serverError(w, r, err, "resolve share link failed")
		return nil
	}
	if !ok {
		http.NotFound(w, r)
		return nil
	}
	v, err := s.videos.Get(videoID)
	if err != nil {
		serverError(w, r, err, "get shared video failed")
		return nil
	}
	if v == nil {
		http.NotFound(w, r)
		return nil
	}
	return v
}

// handleShareVideo serves the public, trimmed video metadata for a share token.
func (s *server) handleShareVideo(w http.ResponseWriter, r *http.Request) {
	v := s.resolveShare(w, r)
	if v == nil {
		return
	}
	// Re-read the link only to surface its expiry on the page footer. A miss
	// here (link vanished between resolve and now) is harmless — omit it.
	var expiresAt string
	if link, err := s.shareLinks.GetByVideo(r.Context(), v.ID); err == nil && link != nil {
		expiresAt = link.ExpiresAt
	}
	writeJSON(w, publicVideoDTO{
		Title:            v.Title,
		ChannelName:      v.ChannelName,
		DurationSeconds:  v.DurationSeconds,
		Summary:          v.Summary,
		SummaryStatus:    v.SummaryStatus,
		Chapters:         rawJSONOrNil(v.Chapters),
		KeyPoints:        rawJSONOrNil(v.KeyPoints),
		HasThumbnail:     v.HasThumbnail,
		ThumbnailVersion: v.ThumbnailVersion,
		HasSubtitles:     v.HasTranscript,
		AudioLanguage:    v.AudioLanguage,
		ExpiresAt:        expiresAt,

		SponsorblockSegments: parseSponsorblockSegments(v.SponsorblockSegments),
	})
}

// handleShareStream streams the shared video's media file. It reuses the exact
// safe-path + ServeContent path the authenticated stream endpoint uses (Range
// requests for seeking, conditional requests), but never honors ?download=1 —
// the media file is watch-only, and it never leaves as a file. That restriction
// is about the video specifically: the public page does let a recipient save the
// captions (.txt/.vtt), which are text this same route family already serves
// inline for the <track> element.
func (s *server) handleShareStream(w http.ResponseWriter, r *http.Request) {
	v := s.resolveShare(w, r)
	if v == nil {
		return
	}
	s.serveMediaFile(w, r, v.MediaPath, "")
}

// handleShareThumbnail serves the shared video's poster image — from the
// database, exactly like the library endpoint, so the public page and the app
// can never disagree about whether a video has a poster.
func (s *server) handleShareThumbnail(w http.ResponseWriter, r *http.Request) {
	v := s.resolveShare(w, r)
	if v == nil {
		return
	}
	serveThumbnail(w, r, s.videos, v.ID, s.shareImagePolicy(r, v.ID))
}

// handleShareSubtitles serves the shared video's VTT captions. The public page
// reads this route twice over: once as the <track> element's source, and once
// via fetch to parse into the searchable transcript panel. Its ".vtt" download
// is the browser saving this same response — no attachment disposition here, and
// none needed.
func (s *server) handleShareSubtitles(w http.ResponseWriter, r *http.Request) {
	v := s.resolveShare(w, r)
	if v == nil {
		return
	}
	serveTranscript(w, r, s, v.ID)
}

// serveMediaFile resolves storedPath under mediaDir and serves it via
// http.ServeContent, writing a 404 for a missing/unsafe/unopenable file. It is
// the shared body behind the public share media routes; contentType, when
// non-empty, is set before serving (captions need text/vtt). It deliberately
// does not implement ?download and sets no Content-Disposition — the media file
// is stream-only, and an attachment header here would also put the on-disk
// filename on the wire, which for the subtitle track is "<videoID>.<lang>.vtt"
// and so would leak the very id publicVideoDTO withholds. (The basename below
// reaches http.ServeContent only for extension-based content sniffing; it is
// never written to a header.)
func (s *server) serveMediaFile(w http.ResponseWriter, r *http.Request, storedPath, contentType string) {
	if storedPath == "" {
		http.NotFound(w, r)
		return
	}
	safe, err := media.SafeMediaPath(s.mediaDir, storedPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(safe)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(w, r, filepath.Base(safe), stat.ModTime(), f)
}
