package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/trick77/vark/internal/settings"
)

// settingsPatchRequest is the request body for PUT /api/settings. It never
// includes the cookie — that only ever moves through PUT /api/settings/cookie.
type settingsPatchRequest struct {
	FormatPreset        *string `json:"format_preset"`
	FormatCustom        *string `json:"format_custom"`
	LimitRate           *string `json:"limit_rate"`
	ThrottleBaseSeconds *int    `json:"throttle_base_seconds"`
	RetentionDays       *int    `json:"retention_days"`
	MinFreeGB           *int    `json:"min_free_gb"`
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
	patch := settings.Patch{
		FormatPreset:        req.FormatPreset,
		FormatCustom:        req.FormatCustom,
		LimitRate:           req.LimitRate,
		ThrottleBaseSeconds: req.ThrottleBaseSeconds,
		RetentionDays:       req.RetentionDays,
		MinFreeGB:           req.MinFreeGB,
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

// handlePutSettingsCookie is the only way the pasted cookie ever enters the
// system. On success it does not echo the cookie back — the response is the
// same cookie-body-free settings view as GET /api/settings.
func (s *server) handlePutSettingsCookie(w http.ResponseWriter, r *http.Request) {
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
