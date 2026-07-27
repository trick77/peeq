package videos

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

// Shared test helpers for the whole videos package live here, alongside the
// tests for the row shape and the two whole-library reads (Upsert, Get, List).
// The lifecycle-specific tests sit beside the source file they cover:
// store_download_test.go, store_probe_test.go, store_sponsorblock_test.go,
// store_summary_test.go and store_watch_test.go.

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
	// downloaded_at is normally written by SetDownloaded as datetime('now'),
	// which no test can position relative to another row — so sort fixtures set
	// it directly, the same way they set created_at.
	if v.DownloadedAt != "" {
		if _, err := s.db.ExecContext(context.Background(),
			`UPDATE videos SET downloaded_at = ? WHERE id = ?`, v.DownloadedAt, v.ID); err != nil {
			t.Fatalf("seed video %s downloaded_at: %v", v.ID, err)
		}
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

// ids is the ordered id list of a result, for comparing against a want slice.
func ids(vs []Video) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return out
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

// categoryResets returns the bulk-reclassification UPDATE from every migration
// that carries one, keyed by filename, read out of the real files so a test
// cannot pass against a migration that says something else.
//
// It scans the whole directory rather than naming 0004: a reset is written
// every time the enum or the classify prompt changes enough to invalidate past
// answers (0004 for the enum growing, 0015 for the category hints), and a
// helper that names one file silently stops covering the next one. Finding
// none is a failure — the caller's whole assertion would otherwise vacuously
// pass if the scan ever broke.
func categoryResets(t *testing.T) map[string]string {
	t.Helper()
	dir := filepath.Join("..", "store", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	found := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		// Comments first, THEN split: a semicolon inside the migration's prose
		// would otherwise cut a statement in half, and the half that survives
		// may still start with UPDATE — a truncated WHERE that quietly clears
		// more than the real migration does.
		for _, stmt := range strings.Split(stripSQLComments(string(body)), ";") {
			s := strings.TrimSpace(stmt)
			if !strings.HasPrefix(s, "UPDATE") || !strings.Contains(s, UncategorizedCategory) {
				continue
			}
			if prev, dup := found[e.Name()]; dup {
				t.Fatalf("%s has two category resets:\n%s\n%s", e.Name(), prev, s)
			}
			found[e.Name()] = s
		}
	}
	if len(found) == 0 {
		t.Fatalf("no category reset found in any migration under %s", dir)
	}
	return found
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
	if _, err := s.SetWatched("b", true); err != nil {
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
	if _, _, err := s.SetResume("partial", 30, nil); err != nil {
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
			if _, err := s.SetWatched(v.id, true); err != nil {
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
	// Three videos whose SIX orderings are all distinct — title, duration,
	// published_at and downloaded_at each rank them differently, and no two
	// sort keys land on the same permutation. That is what makes a dropped or
	// mis-mapped sort key fail here: with a laxer fixture, "oldest" quietly
	// returning the "longest" clause would still pass.
	//
	// downloaded_at runs opposite to neither of the others by accident: it is
	// chosen so added-date order differs from BOTH release-date order and
	// created_at order, which is the whole point of the two dimensions being
	// separate sorts.
	seedVideo(t, s, Video{ID: "c1", Title: "Charlie", DurationSeconds: 300, PublishedAt: "2026-03-01", CreatedAt: "2026-01-01 00:00:00", DownloadedAt: "2026-02-05 00:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "a2", Title: "Alpha", DurationSeconds: 100, PublishedAt: "2026-02-01", CreatedAt: "2026-02-01 00:00:00", DownloadedAt: "2026-03-05 00:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "b3", Title: "Bravo", DurationSeconds: 200, PublishedAt: "2026-01-01", CreatedAt: "2026-03-01 00:00:00", DownloadedAt: "2026-01-05 00:00:00", Status: "downloaded"})

	cases := []struct {
		sort string
		want []string
	}{
		{"newest", []string{"c1", "a2", "b3"}},       // published_at DESC
		{"oldest", []string{"b3", "a2", "c1"}},       // published_at ASC
		{"added_newest", []string{"a2", "c1", "b3"}}, // downloaded_at DESC
		{"added_oldest", []string{"b3", "c1", "a2"}}, // downloaded_at ASC
		{"longest", []string{"c1", "b3", "a2"}},      // duration DESC
		{"title", []string{"a2", "b3", "c1"}},        // title NOCASE ASC
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

// TestList_newest_ranksByReleaseDate pins the DEFAULT ordering: release date,
// newest first, with created_at as the fallback for a row yt-dlp gave no date
// for.
//
// This is the guard against changing it a third time. It was repointed at
// downloaded_at once (#139) on the argument that "new to this library" is what
// the grid should answer; run against a real library that ordering was wrong,
// and it was reverted. Reasoning lost to evidence — leave it alone.
func TestList_newest_ranksByReleaseDate(t *testing.T) {
	// Given: an old talk fetched recently, and a fresh upload fetched long ago.
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "oldtalk", PublishedAt: "2019-05-01", CreatedAt: "2026-03-01 00:00:00", DownloadedAt: "2026-03-01 09:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "freshupload", PublishedAt: "2026-02-20", CreatedAt: "2026-02-20 00:00:00", DownloadedAt: "2026-02-20 09:00:00", Status: "downloaded"})

	// When/Then: the default ranks by when it AIRED, so the fresh upload wins
	// even though the old talk arrived more recently.
	got, err := s.List(ListOptions{Sort: "newest"})
	if err != nil {
		t.Fatalf("list newest: %v", err)
	}
	if want := []string{"freshupload", "oldtalk"}; !slices.Equal(ids(got), want) {
		t.Fatalf("newest order = %v, want %v", ids(got), want)
	}

	// ...and the opt-in added-date sort is where "what arrived last" lives.
	got, err = s.List(ListOptions{Sort: "added_newest"})
	if err != nil {
		t.Fatalf("list added_newest: %v", err)
	}
	if want := []string{"oldtalk", "freshupload"}; !slices.Equal(ids(got), want) {
		t.Fatalf("added_newest order = %v, want %v", ids(got), want)
	}
}

// TestList_newest_missingReleaseDate_fallsBackToCreatedAt asserts a row with no
// known release date (yt-dlp reports none for some live streams and premieres)
// takes the position its insertion date implies, interleaved with the dated
// rows rather than sinking to one end of the grid. The fixture puts `nodate`'s
// created_at deliberately BETWEEN the two real air dates.
func TestList_newest_missingReleaseDate_fallsBackToCreatedAt(t *testing.T) {
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "recent", PublishedAt: "2026-03-01", CreatedAt: "2026-03-02 00:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "nodate", CreatedAt: "2026-02-01 12:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "older", PublishedAt: "2026-01-01", CreatedAt: "2026-01-02 00:00:00", Status: "downloaded"})

	got, err := s.List(ListOptions{Sort: "newest"})
	if err != nil {
		t.Fatalf("list newest: %v", err)
	}
	if want := []string{"recent", "nodate", "older"}; !slices.Equal(ids(got), want) {
		t.Fatalf("newest order = %v, want %v", ids(got), want)
	}

	// And the same fallback applies in the other direction.
	got, err = s.List(ListOptions{Sort: "oldest"})
	if err != nil {
		t.Fatalf("list oldest: %v", err)
	}
	if want := []string{"older", "nodate", "recent"}; !slices.Equal(ids(got), want) {
		t.Fatalf("oldest order = %v, want %v", ids(got), want)
	}
}

// TestList_addedSort_undatedRowsSortLast covers the opt-in added-date pair. An
// 'error' row never downloaded, so it has no added date — and the Library still
// lists it (see notInFlight) so it can be retried. Its created_at sits between
// the two real download times, so a row landing in the middle would mean the
// clause had fallen back to created_at rather than ranking undated rows last.
func TestList_addedSort_undatedRowsSortLast(t *testing.T) {
	// Given: two downloaded rows and one failed row that never downloaded.
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "recent", CreatedAt: "2026-03-01 00:00:00", DownloadedAt: "2026-03-02 00:00:00", Status: "downloaded"})
	seedVideo(t, s, Video{ID: "failed", CreatedAt: "2026-02-01 12:00:00", Status: "error"})
	seedVideo(t, s, Video{ID: "older", CreatedAt: "2026-01-01 00:00:00", DownloadedAt: "2026-01-02 00:00:00", Status: "downloaded"})

	// When/Then: last in both directions, never in the middle.
	got, err := s.List(ListOptions{Sort: "added_newest"})
	if err != nil {
		t.Fatalf("list added_newest: %v", err)
	}
	if want := []string{"recent", "older", "failed"}; !slices.Equal(ids(got), want) {
		t.Fatalf("added_newest order = %v, want %v", ids(got), want)
	}

	got, err = s.List(ListOptions{Sort: "added_oldest"})
	if err != nil {
		t.Fatalf("list added_oldest: %v", err)
	}
	if want := []string{"older", "recent", "failed"}; !slices.Equal(ids(got), want) {
		t.Fatalf("added_oldest order = %v, want %v", ids(got), want)
	}
}

// TestUpsert_neverClearsPublishedAtOrDescription guards the write side of the
// same promise. Several callers legitimately have no date to offer — scan's
// enqueueAuto seeds from a flat listing, the approve-from-inbox path passes
// id/url/title/duration only — and the ON CONFLICT clause used to assign
// excluded.published_at straight through, so any of them silently blanked a
// good air date on a re-seen id. Fixing the sort would mean nothing if a scan
// could still erase the value it sorts on.
func TestUpsert_neverClearsPublishedAtOrDescription(t *testing.T) {
	// Given: a row that knows its air date and description.
	s := newTestStore(t)
	seedVideo(t, s, Video{ID: "v1", URL: "u", Title: "T", PublishedAt: "2019-05-01", Description: "the real description"})

	// When: a metadata-poor caller upserts the same id with neither.
	if err := s.Upsert(Video{ID: "v1", URL: "u", Title: "T"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Then: both survive.
	got, err := s.Get("v1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PublishedAt != "2019-05-01" {
		t.Fatalf("published_at = %q, want it preserved", got.PublishedAt)
	}
	if got.Description != "the real description" {
		t.Fatalf("description = %q, want it preserved", got.Description)
	}

	// And: a caller that DOES have them still updates them — never clearing
	// must not become never writing.
	if err := s.Upsert(Video{ID: "v1", URL: "u", Title: "T", PublishedAt: "2020-01-02", Description: "better"}); err != nil {
		t.Fatalf("upsert with values: %v", err)
	}
	got, err = s.Get("v1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.PublishedAt != "2020-01-02" || got.Description != "better" {
		t.Fatalf("published_at/description = %q/%q, want the new values", got.PublishedAt, got.Description)
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
