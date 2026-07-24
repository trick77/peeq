package videos

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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
// transcript availability, and a subsequent reprocess must not flip a
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

	// Every filter now hides in-flight rows, so c (downloading) is absent from
	// all of them. The queue is the Queue page's subject, not the Library's.
	all, err := s.List(ListOptions{Filter: "all", Category: ""})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if ids := idsOf(all); len(ids) != 2 || !ids["a"] || !ids["b"] {
		t.Fatalf("list all = %+v, want [a b]", all)
	}

	// "unwatched" is what there is to watch right now: downloaded and not yet
	// watched. It used to also count queued/downloading rows so the Library
	// could double as a watch queue; that job moved to the Queue page.
	unwatched, err := s.List(ListOptions{Filter: "unwatched", Category: ""})
	if err != nil {
		t.Fatalf("list unwatched: %v", err)
	}
	if len(unwatched) != 1 || unwatched[0].ID != "a" {
		t.Fatalf("list unwatched = %+v, want [a]", unwatched)
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

	// "downloading" is no longer a filter value. It must not silently keep
	// working as one, and it must not fail open to "everything either" — an
	// unrecognized value means "all", which now excludes in-flight rows like
	// every other filter does.
	gone, err := s.List(ListOptions{Filter: "downloading", Category: ""})
	if err != nil {
		t.Fatalf("list downloading: %v", err)
	}
	if ids := idsOf(gone); len(ids) != 2 || !ids["a"] || !ids["b"] {
		t.Fatalf(`list "downloading" = %+v, want it treated as all → [a b]`, gone)
	}
}

