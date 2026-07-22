package settings

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

func newTestStore(t *testing.T) *Store {
	return openTestDB(t)
}

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

func TestSetAndReadYoutubePaused(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()

	if paused, reason := s.YoutubePaused(ctx); paused || reason != "" {
		t.Fatalf("default = (%v,%q), want (false,\"\")", paused, reason)
	}
	if err := s.SetYoutubePaused(ctx, true, "extractor broke"); err != nil {
		t.Fatal(err)
	}
	paused, reason := s.YoutubePaused(ctx)
	if !paused || reason != "extractor broke" {
		t.Fatalf("after set = (%v,%q), want (true,\"extractor broke\")", paused, reason)
	}
	st, _ := s.Get(ctx)
	if !st.YoutubePaused || st.YoutubePauseReason != "extractor broke" {
		t.Fatalf("Get() didn't reflect pause: %+v", st)
	}
	if err := s.SetYoutubePaused(ctx, false, ""); err != nil {
		t.Fatal(err)
	}
	if paused, _ := s.YoutubePaused(ctx); paused {
		t.Fatal("still paused after resume")
	}
}

func TestUpdate_minVideoDurationSeconds(t *testing.T) {
	s := newTestStore(t) // existing helper in this test file
	want := 300
	if err := s.Update(context.Background(), Patch{MinVideoDurationSeconds: &want}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.MinVideoDurationSeconds != want {
		t.Fatalf("min_video_duration_seconds = %d, want %d", got.MinVideoDurationSeconds, want)
	}
	// Default is 180 on a fresh row — sanity that the column exists & seeds.
}

func TestUpdate_subtitlesDefault(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	got, err := s.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.SubtitlesDefault {
		t.Fatal("subtitles_default should seed to false")
	}

	on := true
	if err := s.Update(ctx, Patch{SubtitlesDefault: &on}); err != nil {
		t.Fatal(err)
	}
	if got, err = s.Get(ctx); err != nil {
		t.Fatal(err)
	}
	if !got.SubtitlesDefault {
		t.Fatal("subtitles_default = false after setting it true")
	}

	// A patch that omits the field must leave it alone — this is the whole
	// point of the COALESCE, and the first bool to go through it.
	days := 21
	if err := s.Update(ctx, Patch{RetentionDays: &days}); err != nil {
		t.Fatal(err)
	}
	if got, err = s.Get(ctx); err != nil {
		t.Fatal(err)
	}
	if !got.SubtitlesDefault {
		t.Fatal("subtitles_default was cleared by an unrelated patch")
	}

	off := false
	if err := s.Update(ctx, Patch{SubtitlesDefault: &off}); err != nil {
		t.Fatal(err)
	}
	if got, err = s.Get(ctx); err != nil {
		t.Fatal(err)
	}
	if got.SubtitlesDefault {
		t.Fatal("subtitles_default = true after setting it false")
	}
}

func TestAPIToken_roundTripsAndReportsPresence(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// Given: a fresh settings row, no token has ever been generated.
	present, createdAt, err := st.APITokenInfo(ctx)
	if err != nil {
		t.Fatalf("APITokenInfo: %v", err)
	}
	if present {
		t.Fatalf("present = true on a fresh row, want false")
	}
	if createdAt != "" {
		t.Fatalf("createdAt = %q on a fresh row, want empty", createdAt)
	}
	if got := st.APITokenHash(ctx); got != "" {
		t.Fatalf("APITokenHash = %q on a fresh row, want empty", got)
	}

	// When: a hash is stored.
	if _, err := st.SetAPITokenHash(ctx, "hash-one"); err != nil {
		t.Fatalf("SetAPITokenHash: %v", err)
	}

	// Then: it round-trips and is reported present with a timestamp.
	if got := st.APITokenHash(ctx); got != "hash-one" {
		t.Fatalf("APITokenHash = %q, want %q", got, "hash-one")
	}
	present, createdAt, err = st.APITokenInfo(ctx)
	if err != nil {
		t.Fatalf("APITokenInfo: %v", err)
	}
	if !present {
		t.Fatalf("present = false after storing a hash, want true")
	}
	if createdAt == "" {
		t.Fatalf("createdAt is empty after storing a hash, want a timestamp")
	}

	// When: a second hash replaces it (regeneration).
	if _, err := st.SetAPITokenHash(ctx, "hash-two"); err != nil {
		t.Fatalf("SetAPITokenHash (regenerate): %v", err)
	}

	// Then: the old hash is gone.
	if got := st.APITokenHash(ctx); got != "hash-two" {
		t.Fatalf("APITokenHash after regenerate = %q, want %q", got, "hash-two")
	}
}

func TestSetAPITokenHash_returnsStampedCreatedAt(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	createdAt, err := store.SetAPITokenHash(ctx, "hash-one")
	if err != nil {
		t.Fatalf("SetAPITokenHash: %v", err)
	}
	if createdAt == "" {
		t.Fatal("SetAPITokenHash returned an empty created_at")
	}

	// The returned value must be exactly what a subsequent read reports —
	// that equality is the whole point: it lets the handler skip the read.
	present, readCreatedAt, err := store.APITokenInfo(ctx)
	if err != nil {
		t.Fatalf("APITokenInfo: %v", err)
	}
	if !present {
		t.Fatal("APITokenInfo reports no token after SetAPITokenHash")
	}
	if readCreatedAt != createdAt {
		t.Fatalf("created_at mismatch: SetAPITokenHash=%q APITokenInfo=%q", createdAt, readCreatedAt)
	}
}

func TestGet_neverCarriesTheAPIToken(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if _, err := st.SetAPITokenHash(ctx, "hash-one"); err != nil {
		t.Fatalf("SetAPITokenHash: %v", err)
	}

	got, err := st.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// The Settings struct is the JSON API's view. A token field here would
	// leak a credential into every GET /api/settings response.
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "hash-one") {
		t.Fatalf("Settings JSON contains the token hash: %s", blob)
	}
	if strings.Contains(strings.ToLower(string(blob)), "api_token") {
		t.Fatalf("Settings JSON has an api_token field: %s", blob)
	}
}
