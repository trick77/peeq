// Package tokenauth gates the bearer-token routes: the extension's cookie push
// (a DB-stored token) and /mcp (a secret from the environment). Neither is a
// session: browser sign-in is kit/auth's.
package tokenauth

import (
	"context"
	"net/http"

	"github.com/trick77/peeq/internal/apitoken"
	"github.com/trick77/ship/kit/httpx"
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
// <token>" header. It puts no identity in the request context — handlers
// behind it must not call auth.FromContext.
func (m *TokenMiddleware) RequireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		presented, ok := httpx.Bearer(r)
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// A nil lookup or an empty stored hash both fail closed.
		stored := ""
		if m.tokens != nil {
			stored = m.tokens.APITokenHash(r.Context())
		}
		if !apitoken.Verify(presented, stored) {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}
