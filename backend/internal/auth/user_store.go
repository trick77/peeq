package auth

import (
	"context"
	"database/sql"
	"fmt"
)

// UserStore persists the app-local user mapped from an authenticated OIDC
// identity (or the dev auto-login identity).
type UserStore struct {
	db DBTX
}

// NewUserStore returns a user store backed by db.
func NewUserStore(db DBTX) *UserStore {
	return &UserStore{db: db}
}

// UpsertFromClaims creates or refreshes the local user from verified OIDC
// claims. Vark is single-user, so the authenticated identity is always the
// admin; the role field is kept only for parity with a possible future
// multi-user mode.
func (s *UserStore) UpsertFromClaims(ctx context.Context, claims Claims) (User, error) {
	username := claims.PreferredUsername
	if username == "" {
		username = claims.Email
	}
	if username == "" {
		username = claims.Subject
	}

	existing, ok, err := s.findBySubject(ctx, claims.Subject)
	if err != nil {
		return User{}, err
	}
	if ok {
		_, err = s.db.ExecContext(ctx, `
UPDATE users
SET username = ?, email = ?, display_name = ?, role = ?, updated_at = datetime('now'), last_seen_at = datetime('now')
WHERE oidc_subject = ?`,
			username, claims.Email, claims.Name, RoleAdmin, claims.Subject,
		)
		if err != nil {
			return User{}, fmt.Errorf("update user: %w", err)
		}
		existing.Username = username
		existing.Email = claims.Email
		existing.DisplayName = claims.Name
		existing.Role = RoleAdmin
		return existing, nil
	}

	user := User{
		ID:          newID(),
		OIDCSubject: claims.Subject,
		Username:    username,
		Email:       claims.Email,
		DisplayName: claims.Name,
		Role:        RoleAdmin,
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO users (id, oidc_subject, username, email, display_name, role, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, datetime('now'))`,
		user.ID, user.OIDCSubject, user.Username, user.Email, user.DisplayName, user.Role,
	)
	if err != nil {
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	return user, nil
}

// FindByID returns a user by app-local ID.
func (s *UserStore) FindByID(ctx context.Context, id string) (User, bool, error) {
	var user User
	err := s.db.QueryRowContext(ctx, `
SELECT id, oidc_subject, username, email, display_name, role
FROM users
WHERE id = ?`,
		id,
	).Scan(&user.ID, &user.OIDCSubject, &user.Username, &user.Email, &user.DisplayName, &user.Role)
	if err == nil {
		return user, true, nil
	}
	if err == sql.ErrNoRows {
		return User{}, false, nil
	}
	return User{}, false, fmt.Errorf("find user by id: %w", err)
}

func (s *UserStore) findBySubject(ctx context.Context, subject string) (User, bool, error) {
	var user User
	err := s.db.QueryRowContext(ctx, `
SELECT id, oidc_subject, username, email, display_name, role
FROM users
WHERE oidc_subject = ?`,
		subject,
	).Scan(&user.ID, &user.OIDCSubject, &user.Username, &user.Email, &user.DisplayName, &user.Role)
	if err == nil {
		return user, true, nil
	}
	if err == sql.ErrNoRows {
		return User{}, false, nil
	}
	return User{}, false, fmt.Errorf("find user: %w", err)
}
