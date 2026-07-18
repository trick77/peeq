package auth

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestService_OIDCConfigured(t *testing.T) {
	db := openTestDB(t)

	devOnly := NewService(nil, NewSessionStore(db, false), NewUserStore(db))
	if devOnly.OIDCConfigured() {
		t.Fatal("OIDCConfigured() = true for a nil OIDC backend, want false")
	}

	withOIDC := NewService(
		NewOIDCService(OIDCServiceConfig{ClientID: "client", Backend: fakeOIDCBackend{}}),
		NewSessionStore(db, false),
		NewUserStore(db),
	)
	if !withOIDC.OIDCConfigured() {
		t.Fatal("OIDCConfigured() = false with an OIDC backend set, want true")
	}

	var nilService *Service
	if nilService.OIDCConfigured() {
		t.Fatal("OIDCConfigured() = true on a nil *Service, want false")
	}
}

func TestService_ClearOIDCCookiesIsNoOpWithoutOIDC(t *testing.T) {
	db := openTestDB(t)
	svc := NewService(nil, NewSessionStore(db, false), NewUserStore(db))
	rec := httptest.NewRecorder()

	svc.ClearOIDCCookies(rec) // must not panic

	if len(rec.Result().Cookies()) != 0 {
		t.Fatalf("expected no cookies set, got %v", rec.Result().Cookies())
	}
}

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
