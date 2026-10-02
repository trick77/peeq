package rag

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

const reuseDim = 1536

func reuseVec(v float32) []float32 {
	out := make([]float32, reuseDim)
	out[0] = v
	return out
}

func reuseStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u')`); err != nil {
		t.Fatal(err)
	}
	return NewStore(db), db
}

// chunkIDs maps each stored chunk's text to its rowid.
func chunkIDs(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT id, text FROM transcript_chunks WHERE video_id = 'v1'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			t.Fatal(err)
		}
		out[text] = id
	}
	return out
}

// Re-indexing a video whose transcript did not change must not throw its
// vectors away: vec0 never frees a deleted vector, so every wholesale replace
// leaves dead storage behind, and re-embedding identical text pays for the
// same answer twice. A row passed with a nil vector keeps the stored one.
func TestReplaceVideoChunks_keepsTheVectorOfARowPassedWithoutOne(t *testing.T) {
	s, db := reuseStore(t)
	ctx := context.Background()
	meta := IndexMeta{Model: "m", Dim: reuseDim, Rev: ChunkRecipeRev}

	if err := s.ReplaceVideoChunks(ctx, "v1", meta, []ChunkRow{
		{Ordinal: 0, Text: "window one", StartSeconds: 0},
		{Ordinal: 1, Text: "window two", StartSeconds: 60},
		{Ordinal: 2, Text: "old chapter", Kind: "chapter", StartSeconds: 0},
	}, [][]float32{reuseVec(0.1), reuseVec(0.2), reuseVec(0.3)}); err != nil {
		t.Fatal(err)
	}
	before := chunkIDs(t, db)

	reuse, err := s.ReusableTexts(ctx, "v1", "m")
	if err != nil {
		t.Fatal(err)
	}
	if reuse["window one"] != 1 || reuse["window two"] != 1 || reuse["old chapter"] != 1 || len(reuse) != 3 {
		t.Fatalf("reusable texts = %v", reuse)
	}

	// The windows are unchanged; the chapter was rewritten and moved first.
	if err := s.ReplaceVideoChunks(ctx, "v1", meta, []ChunkRow{
		{Ordinal: 0, Text: "new chapter", Kind: "chapter", StartSeconds: 5},
		{Ordinal: 1, Text: "window one", StartSeconds: 0},
		{Ordinal: 2, Text: "window two", StartSeconds: 61},
	}, [][]float32{reuseVec(0.9), nil, nil}); err != nil {
		t.Fatal(err)
	}
	after := chunkIDs(t, db)

	if after["window one"] != before["window one"] || after["window two"] != before["window two"] {
		t.Fatalf("kept rows changed rowid: before %v, after %v", before, after)
	}
	if _, ok := after["old chapter"]; ok || len(after) != 3 {
		t.Fatalf("chunks after = %v, want the old chapter gone and three rows", after)
	}
	var ordinal, start int
	if err := db.QueryRow(`SELECT ordinal, start_seconds FROM transcript_chunks WHERE text = 'window two'`).Scan(&ordinal, &start); err != nil {
		t.Fatal(err)
	}
	if ordinal != 2 || start != 61 {
		t.Fatalf("kept row not updated in place: ordinal %d start %d, want 2 and 61", ordinal, start)
	}
	// vec and fts agree with the chunk table: exactly the three chunks, so the
	// dropped one's vector and keyword rows went with it. (Its rowid is not
	// checked for absence: SQLite hands a freed highest rowid to the next
	// insert.)
	for _, table := range []string{"vec_chunks", "fts_chunks"} {
		var total, matching int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&total); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE rowid IN (SELECT id FROM transcript_chunks)`).Scan(&matching); err != nil {
			t.Fatal(err)
		}
		if total != 3 || matching != 3 {
			t.Fatalf("%s: %d rows, %d of them matching a chunk; want 3 and 3", table, total, matching)
		}
	}
}

// Vectors from another model are not the same vectors: nothing is reusable
// then, however identical the text.
func TestReusableTexts_onlyForTheModelThatWroteThem(t *testing.T) {
	s, _ := reuseStore(t)
	ctx := context.Background()
	if err := s.ReplaceVideoChunks(ctx, "v1", IndexMeta{Model: "m", Dim: reuseDim, Rev: ChunkRecipeRev},
		[]ChunkRow{{Ordinal: 0, Text: "window one"}}, [][]float32{reuseVec(0.1)}); err != nil {
		t.Fatal(err)
	}
	reuse, err := s.ReusableTexts(ctx, "v1", "another-model")
	if err != nil || len(reuse) != 0 {
		t.Fatalf("reusable under another model = %v, %v; want none", reuse, err)
	}
}

// A nil vector for text that is not stored cannot be honoured, and must not
// be written as a chunk with no vector.
func TestReplaceVideoChunks_refusesANilVectorWithNothingToReuse(t *testing.T) {
	s, db := reuseStore(t)
	err := s.ReplaceVideoChunks(context.Background(), "v1", IndexMeta{Model: "m", Dim: reuseDim, Rev: ChunkRecipeRev},
		[]ChunkRow{{Ordinal: 0, Text: "never stored"}}, [][]float32{nil})
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := chunkIDs(t, db); len(got) != 0 {
		t.Fatalf("a refused write left chunks behind: %v", got)
	}
}
