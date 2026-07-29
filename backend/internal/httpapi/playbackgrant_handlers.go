package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/trick77/peeq/internal/playbackgrant"
)

// PlaybackGrantStore is the slice of playbackgrant.Store the grant endpoints
// use — declared here at the consumer, following ShareLinkStore and
// PlaybackStore, so the error branches can be driven by a fake.
//
// The same typed-nil caveat applies: the handlers nil-check the INTERFACE, so a
// (*playbackgrant.Store)(nil) placed in it would panic rather than produce the
// documented 503 (owner route) or 404 (public route). playbackgrant.New never
// returns nil.
type PlaybackGrantStore interface {
	Mint(ctx context.Context, videoID string, ttl time.Duration) (token string, expiresAt string, err error)
	Resolve(ctx context.Context, token string) (videoID string, ok bool, err error)
}

// playbackGrantResponse is what POST /api/videos/{id}/playback-grant returns:
// the URL the <video> element should use as its src, and when it dies. The
// token is not returned separately — unlike a share link there is nothing for
// the owner to copy, so the URL is the only useful form.
type playbackGrantResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// handleCreatePlaybackGrant mints an auth-free stream URL for a video.
//
// Session-gated: only a signed-in owner can turn a video id into a grant, which
// is what keeps the auth-free route from being reachable by anyone who merely
// knows a YouTube id. Refuses with 409 when direct playback is switched off, so
// the UI can tell "you have not enabled this" apart from "this video does not
// exist" (404) — a distinction only the owner is ever shown.
func (s *server) handleCreatePlaybackGrant(w http.ResponseWriter, r *http.Request) {
	if s.playbackGrants == nil || s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "direct playback is not configured")
		return
	}
	if !s.settings.DirectStreamEnabled(r.Context()) {
		writeJSONError(w, http.StatusConflict, "direct playback links are turned off")
		return
	}
	v, ok := s.lookupVideo(w, r)
	if !ok {
		return
	}
	if v.MediaPath == "" {
		writeJSONError(w, http.StatusNotFound, "no media for this video")
		return
	}
	token, expiresAt, err := s.playbackGrants.Mint(r.Context(), v.ID, playbackgrant.DefaultTTL)
	if err != nil {
		serverError(w, r, err, "failed to create playback link")
		return
	}
	// Deliberately relative, unlike shareURL: a grant is consumed by the page
	// that asked for it, and Safari hands the receiver the URL already resolved
	// against the origin the browser is on. Building it against publicURL would
	// only risk pointing the <video> at a different host than the session.
	writeJSON(w, playbackGrantResponse{
		URL:       "/api/p/" + token + "/stream",
		ExpiresAt: expiresAt,
	})
}

// handleGrantStream serves a video's media file to a holder of a live grant
// token, with no session. This is the route an AirPlay receiver actually hits.
//
// Every failure — feature off, unknown token, expired token, missing store,
// missing file — is the same bare 404. A dead grant must be indistinguishable
// from one that never existed, matching resolveShare's contract.
func (s *server) handleGrantStream(w http.ResponseWriter, r *http.Request) {
	if s.playbackGrants == nil || s.videos == nil || s.settings == nil {
		http.NotFound(w, r)
		return
	}
	// Re-read the flag per request rather than deciding at route registration:
	// the setting lives in the DB and changes at runtime, so this is what makes
	// switching it off revoke every outstanding grant at once, without a
	// restart. Fails safe to false (settings.DirectStreamEnabled).
	if !s.settings.DirectStreamEnabled(r.Context()) {
		http.NotFound(w, r)
		return
	}
	videoID, ok, err := s.playbackGrants.Resolve(r.Context(), r.PathValue("token"))
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	v, err := s.videos.Get(videoID)
	if err != nil || v == nil {
		http.NotFound(w, r)
		return
	}
	// Same reason handleStreamVideo records it: the retention sweeper's
	// now-playing guard has to see an in-progress stream, and here it matters
	// more than usual — the thing playing is an Apple TV that would have no way
	// to recover if the file vanished mid-playback. A receiver re-issues range
	// requests throughout, so this keeps firing for as long as it is watching.
	if s.streamAccess != nil {
		s.streamAccess.RecordAccess(v.ID)
	}
	// serveMediaFile confines the path under mediaDir, serves through
	// http.ServeContent (range requests, which AirPlay depends on for seeking),
	// passes the real basename so the content type sniffs to video/mp4, and sets
	// no Content-Disposition — so the on-disk "<videoID>.<ext>" filename never
	// reaches the wire, exactly as on the share routes.
	s.serveMediaFile(w, r, v.MediaPath, "")
}
