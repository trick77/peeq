package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/videos"
)

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
// carries only what the public page renders: the video, its summary and
// highlights, and when the link expires.
type publicVideoDTO struct {
	Title           string          `json:"title"`
	ChannelName     string          `json:"channel_name"`
	DurationSeconds int64           `json:"duration_seconds,omitempty"`
	Summary         string          `json:"summary"`
	SummaryStatus   string          `json:"summary_status"`
	Chapters        json.RawMessage `json:"chapters,omitempty"`
	KeyPoints       json.RawMessage `json:"key_points,omitempty"`
	HasThumbnail    bool            `json:"has_thumbnail"`
	HasSubtitles    bool            `json:"has_subtitles"`
	AudioLanguage   string          `json:"audio_language"`
	ExpiresAt       string          `json:"expires_at,omitempty"`
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

// handleCreateShare mints (or replaces) the share link for a video. The body is
// {"ttl": "24h"|"7d"|"30d"|"never"|""}; re-sharing an already-shared video
// rotates the token, so the previous link stops working immediately.
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
		// A DB error is ours, not the caller's — log it, but still answer 404
		// so the response is identical to the not-found case.
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
		Title:           v.Title,
		ChannelName:     v.ChannelName,
		DurationSeconds: v.DurationSeconds,
		Summary:         v.Summary,
		SummaryStatus:   v.SummaryStatus,
		Chapters:        rawJSONOrNil(v.Chapters),
		KeyPoints:       rawJSONOrNil(v.KeyPoints),
		HasThumbnail:    v.ThumbnailPath != "",
		HasSubtitles:    v.SubtitlePath != "",
		AudioLanguage:   v.AudioLanguage,
		ExpiresAt:       expiresAt,
	})
}

// handleShareStream streams the shared video's media file. It reuses the exact
// safe-path + ServeContent path the authenticated stream endpoint uses (Range
// requests for seeking, conditional requests), but never honors ?download=1 —
// a share is watch-only, the file itself never leaves as a file.
func (s *server) handleShareStream(w http.ResponseWriter, r *http.Request) {
	v := s.resolveShare(w, r)
	if v == nil {
		return
	}
	s.serveMediaFile(w, r, v.MediaPath, "")
}

// handleShareThumbnail serves the shared video's poster image.
func (s *server) handleShareThumbnail(w http.ResponseWriter, r *http.Request) {
	v := s.resolveShare(w, r)
	if v == nil {
		return
	}
	s.serveMediaFile(w, r, v.ThumbnailPath, "")
}

// handleShareSubtitles serves the shared video's VTT captions.
func (s *server) handleShareSubtitles(w http.ResponseWriter, r *http.Request) {
	v := s.resolveShare(w, r)
	if v == nil {
		return
	}
	s.serveMediaFile(w, r, v.SubtitlePath, "text/vtt; charset=utf-8")
}

// serveMediaFile resolves storedPath under mediaDir and serves it via
// http.ServeContent, writing a 404 for a missing/unsafe/unopenable file. It is
// the shared body behind the public share media routes; contentType, when
// non-empty, is set before serving (captions need text/vtt). It deliberately
// does not implement ?download — public shares are stream-only.
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
