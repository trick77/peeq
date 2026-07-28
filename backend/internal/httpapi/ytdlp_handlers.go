package httpapi

import (
	"log/slog"
	"net/http"
)

// ytdlpVersionResponse is the response shape for the version check.
type ytdlpVersionResponse struct {
	Version string `json:"version"`
}

// ytdlpUpdateResponse reports what the update actually did, so the Settings
// page can tell "already on the latest build" apart from a real upgrade —
// with only the resulting version to go on, the two are indistinguishable.
//
// Updated describes the VERSION, not the download: UpdateLatest always
// fetches and reinstalls the latest release, so Updated=false means the
// version did not change, never that the download was skipped.
type ytdlpUpdateResponse struct {
	Version         string `json:"version"`
	PreviousVersion string `json:"previous_version"`
	Updated         bool   `json:"updated"`
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
		serverError(w, r, err, "failed to read yt-dlp version")
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
	// Read the installed version BEFORE replacing it — it is the only way to
	// know afterwards whether anything moved. A failure here is deliberately
	// not fatal: the binary may be missing or unrunnable entirely, which is
	// exactly when an update is most wanted. That case reports no previous
	// version and counts as an update.
	// Swallowing it silently would change the user-visible copy ("Installed X"
	// instead of "Updated A → B") with nothing anywhere to say why, so it is
	// logged the same way the boot-time read is in main.go.
	previous, err := s.ytdlp.Version(r.Context())
	if err != nil {
		slog.Warn("yt-dlp version unreadable before update; reporting no previous version", "err", err)
		previous = ""
	}

	v, err := s.ytdlp.UpdateLatest(r.Context())
	if err != nil {
		serverError(w, r, err, "failed to update yt-dlp")
		return
	}
	writeJSON(w, ytdlpUpdateResponse{
		Version:         v,
		PreviousVersion: previous,
		Updated:         previous == "" || previous != v,
	})
}
