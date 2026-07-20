package channels

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/store"
)

// newTestStore opens a migrated temp DB and returns a channels Store backed
// by it, mirroring internal/jobs/store_test.go's openTestDB helper.
func newTestStore(t *testing.T) *Store {
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

// TestList_excludesCacheOnlyRows asserts a channel row that exists only as a
// metadata cache entry (never tracked by the user) is invisible to every
// ?filter= value the channels list supports. If this regresses, the user's
// Channels page fills up with channels they merely clicked on once.
func TestList_excludesCacheOnlyRows(t *testing.T) {
	s := newTestStore(t)

	if err := s.Upsert(Channel{ID: "UCcache", Name: "Cache Only"}); err != nil {
		t.Fatalf("upsert cache row: %v", err)
	}
	if err := s.Upsert(Channel{ID: "UCtracked", Name: "Tracked"}); err != nil {
		t.Fatalf("upsert tracked row: %v", err)
	}
	if err := s.Track("UCtracked", "2026-07-20 10:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}

	for _, filter := range []string{"all", "tracked", "subscribed", "autodownload"} {
		items, err := s.List(filter)
		if err != nil {
			t.Fatalf("list %s: %v", filter, err)
		}
		for _, it := range items {
			if it.ID == "UCcache" {
				t.Fatalf("filter %q returned cache-only channel", filter)
			}
		}
	}

	all, err := s.List("all")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 1 || all[0].ID != "UCtracked" {
		t.Fatalf("list all = %+v, want only UCtracked", all)
	}
}

// TestGet_returnsCacheOnlyRow asserts Get still finds a cache-only row — the
// channel page reads its metadata through Get even when untracked, so Get
// must NOT inherit List's tracked_at filter.
func TestGet_returnsCacheOnlyRow(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Channel{ID: "UCcache", Name: "Cache Only", Description: "hello"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	c, err := s.Get("UCcache")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c == nil {
		t.Fatal("get returned nil for a cache-only row")
	}
	if c.TrackedAt != "" {
		t.Fatalf("TrackedAt = %q, want empty for an untracked row", c.TrackedAt)
	}
	if c.Description != "hello" {
		t.Fatalf("Description = %q, want %q", c.Description, "hello")
	}
}

// TestUpsert_preservesTrackedAt asserts re-caching a channel's metadata (which
// happens on every visit-triggered resolve) never silently untracks it.
func TestUpsert_preservesTrackedAt(t *testing.T) {
	s := newTestStore(t)
	if err := s.Upsert(Channel{ID: "UCx", Name: "Before"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.Track("UCx", "2026-07-20 10:00:00"); err != nil {
		t.Fatalf("track: %v", err)
	}
	if err := s.Upsert(Channel{ID: "UCx", Name: "After"}); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	c, err := s.Get("UCx")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c.TrackedAt == "" {
		t.Fatal("re-upsert cleared tracked_at")
	}
	if c.Name != "After" {
		t.Fatalf("Name = %q, want refreshed to %q", c.Name, "After")
	}
}

func TestChannels_trackSubscribeClaim(t *testing.T) {
	st := newTestStore(t)

	if err := st.Upsert(Channel{ID: "UC1", Handle: "@one", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Upsert(Channel{ID: "UC2", Name: "Two"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Track("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := st.Track("UC2", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	// Tracked only → ClaimDue returns nothing.
	if sub, err := st.ClaimDue("2999-01-01 00:00:00"); err != nil || sub != nil {
		t.Fatalf("no subscriptions yet: sub=%v err=%v", sub, err)
	}
	// Subscribe both, UC1 due earlier.
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC2", "2000-06-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	sub, err := st.ClaimDue("2999-01-01 00:00:00")
	if err != nil || sub == nil || sub.ChannelID != "UC1" {
		t.Fatalf("want UC1 first, got %v err=%v", sub, err)
	}
	if sub.BaselinedAt != "" {
		t.Fatalf("new subscription must have NULL baselined_at, got %q", sub.BaselinedAt)
	}
	// Config update reflected in List.
	if ok, err := st.UpdateConfig("UC1", true, "bestvideo+bestaudio"); err != nil || !ok {
		t.Fatalf("update config: ok=%v err=%v", ok, err)
	}
	items, err := st.List("subscribed")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("subscribed count = %d, want 2", len(items))
	}
}

// mustExec runs a statement against the raw db handle, failing the test on
// error. Used to seed rows the channels Store has no writer for (videos,
// download_jobs, channel_videos) directly in a cascade test.
func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

// TestDeleteCascade_removesEverything asserts DeleteCascade removes the
// channel and every row that belongs to it — its subscription, ledger rows,
// downloaded videos, and (via videos' FK cascade) their download jobs — in one
// go. VideoRefs must first surface the video's media path so the caller can
// unlink the file after the row is gone.
func TestDeleteCascade_removesEverything(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	st.Upsert(Channel{ID: "UC1", Name: "One"})
	st.Subscribe("UC1", "2000-01-01 00:00:00")
	// A downloaded video + a job + a ledger row, all for UC1.
	mustExec(t, db, `INSERT INTO videos (id,url,channel_id,status,media_path) VALUES ('v1','u','UC1','downloaded','/m/v1.mp4')`)
	mustExec(t, db, `INSERT INTO download_jobs (video_id, state) VALUES ('v1','done')`)
	mustExec(t, db, `INSERT INTO channel_videos (video_id, channel_id, state) VALUES ('v1','UC1','queued')`)

	refs, err := st.VideoRefs("UC1")
	if err != nil || len(refs) != 1 || refs[0].MediaPath != "/m/v1.mp4" {
		t.Fatalf("refs = %+v err=%v", refs, err)
	}
	if err := st.DeleteCascade("UC1"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM channels WHERE id='UC1'`,
		`SELECT count(*) FROM subscriptions WHERE channel_id='UC1'`,
		`SELECT count(*) FROM channel_videos WHERE channel_id='UC1'`,
		`SELECT count(*) FROM videos WHERE channel_id='UC1'`,
		`SELECT count(*) FROM download_jobs WHERE video_id='v1'`,
	} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%q → n=%d err=%v, want 0", q, n, err)
		}
	}
}

// TestDeleteCascade_purgesVecAndFTSChunks asserts DeleteCascade also removes
// the vec_chunks (vec0 virtual table) and fts_chunks (fts5 virtual table) rows
// belonging to the channel's videos. Neither virtual table can ride an FK
// cascade or trigger, so without an explicit purge these rows are orphaned
// forever once their transcript_chunks parent is gone — this test fails
// (vec/fts counts stay 1) before the fix.
func TestDeleteCascade_purgesVecAndFTSChunks(t *testing.T) {
	st := newTestStore(t)
	db := st.DB()
	ctx := context.Background()
	st.Upsert(Channel{ID: "UC1", Name: "One"})
	mustExec(t, db, `INSERT INTO videos (id,url,channel_id,status) VALUES ('v1','u','UC1','downloaded')`)

	ragStore := rag.NewStore(db)
	vec := make([]float32, 1536)
	if err := ragStore.ReplaceVideoChunks(ctx, "v1", "e5", 1536, []rag.ChunkRow{
		{Ordinal: 0, Text: "x", StartSeconds: 0, TokenCount: 1},
	}, [][]float32{vec}); err != nil {
		t.Fatalf("seed chunks: %v", err)
	}

	var beforeVec, beforeFTS int
	if err := db.QueryRow(`SELECT count(*) FROM vec_chunks`).Scan(&beforeVec); err != nil || beforeVec != 1 {
		t.Fatalf("vec_chunks before delete = %d err=%v, want 1", beforeVec, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM fts_chunks`).Scan(&beforeFTS); err != nil || beforeFTS != 1 {
		t.Fatalf("fts_chunks before delete = %d err=%v, want 1", beforeFTS, err)
	}

	if err := st.DeleteCascade("UC1"); err != nil {
		t.Fatal(err)
	}

	var afterVec, afterFTS, afterChunks, afterVideos int
	if err := db.QueryRow(`SELECT count(*) FROM vec_chunks`).Scan(&afterVec); err != nil || afterVec != 0 {
		t.Fatalf("vec_chunks after delete = %d err=%v, want 0 (orphaned rows leaked)", afterVec, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM fts_chunks`).Scan(&afterFTS); err != nil || afterFTS != 0 {
		t.Fatalf("fts_chunks after delete = %d err=%v, want 0 (orphaned rows leaked)", afterFTS, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM transcript_chunks WHERE video_id='v1'`).Scan(&afterChunks); err != nil || afterChunks != 0 {
		t.Fatalf("transcript_chunks after delete = %d err=%v, want 0", afterChunks, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM videos WHERE id='v1'`).Scan(&afterVideos); err != nil || afterVideos != 0 {
		t.Fatalf("videos after delete = %d err=%v, want 0", afterVideos, err)
	}
}

func TestChannels_unsubscribeKeepsChannel(t *testing.T) {
	st := newTestStore(t)
	st.Upsert(Channel{ID: "UC1", Name: "One"})
	st.Track("UC1", "2000-01-01 00:00:00")
	st.Subscribe("UC1", "2000-01-01 00:00:00")
	ok, err := st.Unsubscribe("UC1")
	if err != nil || !ok {
		t.Fatalf("unsubscribe: ok=%v err=%v", ok, err)
	}
	if c, err := st.Get("UC1"); err != nil || c == nil {
		t.Fatal("channel must stay tracked after unsubscribe")
	}
	items, _ := st.List("tracked")
	if len(items) != 1 {
		t.Fatalf("tracked count = %d, want 1", len(items))
	}
}

// TestChannels_listAutodownloadFilter pins the "autodownload" filter to the
// subscribed-AND-autodownload-on subset. The unsubscribed channel is the
// interesting case: its LEFT JOIN leaves s.autodownload NULL, and the filter
// relies on `NULL = 1` being untrue in SQLite to exclude it.
func TestChannels_listAutodownloadFilter(t *testing.T) {
	st := newTestStore(t)
	for _, c := range []Channel{
		{ID: "UC1", Name: "Tracked only"},
		{ID: "UC2", Name: "Subscribed, autodownload off"},
		{ID: "UC3", Name: "Subscribed, autodownload on"},
	} {
		if err := st.Upsert(c); err != nil {
			t.Fatalf("upsert %s: %v", c.ID, err)
		}
		if err := st.Track(c.ID, "2000-01-01 00:00:00"); err != nil {
			t.Fatalf("track %s: %v", c.ID, err)
		}
	}
	for _, id := range []string{"UC2", "UC3"} {
		if err := st.Subscribe(id, "2000-01-01 00:00:00"); err != nil {
			t.Fatalf("subscribe %s: %v", id, err)
		}
	}
	if _, err := st.UpdateConfig("UC3", true, ""); err != nil {
		t.Fatalf("update config: %v", err)
	}

	items, err := st.List("autodownload")
	if err != nil {
		t.Fatalf("list autodownload: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("autodownload count = %d, want 1", len(items))
	}
	if items[0].ID != "UC3" {
		t.Fatalf("autodownload id = %q, want UC3", items[0].ID)
	}
	if !items[0].Autodownload {
		t.Fatal("listed item must report autodownload on")
	}
}

// TestUpsert_blankFieldsDoNotEraseCachedMetadata asserts a partial re-upsert
// — which is exactly what the track path does, passing only id/name/handle —
// cannot wipe metadata a previous resolve already cached. Without the
// COALESCE(NULLIF(...)) in Upsert this silently blanks the description and
// both image paths.
func TestUpsert_blankFieldsDoNotEraseCachedMetadata(t *testing.T) {
	s := newTestStore(t)

	if err := s.Upsert(Channel{
		ID:          "UCx",
		Name:        "Full",
		Handle:      "@full",
		Description: "a description",
		AvatarPath:  ".channels/UCx/avatar.jpg",
		BannerPath:  ".channels/UCx/banner.jpg",
		ResolvedAt:  "2026-07-20 10:00:00",
	}); err != nil {
		t.Fatalf("seed upsert: %v", err)
	}

	// A partial re-upsert: every metadata field is the Go zero value.
	if err := s.Upsert(Channel{ID: "UCx", Name: "Full", Handle: "@full"}); err != nil {
		t.Fatalf("partial upsert: %v", err)
	}

	c, err := s.Get("UCx")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if c.Description != "a description" {
		t.Fatalf("Description = %q, want it preserved", c.Description)
	}
	if c.AvatarPath != ".channels/UCx/avatar.jpg" {
		t.Fatalf("AvatarPath = %q, want it preserved", c.AvatarPath)
	}
	if c.BannerPath != ".channels/UCx/banner.jpg" {
		t.Fatalf("BannerPath = %q, want it preserved", c.BannerPath)
	}
	if c.ResolvedAt != "2026-07-20 10:00:00" {
		t.Fatalf("ResolvedAt = %q, want it preserved", c.ResolvedAt)
	}
}
