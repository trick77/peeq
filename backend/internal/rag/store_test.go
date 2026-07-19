package rag

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

func TestReplaceRetrieveAndDelete(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	// vec_chunks in 0001 is float[1536]; use matching-length vectors here.
	dim := 1536
	mk := func(v float32) []float32 {
		out := make([]float32, dim)
		out[0] = v
		return out
	}
	if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u'),('v2','u')`); err != nil {
		t.Fatal(err)
	}
	s := NewStore(db)
	ctx := context.Background()
	rows := []ChunkRow{{Ordinal: 0, Text: "titanium frame", StartSeconds: 108, TokenCount: 2}}
	if err := s.ReplaceVideoChunks(ctx, "v1", "e5", dim, rows, [][]float32{mk(0.9)}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceVideoChunks(ctx, "v2", "e5", dim, []ChunkRow{{Ordinal: 0, Text: "battery life", StartSeconds: 303, TokenCount: 2}}, [][]float32{mk(0.1)}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Retrieve(ctx, mk(0.9), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].VideoID != "v1" || hits[0].StartSeconds != 108 {
		t.Fatalf("nearest neighbor wrong: %+v", hits)
	}
	// embed_model/dim recorded
	var m string
	var d int
	db.QueryRow(`SELECT embed_model, embed_dim FROM videos WHERE id='v1'`).Scan(&m, &d)
	if m != "e5" || d != dim {
		t.Fatalf("embed meta not recorded: %s %d", m, d)
	}
	// delete drops both tables
	if err := s.DeleteVideoChunks(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	var cnt int
	db.QueryRow(`SELECT COUNT(*) FROM transcript_chunks WHERE video_id='v1'`).Scan(&cnt)
	if cnt != 0 {
		t.Fatalf("chunks not deleted: %d", cnt)
	}

	// BuiltDim reports the vec_chunks embedding dimension baked into the schema.
	got, err := s.BuiltDim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != dim {
		t.Fatalf("BuiltDim = %d, want %d", got, dim)
	}
}

// TestReplaceVideoChunksWritesFTSAndKind asserts ReplaceVideoChunks mirrors
// each row's text into fts_chunks (keyed by the same rowid as vec_chunks)
// and persists the row's kind, and that re-indexing a video leaves no fts
// orphan (mirroring the vec_chunks replace-cleanly behavior).
func TestReplaceVideoChunksWritesFTSAndKind(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	dim := 1536
	seedVec := func() []float32 {
		out := make([]float32, dim)
		out[0] = 0.5
		return out
	}
	if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u')`); err != nil {
		t.Fatal(err)
	}
	st := NewStore(db)
	ctx := context.Background()
	rows := []ChunkRow{
		{Ordinal: 0, Text: "intro about sourdough", Kind: "transcript", StartSeconds: 0},
		{Ordinal: 1, Text: "whole video summary", Kind: "summary", StartSeconds: 0},
	}
	vecs := [][]float32{seedVec(), seedVec()}
	if err := st.ReplaceVideoChunks(ctx, "v1", "m", dim, rows, vecs); err != nil {
		t.Fatal(err)
	}
	// fts_chunks has both rows and MATCH works.
	var ftsN int
	if err := st.db.QueryRow(`SELECT count(*) FROM fts_chunks WHERE fts_chunks MATCH 'sourdough'`).Scan(&ftsN); err != nil {
		t.Fatal(err)
	}
	if ftsN != 1 {
		t.Fatalf("fts MATCH sourdough = %d, want 1", ftsN)
	}
	// kind persisted.
	var kind string
	if err := st.db.QueryRow(`SELECT kind FROM transcript_chunks WHERE video_id='v1' AND ordinal=1`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "summary" {
		t.Fatalf("kind = %q, want summary", kind)
	}
	// Re-index replaces cleanly: no fts orphan.
	if err := st.ReplaceVideoChunks(ctx, "v1", "m", dim, rows[:1], vecs[:1]); err != nil {
		t.Fatal(err)
	}
	var total int
	if err := st.db.QueryRow(`SELECT count(*) FROM fts_chunks`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("fts rows after re-index = %d, want 1 (no orphan)", total)
	}
}

// TestSearchFTSMatchesKeyword asserts SearchFTS finds a chunk by an exact
// keyword (e.g. "Fibonacci") that a purely semantic/vector search could
// plausibly miss, and that it round-trips kind/start_seconds.
func TestSearchFTSMatchesKeyword(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	dim := 1536
	seedVec := func() []float32 {
		out := make([]float32, dim)
		out[0] = 0.5
		return out
	}
	if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u')`); err != nil {
		t.Fatal(err)
	}
	st := NewStore(db)
	ctx := context.Background()
	rows := []ChunkRow{{Ordinal: 0, Text: "the Fibonacci sequence explained", Kind: "transcript", StartSeconds: 42}}
	if err := st.ReplaceVideoChunks(ctx, "v1", "m", dim, rows, [][]float32{seedVec()}); err != nil {
		t.Fatal(err)
	}
	hits, err := st.SearchFTS(ctx, BuildFTSMatch("fibonacci"), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].StartSeconds != 42 || hits[0].Kind != "transcript" {
		t.Fatalf("hits = %+v, want one transcript hit at 42s", hits)
	}
}
