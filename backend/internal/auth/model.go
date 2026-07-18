// Package auth implements Authentik OIDC login, opaque session tokens, and
// the RequireAuth middleware for vark's single-user deployment model.
package auth

import (
	"context"
	"database/sql"
)

// Role is the app-local authorization role. Vark is single-user: the one
// authenticated user is always admin. The field is kept for parity with
// upstream (loom) and to leave room for multi-user support later.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleUser  Role = "user"
)

// User is vark's app-local user profile, created from a verified OIDC
// identity (or the dev auto-login identity in VARK_AUTH_MODE=dev).
type User struct {
	ID          string `json:"id"`
	OIDCSubject string `json:"-"`
	Username    string `json:"username"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	Role        Role   `json:"role"`
}

// Claims contains the verified OIDC identity fields vark needs.
type Claims struct {
	Subject           string
	PreferredUsername string
	Email             string
	Name              string
	Groups            []string
}

type contextKey string

const userContextKey contextKey = "vark_user"

// UserFromContext returns the authenticated user stored on a request context.
func UserFromContext(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(userContextKey).(User)
	return user, ok
}

// DBTX is the subset of *sql.DB used by auth stores.
type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