// TestList_unwatchedVsInProgress pins the split between "never opened" and
// "partially watched": a resume position of zero keeps a row under "unwatched",
// a non-zero one moves it to "in_progress", and the two sets never overlap.
func TestList_unwatchedVsInProgress(t *testing.T) {
	s := New(openTestDB(t))
	// "fresh" is downloaded but never opened (resume stays 0).
	if err := s.Upsert(Video{ID: "fresh", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDownloaded("fresh", DownloadedResult{MediaPath: "/m/fresh.mp4"}); err != nil {
		t.Fatal(err)
	}
	// "partial" is downloaded and played to 30s of 100s — started, not finished
	// (30 < the 90% auto-watch threshold, so it stays unwatched).
	if err := s.Upsert(Video{ID: "partial", URL: "u", DurationSeconds: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDownloaded("partial", DownloadedResult{MediaPath: "/m/partial.mp4"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetResume("partial", 30); err != nil {
		t.Fatal(err)
	}

	unwatched, err := s.List(ListOptions{Filter: "unwatched"})
	if err != nil {
		t.Fatalf("list unwatched: %v", err)
	}
	if len(unwatched) != 1 || unwatched[0].ID != "fresh" {
		t.Fatalf("list unwatched = %+v, want [fresh]", unwatched)
	}

	inProgress, err := s.List(ListOptions{Filter: "in_progress"})
	if err != nil {
		t.Fatalf("list in_progress: %v", err)
	}
	if len(inProgress) != 1 || inProgress[0].ID != "partial" {
		t.Fatalf("list in_progress = %+v, want [partial]", inProgress)
	}

	// Both remain reachable through "all"; neither is watched.
	all, err := s.List(ListOptions{Filter: "all"})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if ids := idsOf(all); len(ids) != 2 || !ids["fresh"] || !ids["partial"] {
		t.Fatalf("list all = %+v, want [fresh partial]", all)
	}
	watched, err := s.List(ListOptions{Filter: "watched"})
	if err != nil {
		t.Fatalf("list watched: %v", err)
	}
	if len(watched) != 0 {
		t.Fatalf("list watched = %+v, want []", watched)
	}
}

// TestList_all_keepsRowsOnlyTheLibraryCanRecover is the guard on how far
// "ready-only" goes. It is tempting to read it as status='downloaded', but the
// Library grid is the ONLY place a failed download can be retried (VideoCard's
// re-download button lives there and nowhere else), and a tombstoned row is the
// watched-history entry the retention sweeper deliberately kept re-downloadable.
// Hiding either would delete the only route back for both. The rule is
// therefore "not in the pipeline", not "playable".
func TestList_all_keepsRowsOnlyTheLibraryCanRecover(t *testing.T) {
	s := New(openTestDB(t))
	for _, id := range []string{"err", "tomb", "queued", "ok"} {
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
	if err := s.SetStatus("queued", "queued", ""); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if err := s.SetDownloaded("tomb", DownloadedResult{MediaPath: "/m/tomb.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := s.Tombstone("tomb"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	all, err := s.List(ListOptions{Filter: "all"})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	ids := idsOf(all)
	if !ids["err"] || !ids["tomb"] || !ids["ok"] {
		t.Errorf("list all = %+v, want it to keep err, tomb and ok", all)
	}
	if ids["queued"] {
		t.Errorf("list all = %+v, want the queued row hidden", all)
	}
}

// TestList_channelScoped_agreesWithArchivedCount pins a mismatch this change
// closes. channels.Store.Stats and the channel list's archived_count have
// always counted status='downloaded' only, while the channel page's Archive tab
// lists with no filter at all — so the badge and the list disagreed whenever
// anything was queued. Excluding in-flight rows from List is what brings them
// back into step, and this test fails if List ever widens again.
func TestList_channelScoped_agreesWithArchivedCount(t *testing.T) {
	s := New(openTestDB(t))
	for _, id := range []string{"done", "busy"} {
		if err := s.Upsert(Video{ID: id, URL: "u", ChannelID: "UC1"}); err != nil {
			t.Fatalf("upsert %s: %v", id, err)
		}
	}
	if err := s.SetDownloaded("done", DownloadedResult{MediaPath: "/m/done.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	if err := s.SetStatus("busy", "downloading", ""); err != nil {
		t.Fatalf("set status: %v", err)
	}

	got, err := s.List(ListOptions{ChannelID: "UC1"})
	if err != nil {
		t.Fatalf("list by channel: %v", err)
	}
	if len(got) != 1 || got[0].ID != "done" {
		t.Fatalf("list by channel = %+v, want [done] to match archived_count = 1", got)
	}
}

// TestList_unwatched_excludesDeadRows pins the other half of the rule: an
// unwatched row whose download failed, or whose media the retention sweeper has
// already reclaimed, is not something to watch. Those rows still belong in
// "all" — that is where they are recovered from — but never in the list of
// things you could press play on.
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

// TestNextUnclassified_picksAnySummarizedUncategorized covers the two
// conditions the idle classify sweep depends on: a video is a candidate when it
// is still uncategorized and actually has a summary to classify from (the
// no-transcript case must stay out of the sweep).
//
// Status is deliberately NOT a condition. Classification reads a title and a
// summary, never the media file, so a tombstoned video — media reclaimed, row
// and summary kept, still listed and still filtered by category — is as
// classifiable as any other and must not be stranded on whatever enum existed
// when it was archived.
func TestNextUnclassified_picksAnySummarizedUncategorized(t *testing.T) {
	s := newTestStore(t)

	// Given: two candidates that differ only in status, plus one disqualified
	// row per real condition.
	seed := []struct {
		id, status, summary, category, createdAt string
	}{
		{"v-tombstoned", "tombstoned", "A summary.", "uncategorized", "2026-07-01"},
		{"v-candidate", "downloaded", "A summary.", "uncategorized", "2026-07-02"},
		{"v-classified", "downloaded", "A summary.", "ai", "2026-07-03"},
		{"v-no-summary", "downloaded", "", "uncategorized", "2026-07-04"},
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

	// And: skipping it falls through to the tombstoned row — status is not a
	// filter — and only then does the backlog empty, rather than a
	// disqualified row being offered.
	got, err = s.NextUnclassified([]string{"v-candidate"})
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != "v-tombstoned" {
		t.Fatalf("NextUnclassified(skip candidate) = %v, want v-tombstoned", got)
	}
	got, err = s.NextUnclassified([]string{"v-candidate", "v-tombstoned"})
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("NextUnclassified(skip both) = %v, want nil", got)
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

// TestResetSetMatchesTheSweep pins migration 0004's reset to the query that is
// supposed to undo it. The migration clears categories in bulk on the promise
// that the summarize worker's idle sweep re-classifies whatever it cleared; if
// the two predicates ever drift, the difference is not a stale category, it is
// data erased with no path back — which is exactly the bug this pairing was
// introduced to prevent.
//
// So rather than restate the rule, this reads the real UPDATE out of the real
// migration file, runs it over a table seeded with every row shape peeq can
// produce, and asserts the rows it cleared are exactly the rows
// NextUnclassified will offer. Same trick as ui/src/enumsync.test.ts, which
// reads category.go instead of mirroring it.
func TestResetSetMatchesTheSweep(t *testing.T) {
	s := newTestStore(t)

	// Given: one row per shape. 'category' is the row's category BEFORE the
	// reset, and 'uncategorized' here is not filler — a no-transcript video
	// really does sit at the column default in production, and it is the shape
	// that catches a "cleared" set computed as "uncategorized afterwards".
	seeds := []struct {
		id, status, summary, summaryStatus, category string
		manual                                       bool
	}{
		{"downloaded", "downloaded", "a summary", "done", "entertainment", false},
		{"tombstoned", "tombstoned", "a summary", "done", "history", false},         // media reclaimed, summary kept
		{"notranscript", "downloaded", "", "no_transcript", "uncategorized", false}, // nothing to classify from
		{"handpicked", "downloaded", "", "no_transcript", "gaming", true},           // the picker's whole reason to exist
		{"queued", "queued", "", "pending", "uncategorized", false},
		{"errored", "error", "a summary", "error", "news", false},
		{"handpicked-summarized", "downloaded", "a summary", "done", "ai", true},
	}
	for _, sd := range seeds {
		seedVideo(t, s, Video{ID: sd.id, URL: "https://youtu.be/" + sd.id, Status: sd.status})
		if _, err := s.db.ExecContext(context.Background(),
			`UPDATE videos SET summary = ?, summary_status = ?, category = ?, category_manual = ? WHERE id = ?`,
			sd.summary, sd.summaryStatus, sd.category, boolToInt(sd.manual), sd.id); err != nil {
			t.Fatal(err)
		}
	}
	before := idsWithCategory(t, s, UncategorizedCategory)

	// When: the migration's own UPDATE runs. The test DB is already at 0004,
	// so replaying just this statement is what an upgrade does to the data.
	if _, err := s.db.ExecContext(context.Background(), migration0004Update(t)); err != nil {
		t.Fatalf("replay 0004 reset: %v", err)
	}

	// Then: the set the reset CHANGED — not the set that reads 'uncategorized'
	// now, which would also count rows that were already there and could never
	// be reclassified — equals the set the sweep offers.
	cleared := minusSet(idsWithCategory(t, s, UncategorizedCategory), before)
	reachable := []string{}
	for i := 0; i <= len(seeds); i++ {
		v, err := s.NextUnclassified(reachable)
		if err != nil {
			t.Fatal(err)
		}
		if v == nil {
			break
		}
		reachable = append(reachable, v.ID)
		if i == len(seeds) {
			// Bounded on purpose: an unbounded drain turns a broken skip clause
			// into a hung suite instead of a failed assertion.
			t.Fatalf("NextUnclassified still returning rows after %d turns: %v", i+1, reachable)
		}
	}
	if !sameSet(cleared, reachable) {
		t.Fatalf("reset cleared %v but the sweep can reach %v — a row in the difference is either\n"+
			"erased with no way back, or left on the pre-expansion enum forever", cleared, reachable)
	}
	// And the rule both sides are meant to encode, stated once so a mutual
	// drift (both sides wrong the same way) still fails.
	if !sameSet(cleared, []string{"downloaded", "tombstoned", "errored"}) {
		t.Fatalf("cleared %v, want every row that has a summary and is not a hand pick, and only those", cleared)
	}
	// And the hand picks are untouched — the column's entire purpose.
	for _, id := range []string{"handpicked", "handpicked-summarized"} {
		var got string
		if err := s.db.QueryRowContext(context.Background(),
			`SELECT category FROM videos WHERE id = ?`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got == UncategorizedCategory {
			t.Fatalf("%s was cleared; a flagged row must survive a bulk reset", id)
		}
	}
}

// minusSet returns the ids in a that are not in b.
func minusSet(a, b []string) []string {
	drop := map[string]bool{}
	for _, s := range b {
		drop[s] = true
	}
	out := []string{}
	for _, s := range a {
		if !drop[s] {
			out = append(out, s)
		}
	}
	return out
}

// migration0004Update returns the UPDATE statement from the real migration
// file, so this test cannot pass against a migration that says something else.
func migration0004Update(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "store", "migrations", "0004_category_manual.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Comments first, THEN split: a semicolon inside the migration's prose
	// would otherwise cut a statement in half, and the half that survives may
	// still start with UPDATE — a truncated WHERE that quietly clears more
	// than the real migration does.
	for _, stmt := range strings.Split(stripSQLComments(string(body)), ";") {
		if s := strings.TrimSpace(stmt); strings.HasPrefix(s, "UPDATE") {
			return s
		}
	}
	t.Fatalf("no UPDATE statement in %s", path)
	return ""
}

func stripSQLComments(s string) string {
	var b strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func idsWithCategory(t *testing.T, s *Store, category string) []string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id FROM videos WHERE category = ? ORDER BY id`, category)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[s]++
	}
	for _, s := range b {
		seen[s]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// execTest runs a raw statement against the test database. Used to put a row
// into a state the store's own API deliberately cannot produce — here, an
// aged or never-set sponsorblock_refreshed_at.
func execTest(t *testing.T, s *Store, query string) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(), query); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// TestClaimSponsorblockStale_ordersNeverFetchedFirst covers the backfill claim
// order: a video that has never been looked up (empty
// sponsorblock_refreshed_at) has to come before one that was merely looked up
// a long time ago, since the first has no segments at all while the second
// only has slightly old ones.
func TestClaimSponsorblockStale_ordersNeverFetchedFirst(t *testing.T) {
	// Given: three downloaded videos — one fetched recently, one long ago,
	// one never.
	s := newTestStore(t)
	for _, id := range []string{"fresh", "old", "never"} {
		seedVideo(t, s, Video{ID: id, URL: "u"})
		if err := s.SetDownloaded(id, DownloadedResult{MediaPath: "/m/" + id + ".mp4"}); err != nil {
			t.Fatalf("set downloaded %s: %v", id, err)
		}
	}
	execTest(t, s, `UPDATE videos SET sponsorblock_refreshed_at = datetime('now','-90 days') WHERE id='old'`)
	execTest(t, s, `UPDATE videos SET sponsorblock_refreshed_at = '' WHERE id='never'`)

	// When: the worker claims a batch.
	got, err := s.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Then: the never-fetched one leads, the long-ago one follows, and the
	// freshly-stamped one is not due at all.
	if len(got) != 2 {
		t.Fatalf("claimed %+v, want the never-fetched and the stale one", got)
	}
	if got[0].ID != "never" || got[1].ID != "old" {
		t.Fatalf("claimed %+v, want never then old", got)
	}
}

// TestClaimSponsorblockStale_skipsUndownloadedAndRespectsLimit: only videos
// with media on disk are worth reading segments for, and the claim must stay
// bounded so a large library isn't pulled into memory at once.
func TestClaimSponsorblockStale_skipsUndownloadedAndRespectsLimit(t *testing.T) {
	// Given: two downloaded videos, one queued one, and one tombstoned one.
	s := newTestStore(t)
	for _, id := range []string{"d1", "d2", "queued", "gone"} {
		seedVideo(t, s, Video{ID: id, URL: "u"})
	}
	for _, id := range []string{"d1", "d2", "gone"} {
		if err := s.SetDownloaded(id, DownloadedResult{MediaPath: "/m/" + id + ".mp4"}); err != nil {
			t.Fatalf("set downloaded %s: %v", id, err)
		}
	}
	execTest(t, s, `UPDATE videos SET sponsorblock_refreshed_at = '' WHERE id IN ('d1','d2','queued','gone')`)
	if err := s.Tombstone("gone"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	// When/Then: the queued and tombstoned rows never appear, and the limit
	// holds.
	got, err := s.ClaimSponsorblockStale(1)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("claimed %+v, want exactly the limit", got)
	}
	all, err := s.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("claimed %+v, want only the two downloaded videos", all)
	}
	for _, c := range all {
		if c.ID == "queued" || c.ID == "gone" {
			t.Fatalf("claimed %+v, want neither the queued nor the tombstoned video", all)
		}
	}
}

// TestClaimSponsorblockStale_carriesDuration: the client needs the duration to
// reject segments submitted against a different cut of the video, so the claim
// has to carry it rather than the worker looking it up again.
func TestClaimSponsorblockStale_carriesDuration(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v", URL: "u", DurationSeconds: 612})
	if err := s.SetDownloaded("v", DownloadedResult{MediaPath: "/m/v.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	execTest(t, s, `UPDATE videos SET sponsorblock_refreshed_at = '' WHERE id='v'`)

	got, err := s.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 1 || got[0].DurationSeconds != 612 {
		t.Fatalf("claimed %+v, want the duration carried through", got)
	}
}

// TestSetSponsorblockSegments_stampsEvenWhenEmpty: recording "this video has
// no segments" is what takes it out of the claim set. Without the stamp the
// worker would ask about the same video every minute forever.
func TestSetSponsorblockSegments_stampsEvenWhenEmpty(t *testing.T) {
	// Given: a downloaded video that has never been looked up.
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v", URL: "u"})
	if err := s.SetDownloaded("v", DownloadedResult{MediaPath: "/m/v.mp4"}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}
	execTest(t, s, `UPDATE videos SET sponsorblock_refreshed_at = '' WHERE id='v'`)

	// When: the lookup comes back empty.
	if err := s.SetSponsorblockSegments("v", ""); err != nil {
		t.Fatalf("set segments: %v", err)
	}

	// Then: the column holds the documented empty-array shape, and the video
	// is no longer claimable.
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SponsorblockSegments != "[]" {
		t.Fatalf("segments = %q, want %q", got.SponsorblockSegments, "[]")
	}
	claimed, err := s.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %+v, want none after the stamp", claimed)
	}
}

// TestSetSponsorblockSegments_storesJSON is the populated case.
func TestSetSponsorblockSegments_storesJSON(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v", URL: "u"})
	segments := `[{"category":"sponsor","start_time":10,"end_time":25}]`
	if err := s.SetSponsorblockSegments("v", segments); err != nil {
		t.Fatalf("set segments: %v", err)
	}
	got, err := s.Get("v")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SponsorblockSegments != segments {
		t.Fatalf("segments = %q, want %q", got.SponsorblockSegments, segments)
	}
}

// TestSetDownloaded_stampsSponsorblockRefresh: yt-dlp already asked
// SponsorBlock during the download, so the backfill worker must not
// immediately ask again for a video whose segments just arrived.
func TestSetDownloaded_stampsSponsorblockRefresh(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v", URL: "u"})
	if err := s.SetDownloaded("v", DownloadedResult{
		MediaPath:            "/m/v.mp4",
		SponsorblockSegments: `[{"category":"sponsor","start_time":1,"end_time":2}]`,
	}); err != nil {
		t.Fatalf("set downloaded: %v", err)
	}

	claimed, err := s.ClaimSponsorblockStale(10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %+v, want a just-downloaded video not to be re-fetched", claimed)
	}
}

// seedChannel inserts a channels metadata-cache row directly. The videos
// package must not import channels (that would cycle), so tests write the row
// via raw SQL against the shared db.
func seedChannel(t *testing.T, s *Store, id, name string) {
	t.Helper()
	if _, err := s.db.ExecContext(context.Background(),
		`INSERT INTO channels (id, name) VALUES (?, ?)`, id, name); err != nil {
		t.Fatalf("seed channel %s: %v", id, err)
	}
}

// TestChannelName_resolvesFromChannelsCache pins the fix for videos that
// arrive through a channel scan/subscription: their own videos.channel_name
// is never written, so both Get and List must fall back to the resolved name
// in the channels metadata cache rather than surfacing the raw UCxxxx id.
func TestChannelName_resolvesFromChannelsCache(t *testing.T) {
	s := newTestStore(t)
	seedChannel(t, s, "UC77UtoyivVHkpApL0wGfH5w", "Real Channel Name")
	seedVideo(t, s, Video{
		ID: "v", URL: "u", Title: "t",
		ChannelID: "UC77UtoyivVHkpApL0wGfH5w", // ChannelName deliberately empty
		Status:    "downloaded",
	})

	// Get (the Player detail path) resolves it.
	got, err := s.Get("v")
	if err != nil || got == nil {
		t.Fatalf("get: %v (row=%v)", err, got)
	}
	if got.ChannelName != "Real Channel Name" {
		t.Fatalf("Get ChannelName = %q, want resolved name", got.ChannelName)
	}

	// List (the Library grid path) resolves it too.
	list, err := s.List(ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ChannelName != "Real Channel Name" {
		t.Fatalf("List ChannelName = %+v, want resolved name", list)
	}
}

// TestChannelName_fallbacks covers the two other COALESCE branches: the
// video's own channel_name wins when present, and the bare id remains the last
// resort when the channel is genuinely unresolved (no cache row / blank name).
func TestChannelName_fallbacks(t *testing.T) {
	s := newTestStore(t)

	// Own channel_name present -> wins over the cache.
	seedChannel(t, s, "UCcache", "Cache Name")
	seedVideo(t, s, Video{ID: "own", URL: "u", ChannelID: "UCcache",
		ChannelName: "Own Name", Status: "downloaded"})

	// No cache row at all -> falls through to the id.
	seedVideo(t, s, Video{ID: "bare", URL: "u", ChannelID: "UCunknown",
		Status: "downloaded"})

	// Cache row exists but its name is blank -> also falls through to the id.
	seedChannel(t, s, "UCblank", "")
	seedVideo(t, s, Video{ID: "blank", URL: "u", ChannelID: "UCblank",
		Status: "downloaded"})

	want := map[string]string{
		"own":   "Own Name",
		"bare":  "UCunknown",
		"blank": "UCblank",
	}
	for id, exp := range want {
		got, err := s.Get(id)
		if err != nil || got == nil {
			t.Fatalf("get %s: %v (row=%v)", id, err, got)
		}
		if got.ChannelName != exp {
			t.Fatalf("Get(%s) ChannelName = %q, want %q", id, got.ChannelName, exp)
		}
	}
}
