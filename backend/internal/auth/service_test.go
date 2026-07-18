package auth

import (
	"context"
	"testing"
)

func TestService_CreateSessionFromClaimsUpsertsUserAndSession(t *testing.T) {
	db := openTestDB(t)
	svc := NewService(nil, NewSessionStore(db, false), NewUserStore(db))

	claims := Claims{Subject: "sub-1", PreferredUsername: "jan", Email: "jan@example.com"}
	session, user, err := svc.CreateSessionFromClaims(context.Background(), claims)
	if err != nil {
		t.Fatalf("CreateSessionFromClaims() error: %v", err)
	}
	if session.Token == "" {
		t.Fatal("session token is empty")
	}
	if user.Role != RoleAdmin {
		t.Fatalf("role = %q, want admin", user.Role)
	}

	sessions := NewSessionStore(db, false)
	got, ok, err := sessions.Lookup(context.Background(), session.Token)
	if err != nil || !ok {
		t.Fatalf("Lookup() = ok %v err %v", ok, err)
	}
	if got.UserID != user.ID {
		t.Fatalf("session user id = %q, want %q", got.UserID, user.ID)
	}
}

func TestService_RevokeDeletesSession(t *testing.T) {
	db := openTestDB(t)
	svc := NewService(nil, NewSessionStore(db, false), NewUserStore(db))

	claims := Claims{Subject: "sub-2", PreferredUsername: "amy"}
	session, _, err := svc.CreateSessionFromClaims(context.Background(), claims)
	if err != nil {
		t.Fatalf("CreateSessionFromClaims() error: %v", err)
	}

	if err := svc.Revoke(context.Background(), session.Token); err != nil {
		t.Fatalf("Revoke() error: %v", err)
	}

	sessions := NewSessionStore(db, false)
	_, ok, err := sessions.Lookup(context.Background(), session.Token)
	if err != nil {
		t.Fatalf("Lookup() error: %v", err)
	}
	if ok {
		t.Fatal("revoked session still found")
	}
}
