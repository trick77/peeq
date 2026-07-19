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
