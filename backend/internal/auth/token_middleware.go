package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/trick77/peeq/internal/apitoken"
)

// TokenHashLookup returns the stored API token hash, or "" when no token has
// been generated. *settings.Store satisfies this.
type TokenHashLookup interface {
	APITokenHash(context.Context) string
}

// TokenMiddleware gates peeq's machine endpoints on the API token. It is
// deliberately separate from Middleware: token requests are not sessions, so
// they establish no user identity, and keeping the two apart means the OIDC
// bypass surface is a single greppable middleware in server.go.
type TokenMiddleware struct {
	tokens TokenHashLookup
}

// NewTokenMiddleware returns middleware gating routes on the API token.
func NewTokenMiddleware(tokens TokenHashLookup) *TokenMiddleware {
	return &TokenMiddleware{tokens: tokens}
}

// RequireToken rejects requests without a valid "Authorization: Bearer
// <token>" header. It puts no user in the request context — handlers behind
// it must not call UserFromContext.
func (m *TokenMiddleware) RequireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := bearerToken(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// A nil lookup or an empty stored hash both fail closed.
		stored := ""
		if m.tokens != nil {
			stored = m.tokens.APITokenHash(r.Context())
		}
		if !apitoken.Verify(presented, stored) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearerToken extracts the credential from an Authorization header. The
// scheme match is case-insensitive per RFC 7235; the token itself is not.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}
