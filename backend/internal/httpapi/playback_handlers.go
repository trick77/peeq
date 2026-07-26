package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/trick77/peeq/internal/playback"
	"github.com/trick77/peeq/internal/videos"
)

// PlaybackStore is the slice of playback.Store these handlers use — declared
// here, at the consumer, so the endpoints can be tested against a fake that
// returns an error on demand instead of against a real database with a table
// dropped out from under it. *playback.Store satisfies it, and in this one case
// the interface happens to cover the store's entire surface.
//
// Note for anyone wiring Deps.Playback: the nil checks in these handlers test
// the INTERFACE for nil, so a typed nil — a (*playback.Store)(nil) placed in the
// interface — is NOT nil here and would panic instead of degrading to the no-op
// behaviour each handler documents. playback.New never returns nil, so this is a
// trap for future wiring rather than a live one.
type PlaybackStore interface {
	Get(ctx context.Context) (playback.State, error)
	Set(ctx context.Context, videoID string) error
	Clear(ctx context.Context) error
	ClearIfVideo(ctx context.Context, videoID string) error
}

// playbackPutRequest is the request body for PUT /api/playback. An empty or
// absent video_id clears the pointer, which is how a caller says "nothing is
// playing" without needing a second verb.
type playbackPutRequest struct {
	VideoID string `json:"video_id"`
}

// handleGetPlaybackState returns the "now playing" pointer, which the SPA uses
// as the rail's fallback when the URL carries no video id.
//
// Note the deviation from every other handler in this package: a nil store
// reports an empty pointer instead of 503. The pointer is a convenience — the
// rail behaves exactly as it did before this feature when it can't be loaded —
// and an error banner on a page the user did not ask about would be worse than
// falling back silently.
func (s *server) handleGetPlaybackState(w http.ResponseWriter, r *http.Request) {
	if s.playback == nil {
		writeJSON(w, playback.State{})
		return
	}
	got, err := s.playback.Get(r.Context())
	if err != nil {
		serverError(w, r, err, "failed to load playback state")
		return
	}
	writeJSON(w, got)
}

// handlePutPlaybackState points "now playing" at a video, or clears it.
//
// PUT rather than POST: this is one singleton resource and the write is
// idempotent, exactly like PUT /api/settings. The Player calls it once per video
// it opens — never on the resume ping, which would be hundreds of identical
// writes an hour for a value that doesn't change between them.
func (s *server) handlePutPlaybackState(w http.ResponseWriter, r *http.Request) {
	var req playbackPutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if s.playback == nil {
		// Same reasoning as the GET: a missing store makes the pointer a no-op,
		// not an error the Player has to handle mid-playback.
		writeJSON(w, playback.State{})
		return
	}
	if req.VideoID == "" {
		if err := s.playback.Clear(r.Context()); err != nil {
			serverError(w, r, err, "failed to clear playback state")
			return
		}
		writeJSON(w, playback.State{})
		return
	}
	// Validated by hand rather than through lookupVideo: the id arrives in the
	// body, not the path. Refusing an unplayable target here keeps a dead
	// pointer out of the table in the first place, on top of Get's own filter.
	if s.videos == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "videos are not configured")
		return
	}
	v, err := s.videos.Get(req.VideoID)
	if err != nil {
		serverError(w, r, err, "failed to look up video")
		return
	}
	if v == nil || v.Status != videos.StatusDownloaded {
		writeJSONError(w, http.StatusNotFound, "video not found")
		return
	}
	if err := s.playback.Set(r.Context(), v.ID); err != nil {
		serverError(w, r, err, "failed to set playback state")
		return
	}
	got, err := s.playback.Get(r.Context())
	if err != nil {
		serverError(w, r, err, "failed to load playback state")
		return
	}
	writeJSON(w, got)
}

// clearPlaybackIfPointing drops the now-playing pointer when it is aimed at
// videoID. Called after a manual mark-watched and after a delete — both leave a
// video the rail should no longer offer to reopen.
//
// Best-effort by design: it lives here rather than inside videos.SetWatched
// because the videos store has no business knowing about a UI pointer, and a
// failure here must never fail the toggle or the delete the user actually asked
// for.
//
// Only the manual toggle calls this, never SetResume's >=90% auto-watch. That
// asymmetry mirrors the one in videos.SetWatched: the button zeroes the resume
// position, so a surviving pointer would reopen a finished video at 0:00, while
// auto-watch deliberately KEEPS the position — opening at 92% to watch the last
// two minutes is the right behaviour, not a bug.
func (s *server) clearPlaybackIfPointing(r *http.Request, videoID string) {
	if s.playback == nil {
		return
	}
	_ = s.playback.ClearIfVideo(r.Context(), videoID)
}
