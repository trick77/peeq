package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/trick77/peeq/internal/settings"
)

// settingsPatchRequest is the request body for PUT /api/settings. It never
// includes the cookie — that only ever moves through PUT /api/settings/cookie.
type settingsPatchRequest struct {
	FormatPreset            *string `json:"format_preset"`
	FormatCustom            *string `json:"format_custom"`
	LimitRate               *string `json:"limit_rate"`
	ThrottleBaseSeconds     *int    `json:"throttle_base_seconds"`
	RetentionDays           *int    `json:"retention_days"`
	MinFreeGB               *int    `json:"min_free_gb"`
	MinVideoDurationSeconds *int    `json:"min_video_duration_seconds"`
}

// cookiePutRequest is the request body for PUT /api/settings/cookie.
type cookiePutRequest struct {
	Cookie string `json:"cookie"`
}

// handleGetSettings returns the non-secret settings. settings.Settings has
// no cookie_text field at all, so there is nothing for json.Encode to leak
// here even by accident.
func (s *server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings are not configured")
		return
	}
	got, err := s.settings.Get(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load settings")
		return
	}
	writeJSON(w, got)
}

// handlePutSettings updates the non-secret settings fields (presets,
// limit_rate, throttle, retention, min free space). The cookie body is
// never accepted here.
func (s *server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings are not configured")
		return
	}
	var req settingsPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Reject negative numeric settings before they can be persisted: a
	// negative retention_days moves the sweep cutoff into the future and
	// tombstones the whole library on the next pass; a negative min_free_gb
	// wraps (uint64) into a huge free-space floor that permanently freezes the
	// download queue; a negative throttle_base_seconds is simply nonsensical.
	// These are unrecoverable/queue-breaking, so fail closed with a 400.
	for _, f := range []struct {
		name string
		val  *int
	}{
		{"retention_days", req.RetentionDays},
		{"min_free_gb", req.MinFreeGB},
		{"throttle_base_seconds", req.ThrottleBaseSeconds},
		{"min_video_duration_seconds", req.MinVideoDurationSeconds},
	} {
		if f.val != nil && *f.val < 0 {
			writeJSONError(w, http.StatusBadRequest, f.name+" must not be negative")
			return
		}
	}
	patch := settings.Patch{
		FormatPreset:            req.FormatPreset,
		FormatCustom:            req.FormatCustom,
		LimitRate:               req.LimitRate,
		ThrottleBaseSeconds:     req.ThrottleBaseSeconds,
		RetentionDays:           req.RetentionDays,
		MinFreeGB:               req.MinFreeGB,
		MinVideoDurationSeconds: req.MinVideoDurationSeconds,
	}
	if err := s.settings.Update(r.Context(), patch); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to update settings")
		return
	}
	got, err := s.settings.Get(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load settings")
		return
	}
	writeJSON(w, got)
}

// machineCookieAck is the machine route's deliberately minimal response. A
// token holder can write a cookie and learn whether it took — nothing more.
type machineCookieAck struct {
	Status  string `json:"status"`
	Present bool   `json:"present"`
}

// handlePutSettingsCookie is the session-authenticated way the pasted cookie
// enters the system. On success it does not echo the cookie back — the
// response is the same cookie-body-free settings view as GET /api/settings.
func (s *server) handlePutSettingsCookie(w http.ResponseWriter, r *http.Request) {
	s.applyCookie(w, r, false)
}

// handleMachineCookie is the token-authenticated cookie-write path, used by
// the peeq browser extension. It is deliberately a separate route from
// handlePutSettingsCookie so that exactly one route in server.go bypasses
// OIDC, even though both share the write below. It answers with a minimal
// ack rather than the settings view: a token stored in a browser extension
// is more exposed than one in a terminal, so it should be able to read less.
func (s *server) handleMachineCookie(w http.ResponseWriter, r *http.Request) {
	s.applyCookie(w, r, true)
}

// applyCookie validates and stores a pasted cookie, then un-wedges the
// download worker. Shared by the session and machine routes; it must never
// echo the cookie body back. minimalAck selects the machine route's
// deliberately narrow response over the full settings view.
func (s *server) applyCookie(w http.ResponseWriter, r *http.Request, minimalAck bool) {
	if s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings are not configured")
		return
	}
	var req cookiePutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Cookie == "" {
		writeJSONError(w, http.StatusBadRequest, "cookie is required")
		return
	}
	if err := s.settings.SetCookie(r.Context(), req.Cookie, "valid"); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid cookie: "+err.Error())
		return
	}
	// A valid cookie is now stored: un-wedge the download worker if it paused
	// on a blocked/expired/absent cookie (this is the "re-paste and it
	// resumes" promise the Settings UI makes). Nil-safe: no worker wired in a
	// test/deployment without one just skips this.
	if s.worker != nil {
		s.worker.Resume()
	}
	if minimalAck {
		writeJSON(w, machineCookieAck{Status: "valid", Present: true})
		return
	}
	got, err := s.settings.Get(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load settings")
		return
	}
	writeJSON(w, got)
}

// cookieHealthResponse reports whether a usable cookie is stored, without
// ever exposing its contents.
type cookieHealthResponse struct {
	Status    string `json:"status"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Present   bool   `json:"present"`
}

// handleCookieHealth reports the cookie's status so the UI can prompt the
// user to (re)paste one, without ever exposing the cookie body itself.
func (s *server) handleCookieHealth(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings are not configured")
		return
	}
	got, err := s.settings.Get(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load settings")
		return
	}
	writeJSON(w, cookieHealthResponse{
		Status:    got.CookieStatus,
		UpdatedAt: got.CookieUpdatedAt,
		Present:   got.CookieStatus != "absent",
	})
}
