package httpapi

import "net/http"

// ytdlpVersionResponse is the shared response shape for both the version
// check and the update endpoint (update responds with the post-update
// version on success).
type ytdlpVersionResponse struct {
	Version string `json:"version"`
}

// handleYTDLPVersion reports the currently installed yt-dlp version, for
// the Settings page's "yt-dlp version" display.
func (s *server) handleYTDLPVersion(w http.ResponseWriter, r *http.Request) {
	if s.ytdlp == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "yt-dlp version check not configured")
		return
	}
	v, err := s.ytdlp.Version(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read yt-dlp version")
		return
	}
	writeJSON(w, ytdlpVersionResponse{Version: v})
}

// handleYTDLPUpdate runs yt-dlp's self-update and reports the resulting
// version, for the Settings page's Update button.
func (s *server) handleYTDLPUpdate(w http.ResponseWriter, r *http.Request) {
	if s.ytdlp == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "yt-dlp update not configured")
		return
	}
	v, err := s.ytdlp.UpdateLatest(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to update yt-dlp")
		return
	}
	writeJSON(w, ytdlpVersionResponse{Version: v})
}
