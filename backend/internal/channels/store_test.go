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

// TestDeleteCascade_purgesVecChunks asserts DeleteCascade also removes the
// vec_chunks (vec0 virtual table) rows belonging to the channel's videos.
// vec0 cannot ride an FK cascade or trigger, so without an explicit purge
// these rows are orphaned forever once their transcript_chunks parent is
// gone — this test fails (vec count stays 1) before the fix.
func TestDeleteCascade_purgesVecChunks(t *testing.T) {
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

	var before int
	if err := db.QueryRow(`SELECT count(*) FROM vec_chunks`).Scan(&before); err != nil || before != 1 {
		t.Fatalf("vec_chunks before delete = %d err=%v, want 1", before, err)
	}

	if err := st.DeleteCascade("UC1"); err != nil {
		t.Fatal(err)
	}

	var afterVec, afterChunks, afterVideos int
	if err := db.QueryRow(`SELECT count(*) FROM vec_chunks`).Scan(&afterVec); err != nil || afterVec != 0 {
		t.Fatalf("vec_chunks after delete = %d err=%v, want 0 (orphaned rows leaked)", afterVec, err)
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
