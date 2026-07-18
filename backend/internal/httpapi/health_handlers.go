package httpapi

import "net/http"

// handleHealthz is an unauthenticated liveness probe.
func (s *server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
