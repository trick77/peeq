package auth

import (
	"context"
	"testing"
)

func TestUserStore_UpsertFromClaimsCreatesUserAsAdmin(t *testing.T) {
	db := openTestDB(t)
	store := NewUserStore(db)

	claims := Claims{
		Subject:           "oidc-sub-1",
		PreferredUsername: "jan",
		Email:             "jan@example.com",
		Name:              "Jan",
	}

	user, err := store.UpsertFromClaims(context.Background(), claims)
	if err != nil {
		t.Fatalf("UpsertFromClaims() error: %v", err)
	}
	if user.Role != RoleAdmin {
		t.Fatalf("role = %q, want admin", user.Role)
	}
	if user.ID == "" {
		t.Fatal("user id is empty")
	}

	// Re-authenticating the same subject refreshes the profile but stays admin.
	claims.Name = "Jan Updated"
	user, err = store.UpsertFromClaims(context.Background(), claims)
	if err != nil {
		t.Fatalf("second upsert error: %v", err)
	}
	if user.Role != RoleAdmin {
		t.Fatalf("role after refresh = %q, want admin", user.Role)
	}
	if user.DisplayName != "Jan Updated" {
		t.Fatalf("display name = %q, want refreshed value", user.DisplayName)
	}
	if user.Username != "jan" {
		t.Fatalf("username = %q, want jan", user.Username)
	}
}

func TestUserStore_UpsertFromClaimsFallsBackToEmailForUsername(t *testing.T) {
	db := openTestDB(t)
	store := NewUserStore(db)

	user, err := store.UpsertFromClaims(context.Background(), Claims{
		Subject: "oidc-sub-2",
		Email:   "user@example.com",
	})
	if err != nil {
		t.Fatalf("UpsertFromClaims() error: %v", err)
	}
	if user.Username != "user@example.com" {
		t.Fatalf("username = %q, want email fallback", user.Username)
	}
}

func TestUserStore_FindByIDReturnsUpsertedUser(t *testing.T) {
	db := openTestDB(t)
	store := NewUserStore(db)

	created, err := store.UpsertFromClaims(context.Background(), Claims{
		Subject:           "oidc-sub-3",
		PreferredUsername: "amy",
		Email:             "amy@example.com",
	})
	if err != nil {
		t.Fatalf("UpsertFromClaims() error: %v", err)
	}

	found, ok, err := store.FindByID(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("FindByID() error: %v", err)
	}
	if !ok {
		t.Fatal("FindByID() = not found, want found")
	}
	if found.Username != "amy" {
		t.Fatalf("username = %q, want amy", found.Username)
	}

	_, ok, err = store.FindByID(context.Background(), "missing-id")
	if err != nil {
		t.Fatalf("FindByID(missing) error: %v", err)
	}
	if ok {
		t.Fatal("FindByID(missing) = found, want not found")
	}
}
