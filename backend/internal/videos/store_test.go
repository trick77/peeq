package videos

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
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

func TestUpsert_requestedFormat(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v1", URL: "u", RequestedFormat: "bestvideo+bestaudio"}); err != nil {
		t.Fatal(err)
	}
	v, err := s.Get("v1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.RequestedFormat != "bestvideo+bestaudio" {
		t.Fatalf("requested_format = %q", v.RequestedFormat)
	}
	if err := s.SetRequestedFormat("v1", "worst"); err != nil {
		t.Fatal(err)
	}
	v, err = s.Get("v1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.RequestedFormat != "worst" {
		t.Fatalf("after set, requested_format = %q", v.RequestedFormat)
	}
}

// TestUpsert_requestedFormatSurvivesMetadataResync guards against Upsert
// clobbering an already-set requested_format override: a later
// metadata-only re-sync (e.g. the channel scanner refreshing title/
// duration/etc.) always calls Upsert with a zero-value RequestedFormat,
// and that must NOT wipe an override previously written by
// SetRequestedFormat. requested_format is only ever changed via
// SetRequestedFormat once a row exists.
func TestUpsert_requestedFormatSurvivesMetadataResync(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v1", URL: "u", RequestedFormat: "bestvideo+bestaudio"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a metadata-only re-sync: same id, empty RequestedFormat.
	if err := s.Upsert(Video{ID: "v1", URL: "u", Title: "Updated Title"}); err != nil {
		t.Fatal(err)
	}
	v, err := s.Get("v1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.RequestedFormat != "bestvideo+bestaudio" {
		t.Fatalf("requested_format = %q, want preserved bestvideo+bestaudio", v.RequestedFormat)
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

func TestSetResume_autoMarksWatchedAtNinetyPercent_noResetOnRewatch(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// 95/100 = 95% >= 90% threshold: auto-marks watched.
	if err := s.SetResume("v", 95); err != nil {
		t.Fatalf("set resume: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Watched {
		t.Fatalf("watched = false, want true after resume >= 90%%")
	}
	if got.WatchedAt == "" {
		t.Fatalf("watched_at not set")
	}
	if got.ResumePositionSeconds != 95 {
		t.Fatalf("resume_position_seconds = %v, want 95", got.ResumePositionSeconds)
	}
	firstWatchedAt := got.WatchedAt

	// Re-watching (another SetResume above threshold) must NOT reset
	// watched_at — no "life extension".
	if err := s.SetResume("v", 98); err != nil {
		t.Fatalf("set resume again: %v", err)
	}
	got, err = s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.WatchedAt != firstWatchedAt {
		t.Fatalf("watched_at changed on re-watch: got %q, want unchanged %q", got.WatchedAt, firstWatchedAt)
	}
	if got.ResumePositionSeconds != 98 {
		t.Fatalf("resume_position_seconds = %v, want 98", got.ResumePositionSeconds)
	}

	// Manual un-watch clears both watched and watched_at (rescues from the
	// auto-delete sweep), and ALSO resets resume_position_seconds to 0 so
	// the rescue is sticky: a subsequent player resume ping can't
	// immediately re-cross the 90% threshold and undo the un-watch.
	if err := s.SetWatched("v", false); err != nil {
		t.Fatalf("set watched false: %v", err)
	}
	got, err = s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Watched {
		t.Fatalf("watched = true, want false after manual un-watch")
	}
	if got.WatchedAt != "" {
		t.Fatalf("watched_at = %q, want cleared after manual un-watch", got.WatchedAt)
	}
	if got.ResumePositionSeconds != 0 {
		t.Fatalf("resume_position_seconds = %v, want reset to 0 after manual un-watch", got.ResumePositionSeconds)
	}
}

func TestSetResume_belowThreshold_doesNotMarkWatched(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetResume("v", 50); err != nil {
		t.Fatalf("set resume: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Watched {
		t.Fatalf("watched = true, want false below 90%% threshold")
	}
	if got.WatchedAt != "" {
		t.Fatalf("watched_at = %q, want empty", got.WatchedAt)
	}
}

func TestSetWatched_manualTrue_setsWatchedAt(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetWatched("v", true); err != nil {
		t.Fatalf("set watched: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Watched || got.WatchedAt == "" {
		t.Fatalf("watched=%v watched_at=%q, want true/set", got.Watched, got.WatchedAt)
	}
}

// TestSetWatched_manualTrue_doesNotResetResumePosition ensures the sticky
// un-watch fix (which zeroes resume_position_seconds on SetWatched(id,
// false)) does not bleed into the true branch: manually (re-)marking a
// video watched must leave an existing resume position untouched.
func TestSetWatched_manualTrue_doesNotResetResumePosition(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetResume("v", 42); err != nil {
		t.Fatalf("set resume: %v", err)
	}
	if err := s.SetWatched("v", true); err != nil {
		t.Fatalf("set watched: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Watched {
		t.Fatalf("watched = false, want true")
	}
	if got.ResumePositionSeconds != 42 {
		t.Fatalf("resume_position_seconds = %v, want untouched 42", got.ResumePositionSeconds)
	}
}

func TestSetFavorite_toggles(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetFavorite("v", true); err != nil {
		t.Fatalf("set favorite: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Favorite {
		t.Fatalf("favorite = false, want true")
	}
	if err := s.SetFavorite("v", false); err != nil {
		t.Fatalf("set favorite false: %v", err)
	}
	got, err = s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Favorite {
		t.Fatalf("favorite = true, want false")
	}
}

func TestTombstone_clearsMediaPathSetsStatusKeepsRow(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetDownloaded("v", DownloadedResult{MediaPath: "/media/v.mp4", FilesizeBytes: 10}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := s.Tombstone("v"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("row was deleted, want kept")
	}
	if got.Status != "tombstoned" {
		t.Fatalf("status = %q, want tombstoned", got.Status)
	}
	if got.MediaPath != "" {
		t.Fatalf("media_path = %q, want cleared", got.MediaPath)
	}
}

// TestTombstoneClearsSubtitlePathKeepsSummary guards against a stale
// subtitle_path (and its .vtt) surviving a tombstone: the DTO derives
// has_subtitles from subtitle_path, so a leftover value would lie about
// transcript availability, and a subsequent resummarize must not flip a
// valid, kept summary to no_transcript.
func TestTombstoneClearsSubtitlePathKeepsSummary(t *testing.T) {
	s := New(openTestDB(t))
	const id = "vid1"
	if err := s.Upsert(Video{ID: id, URL: "u"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetDownloaded(id, DownloadedResult{
		MediaPath:       "/media/vid1.mp4",
		FilesizeBytes:   10,
		SubtitleRelPath: "vid1.en.vtt",
	}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := s.SetSummary(id, "a summary", `[]`, `[]`); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	if err := s.Tombstone(id); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	v, err := s.Get(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v.SubtitlePath != "" {
		t.Errorf("subtitle_path = %q, want empty after tombstone", v.SubtitlePath)
	}
	if v.Summary != "a summary" {
		t.Errorf("summary = %q, want kept after tombstone", v.Summary)
	}
	if v.Status != "tombstoned" {
		t.Errorf("status = %q, want tombstoned", v.Status)
	}
}

func TestList_filters(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "a", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDownloaded("a", DownloadedResult{MediaPath: "/m/a.mp4"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Video{ID: "b", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDownloaded("b", DownloadedResult{MediaPath: "/m/b.mp4"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWatched("b", true); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Video{ID: "c", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatus("c", "downloading", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavorite("a", true); err != nil {
		t.Fatal(err)
	}

	all, err := s.List("all", "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list all = %d, want 3", len(all))
	}

	unwatched, err := s.List("unwatched", "")
	if err != nil {
		t.Fatalf("list unwatched: %v", err)
	}
	if len(unwatched) != 1 || unwatched[0].ID != "a" {
		t.Fatalf("list unwatched = %+v, want [a]", unwatched)
	}

	watched, err := s.List("watched", "")
	if err != nil {
		t.Fatalf("list watched: %v", err)
	}
	if len(watched) != 1 || watched[0].ID != "b" {
		t.Fatalf("list watched = %+v, want [b]", watched)
	}

	favs, err := s.List("favorites", "")
	if err != nil {
		t.Fatalf("list favorites: %v", err)
	}
	if len(favs) != 1 || favs[0].ID != "a" {
		t.Fatalf("list favorites = %+v, want [a]", favs)
	}

	downloading, err := s.List("downloading", "")
	if err != nil {
		t.Fatalf("list downloading: %v", err)
	}
	if len(downloading) != 1 || downloading[0].ID != "c" {
		t.Fatalf("list downloading = %+v, want [c]", downloading)
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

// TestSetResume_negativePositionClampedToZero is the store-level
// defense-in-depth: the HTTP handler already rejects a negative resume
// position with 400, but the store must never persist one either, in case
// some other caller (a future internal job, a bug) skips the handler.
func TestSetResume_negativePositionClampedToZero(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetResume("v", -42); err != nil {
		t.Fatalf("set resume: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ResumePositionSeconds != 0 {
		t.Fatalf("resume_position_seconds = %v, want clamped to 0", got.ResumePositionSeconds)
	}
	if got.Watched {
		t.Fatalf("watched = true, want false for a clamped-to-0 position")
	}
}

// TestSweepCandidates_filtersByWatchedFavoriteTombstoneAndCutoff exercises
// the retention sweeper's underlying query directly: only a watched,
// non-favorite, non-tombstoned video whose watched_at is strictly before
// cutoff comes back.
func TestSweepCandidates_filtersByWatchedFavoriteTombstoneAndCutoff(t *testing.T) {
	db := openTestDB(t)
	s := New(db)

	seed := func(id string, watched, favorite bool, watchedAt string, tombstoned bool) {
		t.Helper()
		if err := s.Upsert(Video{ID: id, URL: "u-" + id}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
		status := "downloaded"
		if tombstoned {
			status = "tombstoned"
		}
		watchedInt := 0
		if watched {
			watchedInt = 1
		}
		favInt := 0
		if favorite {
			favInt = 1
		}
		_, err := db.Exec(
			`UPDATE videos SET watched = ?, favorite = ?, watched_at = ?, status = ? WHERE id = ?`,
			watchedInt, favInt, nullStr(watchedAt), status, id,
		)
		if err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	const cutoff = "2026-01-01 00:00:00"
	seed("old-eligible", true, false, "2025-01-01 00:00:00", false)  // before cutoff, watched, not fav -> candidate
	seed("old-favorite", true, true, "2025-01-01 00:00:00", false)   // favorite -> excluded
	seed("unwatched", false, false, "", false)                       // not watched -> excluded
	seed("old-tombstoned", true, false, "2025-01-01 00:00:00", true) // already gone -> excluded
	seed("recent", true, false, "2026-06-01 00:00:00", false)        // after cutoff -> excluded

	got, err := s.SweepCandidates(cutoff)
	if err != nil {
		t.Fatalf("sweep candidates: %v", err)
	}
	if len(got) != 1 || got[0].ID != "old-eligible" {
		ids := make([]string, len(got))
		for i, v := range got {
			ids[i] = v.ID
		}
		t.Fatalf("sweep candidates = %v, want [old-eligible]", ids)
	}
}

func TestSetCategoryAndListByCategory(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v-ai", URL: "u-v-ai", DurationSeconds: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDownloaded("v-ai", DownloadedResult{MediaPath: "/m/v-ai.mp4"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(Video{ID: "v-news", URL: "u-v-news", DurationSeconds: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDownloaded("v-news", DownloadedResult{MediaPath: "/m/v-news.mp4"}); err != nil {
		t.Fatal(err)
	}

	if err := s.SetCategory("v-ai", "ai"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCategory("v-news", "news"); err != nil {
		t.Fatal(err)
	}

	// Default before SetCategory is uncategorized; verify round-trip.
	got, err := s.Get("v-ai")
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "ai" {
		t.Fatalf("category = %q, want ai", got.Category)
	}

	// Category filter, orthogonal to status.
	ai, err := s.List("all", "ai")
	if err != nil {
		t.Fatal(err)
	}
	if len(ai) != 1 || ai[0].ID != "v-ai" {
		t.Fatalf("List all/ai = %v, want [v-ai]", ai)
	}

	// Empty / "all" category => no constraint.
	all, err := s.List("all", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("List all/'' returned %d, want 2", len(all))
	}
}
