package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/trick77/peeq/internal/activity"
)

// ytdlpVersionResponse is the response shape for the version check: what is
// installed, what upstream publishes, and how fresh that second answer is.
//
// UpdateAvailable is computed server-side so no client has to know how
// yt-dlp versions order. CheckError is carried alongside rather than
// replacing the rest: a check that keeps failing leaves a stale Latest that
// is still worth showing, and without the error an unreachable GitHub would
// be indistinguishable from "you are up to date" — the exact silent failure
// the check exists to surface.
type ytdlpVersionResponse struct {
	Version         string `json:"version"`
	Latest          string `json:"latest,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	// CheckedAt is RFC3339, and stamps the last SUCCESSFUL check.
	CheckedAt  string `json:"checked_at,omitempty"`
	CheckError string `json:"check_error,omitempty"`
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

// handleYTDLPVersion reports the installed yt-dlp version alongside the
// newest published release, for the Settings page's "yt-dlp version" display
// and the nav rail's update indicator.
//
// The installed version is read live (a shell-out to --version) rather than
// taken from the cache, so a manual update is reflected immediately; only the
// upstream half, which costs a network call, is served from the cache.
func (s *server) handleYTDLPVersion(w http.ResponseWriter, r *http.Request) {
	if s.ytdlp == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "yt-dlp version check not configured")
		return
	}
	// An unreadable binary is reported as an empty version, not as a 500. A
	// broken or missing yt-dlp is exactly when knowing what upstream publishes
	// matters most, and failing the whole response would take the release
	// check's answer down with it — leaving the Settings note stuck on
	// "Checking for newer releases." forever, which is the silent failure this
	// endpoint exists to surface. An empty version also keeps
	// UpdateAvailable false below, so the rail stays quiet rather than nudging
	// an update at a deployment that has nothing installed to update.
	v, err := s.ytdlp.Version(r.Context())
	if err != nil {
		slog.Warn("yt-dlp version unreadable; reporting an empty version", "err", err)
		v = ""
	}
	latest, checkedAt, checkErr := s.ytdlp.Latest(r.Context())
	resp := ytdlpVersionResponse{
		Version: v,
		Latest:  latest,
		// Strictly "installed is older", never "differs": yt-dlp tags are
		// zero-padded calendar versions so a string compare orders them
		// exactly, and a nightly binary that is AHEAD of the last stable
		// must not be told to update. Mirrors ytdlp.Status.UpdateAvailable,
		// which the ticker uses on the other side of this cache.
		UpdateAvailable: latest != "" && v != "" && v < latest,
		CheckError:      checkErr,
	}
	if !checkedAt.IsZero() {
		resp.CheckedAt = checkedAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, resp)
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
	updated := previous == "" || previous != v
	// Record the install that actually moved the binary. This is the event the
	// whole version check exists around: the extractor every download depends
	// on just changed, and Activity is the only durable place a later "why did
	// downloads start behaving differently?" can be answered against a date —
	// the note beside the button is gone on the next navigation.
	//
	// A timer used to write this row; moving the trigger to a button is what
	// dropped it, not a decision that the event stopped mattering. The silence
	// rule still holds: a press that reinstalls the same version moved nothing
	// and records nothing. See the activity package doc for why this one
	// user-triggered event belongs in the log.
	if updated && s.activityWrite != nil {
		summary := "Updated to " + v
		if previous == "" {
			summary = "Installed " + v
		}
		s.activityWrite.Record(activity.Event{
			Kind: activity.KindYtdlp, Outcome: activity.OutcomeOK,
			Summary: summary,
		})
	}
	writeJSON(w, ytdlpUpdateResponse{
		Version:         v,
		PreviousVersion: previous,
		Updated:         updated,
	})
}
