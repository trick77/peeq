package auth

import (
	"context"
	"net/http"

	"github.com/trick77/ship/kit/httpx"
)

// SessionLookup is the session lookup dependency required by Middleware.
type SessionLookup interface {
	Lookup(context.Context, string) (Session, bool, error)
}

// UserLookup is the user lookup dependency required by Middleware.
type UserLookup interface {
	FindByID(context.Context, string) (User, bool, error)
}

// Middleware protects authenticated routes. Peeq is single-user, so there is
// no separate admin-only gate: any authenticated session is the admin.
type Middleware struct {
	sessions SessionLookup
	users    UserLookup
}

// NewMiddleware returns auth middleware.
func NewMiddleware(sessions SessionLookup, users UserLookup) *Middleware {
	return &Middleware{sessions: sessions, users: users}
}

// RequireAuth rejects requests without a valid peeq session.
func (m *Middleware) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || cookie.Value == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		session, ok, err := m.sessions.Lookup(r.Context(), cookie.Value)
		if err != nil {
			httpx.ServerError(w, r, err, "session lookup failed")
			return
		}
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		user, ok, err := m.users.FindByID(r.Context(), session.UserID)
		if err != nil {
			httpx.ServerError(w, r, err, "user lookup failed")
			return
		}
		if !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
