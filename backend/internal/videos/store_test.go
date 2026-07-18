package videos

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/vark/internal/store"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestUpsert_insertThenGet(t *testing.T) {
	s := New(openTestDB(t))
	in := Video{
		ID:              "vid1",
		URL:             "https://youtu.be/vid1",
		Title:           "Hello",
		ChannelID:       "chan1",
		ChannelName:     "Chan",
		DurationSeconds: 120,
	}
	if err := s.Upsert(in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.Get("vid1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("get: nil, want row")
	}
	if got.URL != in.URL || got.Title != "Hello" || got.ChannelID != "chan1" {
		t.Fatalf("get mismatch: %+v", got)
	}
	if got.DurationSeconds != 120 {
		t.Fatalf("duration = %d, want 120", got.DurationSeconds)
	}
	if got.Status != "new" {
		t.Fatalf("status = %q, want default new", got.Status)
	}
}

func TestGet_missing(t *testing.T) {
	s := New(openTestDB(t))
	got, err := s.Get("nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Fatalf("get missing: got %+v, want nil", got)
	}
}

func TestUpsert_preservesDownloadState(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", Title: "old"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetDownloaded("v", DownloadedResult{
		MediaPath:     "/media/v.mp4",
		FilesizeBytes: 999,
		FormatUsed:    "bv+ba",
	}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	// Re-running metadata (Upsert) must not wipe the downloaded state.
	if err := s.Upsert(Video{ID: "v", URL: "u", Title: "new title"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "new title" {
		t.Fatalf("title = %q, want refreshed 'new title'", got.Title)
	}
	if got.Status != "downloaded" {
		t.Fatalf("status = %q, want preserved downloaded", got.Status)
	}
	if got.MediaPath != "/media/v.mp4" {
		t.Fatalf("media_path = %q, want preserved", got.MediaPath)
	}
}

func TestSetStatus_setsErrorMessage(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetStatus("v", "error", "video is private"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "error" {
		t.Fatalf("status = %q, want error", got.Status)
	}
	if got.ErrorMessage != "video is private" {
		t.Fatalf("error_message = %q, want %q", got.ErrorMessage, "video is private")
	}
}

func TestSetDownloaded_recordsResult(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// A prior error should be cleared by a successful download.
	if err := s.SetStatus("v", "error", "was rate limited"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if err := s.SetDownloaded("v", DownloadedResult{
		MediaPath:            "/media/chan/v/v.mp4",
		ThumbnailPath:        "/media/chan/v/v.jpg",
		FilesizeBytes:        123456,
		FormatUsed:           "bv*+ba",
		SponsorblockSegments: `[{"category":"sponsor"}]`,
	}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "downloaded" {
		t.Fatalf("status = %q, want downloaded", got.Status)
	}
	if got.MediaPath != "/media/chan/v/v.mp4" {
		t.Fatalf("media_path = %q", got.MediaPath)
	}
	if got.FilesizeBytes != 123456 {
		t.Fatalf("filesize = %d, want 123456", got.FilesizeBytes)
	}
	if got.FormatUsed != "bv*+ba" {
		t.Fatalf("format_used = %q", got.FormatUsed)
	}
	if got.SponsorblockSegments != `[{"category":"sponsor"}]` {
		t.Fatalf("sponsorblock = %q", got.SponsorblockSegments)
	}
	if got.ErrorMessage != "" {
		t.Fatalf("error_message = %q, want cleared", got.ErrorMessage)
	}
	if got.DownloadedAt == "" {
		t.Fatalf("downloaded_at not stamped")
	}
	if got.ThumbnailPath != "/media/chan/v/v.jpg" {
		t.Fatalf("thumbnail_path = %q", got.ThumbnailPath)
	}
}
