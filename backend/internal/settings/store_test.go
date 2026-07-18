package settings

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/trick77/vark/internal/store"
)

func openTestDB(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

const validYouTubeCookie = "# Netscape HTTP Cookie File\n" +
	".youtube.com\tTRUE\t/\tTRUE\t1789000000\tSID\tabc\n" +
	".youtube.com\tTRUE\t/\tTRUE\t1789000000\t__Secure-3PSID\tdef\n"

func TestGet_defaults(t *testing.T) {
	s := openTestDB(t)
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CookieStatus != "absent" {
		t.Fatalf("CookieStatus = %q, want %q", got.CookieStatus, "absent")
	}
	if got.FormatPreset != "apple-1080p" {
		t.Fatalf("FormatPreset = %q, want %q", got.FormatPreset, "apple-1080p")
	}
	if got.RetentionDays != 14 {
		t.Fatalf("RetentionDays = %d, want 14", got.RetentionDays)
	}
}

func TestSetCookie_rejectsInvalid(t *testing.T) {
	s := openTestDB(t)
	if err := s.SetCookie(context.Background(), "garbage", "valid"); err == nil {
		t.Fatal("expected SetCookie to reject invalid cookie text")
	}
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CookieStatus != "absent" {
		t.Fatalf("CookieStatus after rejected SetCookie = %q, want unchanged %q", got.CookieStatus, "absent")
	}
}

func TestSetCookie_acceptsValid(t *testing.T) {
	s := openTestDB(t)
	if err := s.SetCookie(context.Background(), validYouTubeCookie, "valid"); err != nil {
		t.Fatalf("SetCookie: %v", err)
	}
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.CookieStatus != "valid" {
		t.Fatalf("CookieStatus = %q, want %q", got.CookieStatus, "valid")
	}
	if got.CookieUpdatedAt == "" {
		t.Fatal("CookieUpdatedAt should be set after a valid SetCookie")
	}
	if status := s.CookieStatus(context.Background()); status != "valid" {
		t.Fatalf("CookieStatus() = %q, want %q", status, "valid")
	}
}

func TestUpdate_appliesPatch(t *testing.T) {
	s := openTestDB(t)
	preset := "custom"
	retention := 30
	if err := s.Update(context.Background(), Patch{
		FormatPreset:  &preset,
		RetentionDays: &retention,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.FormatPreset != "custom" {
		t.Fatalf("FormatPreset = %q, want %q", got.FormatPreset, "custom")
	}
	if got.RetentionDays != 30 {
		t.Fatalf("RetentionDays = %d, want 30", got.RetentionDays)
	}
	// Untouched fields keep their previous value.
	if got.MinFreeGB != 5 {
		t.Fatalf("MinFreeGB = %d, want unchanged default 5", got.MinFreeGB)
	}
}
