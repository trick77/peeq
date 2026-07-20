package httpapi

import (
	"crypto/rand"
	"net/http"

	"github.com/trick77/peeq/internal/apitoken"
)

// apiTokenStatusResponse reports whether a machine token exists, without
// exposing anything secret. Safe to return to any authenticated session.
type apiTokenStatusResponse struct {
	CreatedAt string `json:"created_at,omitempty"`
	Present   bool   `json:"present"`
}

// apiTokenCreatedResponse carries the plaintext token. This is the only
// response in peeq that ever contains it: the token is stored as a hash, so
// it cannot be shown again afterwards.
type apiTokenCreatedResponse struct {
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}

// handleGetAPIToken reports whether a machine token has been generated, so
// the Settings UI can pick between its empty and active states.
func (s *server) handleGetAPIToken(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings are not configured")
		return
	}
	present, createdAt, err := s.settings.APITokenInfo(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load api token")
		return
	}
	writeJSON(w, apiTokenStatusResponse{Present: present, CreatedAt: createdAt})
}

// handlePostAPIToken generates a machine token, stores only its hash, and
// returns the plaintext once. It serves both first-time creation and
// regeneration: the operation is identical, and overwriting the stored hash
// is what invalidates any previous token immediately.
func (s *server) handlePostAPIToken(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings are not configured")
		return
	}
	token, err := apitoken.Generate(rand.Reader)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to generate api token")
		return
	}
	if err := s.settings.SetAPITokenHash(r.Context(), apitoken.Hash(token)); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to store api token")
		return
	}
	_, createdAt, err := s.settings.APITokenInfo(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to load api token")
		return
	}
	writeJSON(w, apiTokenCreatedResponse{Token: token, CreatedAt: createdAt})
}
