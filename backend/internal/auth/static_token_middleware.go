package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// StaticTokenMiddleware gates a route on a bearer secret taken from the
// environment (BACKEND_MCP_TOKEN), as opposed to TokenMiddleware's DB-stored
// extension token. The two are deliberately separate credentials: the
// extension token can only write a cookie, while this one reads the library.
type StaticTokenMiddleware struct {
	digest [sha256.Size]byte
	set    bool
}

// NewStaticTokenMiddleware returns middleware accepting exactly secret. An
// empty secret rejects every request.
func NewStaticTokenMiddleware(secret string) *StaticTokenMiddleware {
	return &StaticTokenMiddleware{digest: sha256.Sum256([]byte(secret)), set: secret != ""}
}

// RequireToken rejects a request whose "Authorization: Bearer" value is not
// the configured secret, answering 401 with a Bearer challenge. Both sides are
// hashed before the constant-time compare so the secret's length does not
// leak either. No user lands in the request context.
func (m *StaticTokenMiddleware) RequireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := bearerToken(r)
		if !ok || !m.set {
			unauthorizedBearer(w)
			return
		}
		got := sha256.Sum256([]byte(presented))
		if subtle.ConstantTimeCompare(got[:], m.digest[:]) != 1 {
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
