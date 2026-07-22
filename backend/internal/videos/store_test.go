package videos

import (
	"context"
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

// newTestStore returns a Store backed by a fresh, migrated SQLite db in a
// temp dir.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return New(openTestDB(t))
}

// seedVideo upserts v and, if v.CreatedAt is set, backfills the created_at
// column directly (Upsert never writes it; the schema default is
// datetime('now'), which tests need to override to control List sort
// order).
func seedVideo(t *testing.T, s *Store, v Video) {
	t.Helper()
	if err := s.Upsert(v); err != nil {
		t.Fatalf("seed video %s: %v", v.ID, err)
	}
	if v.Status != "" {
		if err := s.SetStatus(v.ID, v.Status, ""); err != nil {
			t.Fatalf("seed video %s status: %v", v.ID, err)
		}
	}
	if v.CreatedAt != "" {
		if _, err := s.db.ExecContext(context.Background(),
			`UPDATE videos SET created_at = ? WHERE id = ?`, v.CreatedAt, v.ID); err != nil {
			t.Fatalf("seed video %s created_at: %v", v.ID, err)
		}
	}
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

// TestSetResumeRaw_doesNotAutoWatch is the discriminator against SetResume: the
// TubeArchivist import writes resume positions with SetResumeRaw so a
// nearly-finished "continue" video keeps its position without being flipped to
// watched (which would drop it out of the Continue Watching queue).
func TestSetResumeRaw_doesNotAutoWatch(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// 95/100 = 95%: SetResume would auto-mark this watched; SetResumeRaw must not.
	if err := s.SetResumeRaw("v", 95); err != nil {
		t.Fatalf("set resume raw: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ResumePositionSeconds != 95 {
		t.Fatalf("resume_position_seconds = %v, want 95", got.ResumePositionSeconds)
	}
	if got.Watched {
		t.Fatalf("watched = true, want false — SetResumeRaw must not auto-watch")
	}
	if got.WatchedAt != "" {
		t.Fatalf("watched_at = %q, want empty", got.WatchedAt)
	}
}

// TestSetResumeRaw_missingRow errors rather than silently no-op'ing, so the
// import's Upsert-before-resume ordering is enforced.
func TestSetResumeRaw_missingRow(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.SetResumeRaw("nope", 10); err == nil {
		t.Fatal("err = nil, want a not-found error for a missing row")
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

// TestSetWatched_manualTrue_resetsResumePosition covers the manual
// mark-watched rule: pressing the button means "done", so any stored resume
// position is cleared and reopening the video starts at 0:00.
func TestSetWatched_manualTrue_resetsResumePosition(t *testing.T) {
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
	if got.ResumePositionSeconds != 0 {
		t.Fatalf("resume_position_seconds = %v, want 0", got.ResumePositionSeconds)
	}
}

// TestSetResume_autoWatched_keepsResumePosition guards the deliberate
// asymmetry with the test above: a video that crossed the 90% threshold by
// actually playing keeps its position, so the last few minutes stay
// resumable. Only the manual button means "done".
func TestSetResume_autoWatched_keepsResumePosition(t *testing.T) {
	s := New(openTestDB(t))
	if err := s.Upsert(Video{ID: "v", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetResume("v", 95); err != nil {
		t.Fatalf("set resume: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Watched {
		t.Fatalf("watched = false, want true (95 >= 90%% of 100)")
	}
	if got.ResumePositionSeconds != 95 {
		t.Fatalf("resume_position_seconds = %v, want untouched 95", got.ResumePositionSeconds)
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

// idsOf collapses a result list to a set of ids, for assertions that care
// about membership rather than the sort order List happens to apply.
func idsOf(vs []Video) map[string]bool {
	ids := make(map[string]bool, len(vs))
	for _, v := range vs {
		ids[v.ID] = true
	}
	return ids
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

	all, err := s.List(ListOptions{Filter: "all", Category: ""})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("list all = %d, want 3", len(all))
	}

	// "unwatched" is the Library's watch queue, so it covers what is already
	// downloaded (a) *and* what is still on its way (c, downloading) — but
	// never the watched one (b).
	unwatched, err := s.List(ListOptions{Filter: "unwatched", Category: ""})
	if err != nil {
		t.Fatalf("list unwatched: %v", err)
	}
	if ids := idsOf(unwatched); len(ids) != 2 || !ids["a"] || !ids["c"] {
		t.Fatalf("list unwatched = %+v, want [a c]", unwatched)
	}

	watched, err := s.List(ListOptions{Filter: "watched", Category: ""})
	if err != nil {
		t.Fatalf("list watched: %v", err)
	}
	if len(watched) != 1 || watched[0].ID != "b" {
		t.Fatalf("list watched = %+v, want [b]", watched)
	}

	favs, err := s.List(ListOptions{Filter: "favorites", Category: ""})
	if err != nil {
		t.Fatalf("list favorites: %v", err)
	}
	if len(favs) != 1 || favs[0].ID != "a" {
		t.Fatalf("list favorites = %+v, want [a]", favs)
	}

	downloading, err := s.List(ListOptions{Filter: "downloading", Category: ""})
	if err != nil {
		t.Fatalf("list downloading: %v", err)
	}
	if len(downloading) != 1 || downloading[0].ID != "c" {
		t.Fatalf("list downloading = %+v, want [c]", downloading)
	}
}

// TestList_unwatched_excludesDeadRows pins the other half of the watch-queue
// rule: an unwatched row whose download failed, or whose media the retention
// sweeper has already reclaimed, is not something to watch and must stay out
// of the queue — the filter widened to queued/downloading, not to everything.
func TestList_unwatched_excludesDeadRows(t *testing.T) {
	s := New(openTestDB(t))
	for _, id := range []string{"err", "tomb", "ok"} {
		if err := s.Upsert(Video{ID: id, URL: "u"}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	if err := s.SetDownloaded("ok", DownloadedResult{MediaPath: "/m/ok.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := s.SetStatus("err", "error", "yt-dlp exploded"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if err := s.SetDownloaded("tomb", DownloadedResult{MediaPath: "/m/tomb.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := s.Tombstone("tomb"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	unwatched, err := s.List(ListOptions{Filter: "unwatched"})
	if err != nil {
		t.Fatalf("list unwatched: %v", err)
	}
	if len(unwatched) != 1 || unwatched[0].ID != "ok" {
		t.Fatalf("list unwatched = %+v, want [ok]", unwatched)
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
	ai, err := s.List(ListOptions{Filter: "all", Category: "ai"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ai) != 1 || ai[0].ID != "v-ai" {
		t.Fatalf("List all/ai = %v, want [v-ai]", ai)
	}

	// Empty / "all" category => no constraint.
	all, err := s.List(ListOptions{Filter: "all", Category: ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("List all/'' returned %d, want 2", len(all))
	}
}

func TestList_statusAndCategoryAreAnded(t *testing.T) {
	s := New(openTestDB(t))

	// Given: four videos spanning both axes — watched × category.
	seed := []struct {
		id       string
		watched  bool
		category string
	}{
		{"a", false, "ai"},
		{"b", false, "gaming"},
		{"c", true, "ai"},
		{"d", true, "gaming"},
	}
	for _, v := range seed {
		if err := s.Upsert(Video{ID: v.id, URL: "u", DurationSeconds: 100}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetDownloaded(v.id, DownloadedResult{MediaPath: "/m/" + v.id + ".mp4"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetCategory(v.id, v.category); err != nil {
			t.Fatal(err)
		}
		if v.watched {
			if err := s.SetWatched(v.id, true); err != nil {
				t.Fatal(err)
			}
		}
	}

	// When: both filters are applied together.
	got, err := s.List(ListOptions{Filter: "unwatched", Category: "ai"})
	if err != nil {
		t.Fatalf("list unwatched+ai: %v", err)
	}

	// Then: only the row matching BOTH comes back — not the union of each.
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("list unwatched+ai = %+v, want [a]", got)
	}

	// And: each filter alone still returns its own two rows, proving the
	// combination narrowed the result rather than one filter winning.
	unwatched, err := s.List(ListOptions{Filter: "unwatched", Category: ""})
	if err != nil {
		t.Fatalf("list unwatched: %v", err)
	}
	if len(unwatched) != 2 {
		t.Fatalf("list unwatched = %d rows, want 2", len(unwatched))
	}
	ai, err := s.List(ListOptions{Filter: "all", Category: "ai"})
	if err != nil {
		t.Fatalf("list ai: %v", err)
	}
	if len(ai) != 2 {
		t.Fatalf("list ai = %d rows, want 2", len(ai))
	}
}

// TestList_query_matchesTitleCaseInsensitively asserts the search box matches
// on title regardless of case, and that a non-matching row is excluded.
func TestList_query_matchesTitleCaseInsensitively(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", Title: "Descending the Hranice Abyss", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "v2", Title: "Night trek across the Salar", Status: "downloaded"})

	got, err := s.List(ListOptions{Query: "HRANICE"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != "v1" {
		t.Fatalf("got %d rows %+v, want only v1", len(got), got)
	}
}

// TestList_query_escapesLikeWildcards asserts a literal % in the search box
// does not turn into a match-everything wildcard.
func TestList_query_escapesLikeWildcards(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", Title: "100% wool", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "v2", Title: "nothing special", Status: "downloaded"})

	got, err := s.List(ListOptions{Query: "%"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != "v1" {
		t.Fatalf("got %d rows %+v, want only the row literally containing %%", len(got), got)
	}
}

// TestList_sort_ordersRows asserts each sort key produces the documented
// order. Sorting was previously hardcoded to created_at DESC.
func TestList_sort_ordersRows(t *testing.T) {
	s := newTestStore(t)
	// Three videos whose four orderings are all DISTINCT (title, duration and
	// published_at each rank them differently). A two-row fixture made title and
	// longest coincide with the newest fallback, so a dropped or mis-mapped
	// sort key could pass unnoticed; with these rows any such regression yields
	// the wrong first row and fails.
	//
	// created_at deliberately runs OPPOSITE to published_at: newest/oldest rank
	// by RELEASE date, so a fixture where the two agree would still pass with
	// the old created_at-only clause.
	seedVideo(t, s, Video{ID: "c1", Title: "Charlie", DurationSeconds: 200, PublishedAt: "2026-03-01", CreatedAt: "2026-01-01 00:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "a2", Title: "Alpha", DurationSeconds: 100, PublishedAt: "2026-02-01", CreatedAt: "2026-02-01 00:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "b3", Title: "Bravo", DurationSeconds: 300, PublishedAt: "2026-01-01", CreatedAt: "2026-03-01 00:00:00", Status: "downloaded"})

	cases := []struct {
		sort string
		want []string
	}{
		{"newest", []string{"c1", "a2", "b3"}},  // published_at DESC
		{"oldest", []string{"b3", "a2", "c1"}},  // published_at ASC
		{"longest", []string{"b3", "c1", "a2"}}, // duration DESC
		{"title", []string{"a2", "b3", "c1"}},   // title NOCASE ASC
	}
	for _, tc := range cases {
		got, err := s.List(ListOptions{Sort: tc.sort})
		if err != nil {
			t.Fatalf("list sort=%s: %v", tc.sort, err)
		}
		var ids []string
		for _, v := range got {
			ids = append(ids, v.ID)
		}
		if len(ids) != len(tc.want) {
			t.Fatalf("sort=%s ids = %v, want %v", tc.sort, ids, tc.want)
		}
		for i := range tc.want {
			if ids[i] != tc.want[i] {
				t.Fatalf("sort=%s ids = %v, want %v", tc.sort, ids, tc.want)
			}
		}
	}
}

// TestSetDownloaded_fillsPublishedAt asserts the download's own info.json
// supplies the release date for videos seeded from a metadata-poor flat
// channel listing — without it, everything peeq auto-downloads would sort by
// download date forever.
func TestSetDownloaded_fillsPublishedAt(t *testing.T) {
	// Given: a row seeded the way scan.Scheduler.enqueueAuto seeds one — no
	// release date.
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "auto", URL: "u"})

	// When: the download completes and reports one.
	if err := s.SetDownloaded("auto", DownloadedResult{MediaPath: "/m/auto.mp4", PublishedAt: "2025-04-09"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	// Then: the row carries it.
	got, err := s.Get("auto")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PublishedAt != "2025-04-09" {
		t.Fatalf("published_at = %q, want 2025-04-09", got.PublishedAt)
	}
}

// TestSetDownloaded_emptyPublishedAt_keepsExisting asserts a re-download of a
// video whose release date is already known (the manual-add path fetches it
// up front) never blanks it out when yt-dlp reports no upload_date.
func TestSetDownloaded_emptyPublishedAt_keepsExisting(t *testing.T) {
	// Given: a row that already knows its release date.
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "known", URL: "u", PublishedAt: "2025-04-09"})

	// When: a download completes without one.
	if err := s.SetDownloaded("known", DownloadedResult{MediaPath: "/m/known.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	// Then: the stored date survives.
	got, err := s.Get("known")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PublishedAt != "2025-04-09" {
		t.Fatalf("published_at = %q, want it preserved", got.PublishedAt)
	}
}

// TestList_sort_missingPublishedAt_fallsBackToCreatedAt asserts a row with no
// known release date (yt-dlp reports no upload_date for some live streams and
// premieres) takes the position its download date implies, interleaved with
// the dated rows rather than sinking to one end of the list.
func TestList_sort_missingPublishedAt_fallsBackToCreatedAt(t *testing.T) {
	// Given: two dated rows around one undated row whose created_at sits
	// between their release dates.
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "recent", PublishedAt: "2026-03-01", CreatedAt: "2026-03-02 00:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "nodate", CreatedAt: "2026-02-01 12:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "older", PublishedAt: "2026-01-01", CreatedAt: "2026-01-02 00:00:00", Status: "downloaded"})

	// When: the list is sorted newest-first.
	got, err := s.List(ListOptions{Sort: "newest"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	// Then: the undated row lands in the middle, not first or last.
	want := []string{"recent", "nodate", "older"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows %+v, want %v", len(got), got, want)
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("newest order = %+v, want %v", got, want)
		}
	}

	// And: the same fallback applies in the other direction.
	got, err = s.List(ListOptions{Sort: "oldest"})
	if err != nil {
		t.Fatalf("list oldest: %v", err)
	}
	if len(got) != 3 || got[1].ID != "nodate" {
		t.Fatalf("oldest order = %+v, want nodate in the middle", got)
	}
}

// TestList_unknownSort_fallsBackToNewest asserts an unrecognized sort value
// from a hand-edited URL yields the default order rather than a SQL error or
// an injected ORDER BY clause.
func TestList_unknownSort_fallsBackToNewest(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "a", CreatedAt: "2026-01-01 00:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "b", CreatedAt: "2026-02-01 00:00:00", Status: "downloaded"})

	got, err := s.List(ListOptions{Sort: "id; DROP TABLE videos"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].ID != "b" {
		t.Fatalf("got %+v, want newest-first fallback", got)
	}
}

// TestList_channelID_scopesToOneChannel asserts channel scoping matches on
// channel_id and, for older rows written before channel ids were recorded,
// falls back to an exact channel_name match.
func TestList_channelID_scopesToOneChannel(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", ChannelID: "UCa", ChannelName: "Alpha", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "v2", ChannelID: "", ChannelName: "Alpha", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "v3", ChannelID: "UCb", ChannelName: "Beta", Status: "downloaded"})

	got, err := s.List(ListOptions{ChannelID: "UCa", ChannelName: "Alpha"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows %+v, want v1 and v2", len(got), got)
	}
	for _, v := range got {
		if v.ID == "v3" {
			t.Fatal("channel scoping leaked another channel's video")
		}
	}
}

// TestList_channelID_withoutChannelName_matchesChannelIDOnly asserts that
// when ChannelName is not supplied, scoping matches strictly on channel_id
// and does not fall back to matching rows by channel_name (that fallback
// only makes sense when the caller has a name to fall back to).
func TestList_channelID_withoutChannelName_matchesChannelIDOnly(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", ChannelID: "UCa", ChannelName: "Alpha", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "v2", ChannelID: "", ChannelName: "Alpha", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "v3", ChannelID: "UCb", ChannelName: "Beta", Status: "downloaded"})

	got, err := s.List(ListOptions{ChannelID: "UCa"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != "v1" {
		t.Fatalf("got %+v, want only v1", got)
	}
}

// TestList_errorsOnCorruptRow asserts a row that fails to scan (here, a
// non-numeric value in an INTEGER column — SQLite's dynamic typing allows
// writing it directly, bypassing the app-level guarantees Upsert provides)
// surfaces as an error rather than a panic or a silently truncated list.
func TestList_errorsOnCorruptRow(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", URL: "https://youtu.be/v1", Status: "downloaded"})
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET watched = 'not-a-number' WHERE id = 'v1'`); err != nil {
		t.Fatalf("corrupt row: %v", err)
	}

	if _, err := s.List(ListOptions{}); err == nil {
		t.Fatal("expected an error scanning a corrupt row")
	}
}

// TestList_errorsOnClosedDB asserts a query failure (here, a closed handle)
// is reported to the caller rather than an empty list masquerading as "no
// videos".
func TestList_errorsOnClosedDB(t *testing.T) {
	s := newTestStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if _, err := s.List(ListOptions{}); err == nil {
		t.Fatal("expected an error listing against a closed db")
	}
}

// TestNextUnclassified_picksOnlySummarizedDownloadedUncategorized covers the
// three conditions the idle classify sweep depends on: a video is a candidate
// only when it is downloaded, still uncategorized, and actually has a summary
// to classify from (the no-transcript case must stay out of the sweep).
func TestNextUnclassified_picksOnlySummarizedDownloadedUncategorized(t *testing.T) {
	s := newTestStore(t)

	// Given: one true candidate plus one disqualified row per condition.
	seed := []struct {
		id, status, summary, category, createdAt string
	}{
		{"v-candidate", "downloaded", "A summary.", "uncategorized", "2026-07-01"},
		{"v-classified", "downloaded", "A summary.", "ai", "2026-07-02"},
		{"v-no-summary", "downloaded", "", "uncategorized", "2026-07-03"},
		{"v-not-downloaded", "queued", "A summary.", "uncategorized", "2026-07-04"},
	}
	for _, v := range seed {
		seedVideo(t, s, Video{ID: v.id, URL: "https://youtu.be/" + v.id, Status: v.status, CreatedAt: v.createdAt})
		if v.summary != "" {
			if err := s.SetSummaryText(v.id, v.summary); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.SetCategory(v.id, v.category); err != nil {
			t.Fatal(err)
		}
	}

	// When/Then: only the candidate is returned.
	got, err := s.NextUnclassified(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "v-candidate" {
		t.Fatalf("NextUnclassified = %v, want v-candidate", got)
	}
	if got.Summary != "A summary." {
		t.Fatalf("candidate summary = %q, want it loaded for the classify call", got.Summary)
	}

	// And: skipping the candidate empties the backlog rather than falling back
	// to a disqualified row.
	got, err = s.NextUnclassified([]string{"v-candidate"})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("NextUnclassified(skip candidate) = %v, want nil", got)
	}
}

// TestNextUnclassified_newestFirstAndSkipsMany asserts ordering and that the
// skip list works with more than one entry (the IN-clause placeholder build).
func TestNextUnclassified_newestFirstAndSkipsMany(t *testing.T) {
	s := newTestStore(t)

	for _, id := range []struct{ id, createdAt string }{
		{"v-old", "2026-07-01"}, {"v-mid", "2026-07-02"}, {"v-new", "2026-07-03"},
	} {
		seedVideo(t, s, Video{ID: id.id, URL: "https://youtu.be/" + id.id, Status: "downloaded", CreatedAt: id.createdAt})
		if err := s.SetSummaryText(id.id, "A summary."); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.NextUnclassified(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "v-new" {
		t.Fatalf("NextUnclassified = %v, want the newest (v-new)", got)
	}

	got, err = s.NextUnclassified([]string{"v-new", "v-mid"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "v-old" {
		t.Fatalf("NextUnclassified(skip 2) = %v, want v-old", got)
	}
}

// TestNextUnclassified_errorsOnClosedDB asserts a query failure is reported to
// the caller rather than a nil video masquerading as "backlog empty" — which
// would silently retire the classify sweep for the rest of the process.
func TestNextUnclassified_errorsOnClosedDB(t *testing.T) {
	s := newTestStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	if _, err := s.NextUnclassified(nil); err == nil {
		t.Fatal("expected an error querying against a closed db")
	}
}

// TestSetCategoryIfUnset_guardsAManualPick is the whole reason the guarded
// write exists: both classifier paths decide to classify from a row read
// before a slow LLM call, so the write must re-check rather than trust that
// decision.
func TestSetCategoryIfUnset_guardsAManualPick(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", URL: "https://youtu.be/v1", Status: "downloaded"})

	// Unset: the classifier's write lands.
	applied, err := s.SetCategoryIfUnset("v1", "ai")
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("applied = false, want the write to land on an uncategorized row")
	}

	// Already set — the state a manual pick leaves behind: the write is a
	// no-op and says so, rather than silently overwriting the human.
	applied, err = s.SetCategoryIfUnset("v1", "gaming")
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("applied = true, want the guard to refuse an already-set row")
	}
	got, err := s.Get("v1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != "ai" {
		t.Fatalf("category = %q, want the existing value kept", got.Category)
	}

	// SetCategory itself stays unconditional: the user is allowed to overwrite
	// the model, never the other way round.
	if err := s.SetCategory("v1", "gaming"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get("v1")
	if got.Category != "gaming" {
		t.Fatalf("category = %q, want gaming — a manual write must not be guarded", got.Category)
	}
}

// categoryManual reads the flag column, which is deliberately not on the Video
// struct: nothing outside the store needs it.
func categoryManual(t *testing.T, s *Store, id string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(),
		`SELECT category_manual FROM videos WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("read category_manual for %s: %v", id, err)
	}
	return n
}

// TestSetCategory_maintainsTheManualFlag pins the rule migration 0004 depends
// on: a real category is the human speaking and survives a bulk reset, while a
// reset to 'uncategorized' (Re-summarize) hands the video back to the
// classifier and must therefore clear the flag too.
func TestSetCategory_maintainsTheManualFlag(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", URL: "https://youtu.be/v1", Status: "downloaded"})
	if err := s.SetSummaryText("v1", "A cycling video."); err != nil {
		t.Fatal(err)
	}

	if got := categoryManual(t, s, "v1"); got != 0 {
		t.Fatalf("category_manual = %d on a fresh row, want 0", got)
	}
	if err := s.SetCategory("v1", "sports"); err != nil {
		t.Fatal(err)
	}
	if got := categoryManual(t, s, "v1"); got != 1 {
		t.Fatalf("category_manual = %d after a manual pick, want 1", got)
	}

	// Flagged and uncategorized at once cannot happen through the UI (the
	// picker has no "clear" entry), but the guard is what makes that a
	// guarantee rather than a convention, so exercise it directly.
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET category = ? WHERE id = 'v1'`, UncategorizedCategory); err != nil {
		t.Fatal(err)
	}
	applied, err := s.SetCategoryIfUnset("v1", "gaming")
	if err != nil {
		t.Fatal(err)
	}
	if applied {
		t.Fatal("applied = true, want the classifier refused on a flagged row")
	}

	// Re-summarize: back to the classifier, flag cleared, and the idle sweep
	// can see it again.
	if err := s.SetCategory("v1", UncategorizedCategory); err != nil {
		t.Fatal(err)
	}
	if got := categoryManual(t, s, "v1"); got != 0 {
		t.Fatalf("category_manual = %d after a reset, want 0", got)
	}
	next, err := s.NextUnclassified(nil)
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.ID != "v1" {
		t.Fatalf("NextUnclassified = %v, want v1 back in the backlog", next)
	}
	applied, err = s.SetCategoryIfUnset("v1", "sports")
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("applied = false, want the classifier's write to land once the flag is clear")
	}
}

// TestClearSummary_wipesTheAnalysisButNotTheStatus asserts ClearSummary is the
// exact counterpart of SetSummary: it removes the three artifacts and the error
// text, and deliberately leaves summary_status for the caller to set, since the
// resulting state differs (pending for a re-summarize, no_transcript for a
// track that turned out to carry no speech).
func TestClearSummary_wipesTheAnalysisButNotTheStatus(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Video{ID: "v1", URL: "u1"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.SetSummary("v1", "prose", `[{"ts":0}]`, `[{"ts":1}]`); err != nil {
		t.Fatalf("set summary: %v", err)
	}
	if err := s.SetSummaryStatus("v1", "error", "boom"); err != nil {
		t.Fatalf("set status: %v", err)
	}

	if err := s.ClearSummary("v1"); err != nil {
		t.Fatalf("clear summary: %v", err)
	}

	got, err := s.Get("v1")
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.Summary != "" || got.Chapters != "" || got.KeyPoints != "" {
		t.Fatalf("expected the artifacts wiped, got summary=%q chapters=%q key_points=%q",
			got.Summary, got.Chapters, got.KeyPoints)
	}
	if got.SummaryError != "" {
		t.Fatalf("expected the stale error cleared, got %q", got.SummaryError)
	}
	if got.SummaryStatus != "error" {
		t.Fatalf("summary_status = %q, want it left for the caller to set", got.SummaryStatus)
	}
}

// TestClearSummary_errorsOnClosedDB asserts a failed wipe is reported rather
// than swallowed — a caller that thinks it cleared the summary but did not
// would leave the resumable worker skipping the summary step forever.
func TestClearSummary_errorsOnClosedDB(t *testing.T) {
	s := newTestStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if err := s.ClearSummary("v1"); err == nil {
		t.Fatal("expected an error clearing against a closed db")
	}
}
