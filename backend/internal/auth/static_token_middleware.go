package auth

import (
	"net/http"

	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/secret"
)

// StaticTokenMiddleware gates a route on a bearer secret taken from the
// environment (BACKEND_MCP_TOKEN), as opposed to TokenMiddleware's DB-stored
// extension token. The two are deliberately separate credentials: the
// extension token can only write a cookie, while this one reads the library.
type StaticTokenMiddleware struct {
	secret string
}

// NewStaticTokenMiddleware returns middleware accepting exactly secret. An
// empty secret rejects every request.
func NewStaticTokenMiddleware(secret string) *StaticTokenMiddleware {
	return &StaticTokenMiddleware{secret: secret}
}

// RequireToken rejects a request whose "Authorization: Bearer" value is not
// the configured secret, answering 401 with a Bearer challenge. secret.Match
// leaks neither the differing byte nor the length, and matches nothing when
// the secret is empty. No user lands in the request context.
func (m *StaticTokenMiddleware) RequireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := httpx.Bearer(r)
		if !ok || !secret.Match(presented, m.secret) {
			unauthorizedBearer(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorizedBearer(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeJSONError(w, http.StatusUnauthorized, "unauthorized")
}
