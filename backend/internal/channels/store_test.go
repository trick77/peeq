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

func TestChannels_trackSubscribeClaim(t *testing.T) {
	st := newTestStore(t)

	if err := st.Upsert(Channel{ID: "UC1", Handle: "@one", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Upsert(Channel{ID: "UC2", Name: "Two"}); err != nil {
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
	adOn, format := true, "bestvideo+bestaudio"
	if _, _, ok, err := st.UpdateConfig("UC1", &adOn, &format); err != nil || !ok {
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

// TestChannels_updateConfigPartial_leavesOtherFieldUnchanged asserts a
// partial UpdateConfig call (nil pointer for one field) leaves that column
// exactly as it was, in both directions.
func TestChannels_updateConfigPartial_leavesOtherFieldUnchanged(t *testing.T) {
	st := newTestStore(t)
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	adOn, format := true, "bestvideo+bestaudio"
	if _, _, ok, err := st.UpdateConfig("UC1", &adOn, &format); err != nil || !ok {
		t.Fatalf("seed config: ok=%v err=%v", ok, err)
	}

	// Partial: only autodownload. format_override must survive.
	adOff := false
	gotAD, gotFormat, ok, err := st.UpdateConfig("UC1", &adOff, nil)
	if err != nil || !ok {
		t.Fatalf("update autodownload only: ok=%v err=%v", ok, err)
	}
	if gotAD != false || gotFormat != "bestvideo+bestaudio" {
		t.Fatalf("got autodownload=%v format=%q, want false/bestvideo+bestaudio", gotAD, gotFormat)
	}

	// Partial: only format_override. autodownload must survive.
	newFormat := "worst"
	gotAD, gotFormat, ok, err = st.UpdateConfig("UC1", nil, &newFormat)
	if err != nil || !ok {
		t.Fatalf("update format only: ok=%v err=%v", ok, err)
	}
	if gotAD != false || gotFormat != "worst" {
		t.Fatalf("got autodownload=%v format=%q, want false/worst", gotAD, gotFormat)
	}
}

// TestChannels_updateConfig_notSubscribed reports ok=false, not an error,
// when the channel has no subscription row (tracked-only or unknown).
func TestChannels_updateConfig_notSubscribed(t *testing.T) {
	st := newTestStore(t)
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	adOn := true
	if _, _, ok, err := st.UpdateConfig("UC1", &adOn, nil); err != nil || ok {
		t.Fatalf("tracked-only channel: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

// TestChannels_updateConfig_doesNotResurrectStaleReadValue reproduces the
// original defect's mechanism directly. The old UpdateConfig took plain
// bool/string values (no "leave unchanged" concept), so the only way the
// old handleChannelsPut could implement a partial PUT was to read the
// current row first (List), merge the request over it in Go, and pass the
// merged concrete values through. That read could go stale: this test
// captures exactly such a stale read, then has a concurrent
// unsubscribe/resubscribe (the exact interleaving from the bug report)
// reset the row before the write lands.
//
// With the old signature, completing the write meant calling
// UpdateConfig(id, mergedAutodownload, staleFormatOverride) — there was no
// way to say "don't touch format_override", so the stale value from the
// read would have been written back, clobbering the concurrent reset. That
// call no longer compiles: UpdateConfig now takes *string, and passing the
// captured stale string forces an explicit, visibly-wrong non-nil argument.
// The fix is that a caller who only wants to change autodownload passes nil
// for format_override — there is no stale value to thread through, because
// there is no read step at all. This test proves that path: even though a
// "stale read" is captured first (mirroring what the old handler would have
// cached), passing nil for format_override to the real UpdateConfig
// preserves the concurrently-written value instead of the stale one.
func TestChannels_updateConfig_doesNotResurrectStaleReadValue(t *testing.T) {
	st := newTestStore(t)
	if err := st.Upsert(Channel{ID: "UC1", Name: "One"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	adOff, staleFormat := false, "1080p"
	if _, _, ok, err := st.UpdateConfig("UC1", &adOff, &staleFormat); err != nil || !ok {
		t.Fatalf("seed config: ok=%v err=%v", ok, err)
	}

	// Request A's read: capture the current config, the way the old
	// handler's List("subscribed") call did before merging in a partial PUT.
	items, err := st.List("subscribed")
	if err != nil {
		t.Fatal(err)
	}
	var current *ListItem
	for i := range items {
		if items[i].ID == "UC1" {
			current = &items[i]
		}
	}
	if current == nil {
		t.Fatal("UC1 not found in subscribed list")
	}
	if current.FormatOverride != staleFormat {
		t.Fatalf("captured read = %q, want %q", current.FormatOverride, staleFormat)
	}

	// Request B: unsubscribe then resubscribe, exactly as in the bug
	// report. Subscribe's INSERT ... ON CONFLICT DO NOTHING leaves the
	// fresh row at the schema default, discarding the prior format_override.
	if _, err := st.Unsubscribe("UC1"); err != nil {
		t.Fatal(err)
	}
	if err := st.Subscribe("UC1", "2000-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}

	// Request A resumes: a partial PUT that only sets autodownload. The old
	// handler had nothing but the stale `current.FormatOverride` to pass as
	// its concrete format_override argument here — that call no longer
	// exists. The real fix passes nil: there is nothing captured to
	// resurrect.
	adOn := true
	gotAD, gotFormat, ok, err := st.UpdateConfig("UC1", &adOn, nil)
	if err != nil || !ok {
		t.Fatalf("resume update: ok=%v err=%v", ok, err)
	}
	if gotFormat == staleFormat {
		t.Fatalf("resurrected stale format_override %q from the pre-resubscribe read", staleFormat)
	}
	if gotFormat != "" {
		t.Fatalf("format_override = %q, want %q (the schema default Subscribe reset it to)", gotFormat, "")
	}
	if !gotAD {
		t.Fatal("autodownload was not applied")
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
	}
	for _, id := range []string{"UC2", "UC3"} {
		if err := st.Subscribe(id, "2000-01-01 00:00:00"); err != nil {
			t.Fatalf("subscribe %s: %v", id, err)
		}
	}
	adOn, format := true, ""
	if _, _, _, err := st.UpdateConfig("UC3", &adOn, &format); err != nil {
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
