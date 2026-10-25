package rag

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

func vecModelDB(t *testing.T) (*sql.DB, *Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO videos (id, url) VALUES ('v1','u'),('v2','u')`); err != nil {
		t.Fatal(err)
	}
	return db, NewStore(db)
}

// indexWith writes one chunk for videoID as if model had embedded it.
func indexWith(t *testing.T, s *Store, videoID, model string) error {
	t.Helper()
	vec := make([]float32, 1536) // vec_chunks' width in 0001
	vec[0] = 1
	return s.ReplaceVideoChunks(context.Background(), videoID, IndexMeta{Model: model, Dim: 1536, Rev: ChunkRecipeRev},
		[]ChunkRow{{Ordinal: 0, Text: "t", TokenCount: 1}}, [][]float32{vec})
}

func recorded(t *testing.T, s *Store) string {
	t.Helper()
	m, err := s.RecordedEmbedModel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// An empty database adopts the configured model: nothing is stored yet, so
// every vector written from now on is that model's.
func TestCheckEmbedModel_emptyDBRecordsTheConfiguredModel(t *testing.T) {
	_, s := vecModelDB(t)
	if err := s.CheckEmbedModel(context.Background(), "model-a"); err != nil {
		t.Fatalf("empty DB refused: %v", err)
	}
	if got := recorded(t, s); got != "model-a" {
		t.Fatalf("recorded = %q, want model-a", got)
	}
}

func TestCheckEmbedModel_sameModelIsAccepted(t *testing.T) {
	_, s := vecModelDB(t)
	if err := indexWith(t, s, "v1", "model-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckEmbedModel(context.Background(), "model-a"); err != nil {
		t.Fatalf("same model refused: %v", err)
	}
}

// Two models of the same width embed into different vector spaces: the width
// check passes, and search would silently compare apples to oranges. Refused.
func TestCheckEmbedModel_sameWidthDifferentModelIsRefused(t *testing.T) {
	_, s := vecModelDB(t)
	if err := indexWith(t, s, "v1", "model-a"); err != nil {
		t.Fatal(err)
	}
	err := s.CheckEmbedModel(context.Background(), "model-b")
	if err == nil {
		t.Fatal("a different model over stored vectors must refuse boot")
	}
	for _, w := range []string{"model-a", "model-b", "BACKEND_EMBED_MODEL", "re-index"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("err = %q, want it to mention %q", err, w)
		}
	}
	if got := recorded(t, s); got != "model-a" {
		t.Fatalf("a refused check rewrote the record to %q", got)
	}
}

// The documented re-index: a migration clears vec_chunks and vec_model and
// resets embed_rev, keeping the chunk rows. The next boot must take the new
// model, not refuse it because chunk rows from the old one remain.
func TestCheckEmbedModel_reindexMigrationAcceptsTheNewModel(t *testing.T) {
	db, s := vecModelDB(t)
	if err := indexWith(t, s, "v1", "model-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM vec_chunks; DELETE FROM vec_model; UPDATE videos SET embed_rev = 0`); err != nil {
		t.Fatal(err)
	}
	var chunks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_chunks`).Scan(&chunks); err != nil || chunks == 0 {
		t.Fatalf("transcript_chunks = %d, %v; the re-index keeps them", chunks, err)
	}
	if err := s.CheckEmbedModel(context.Background(), "model-b"); err != nil {
		t.Fatalf("re-indexed DB refused the new model: %v", err)
	}
	if got := recorded(t, s); got != "model-b" {
		t.Fatalf("recorded = %q, want model-b", got)
	}
}

// Vectors with no recorded model can only come from a migration that cleared
// vec_model alone. Boot refuses rather than guess which space they are in.
func TestCheckEmbedModel_vectorsWithoutARecordAreRefused(t *testing.T) {
	db, s := vecModelDB(t)
	if err := indexWith(t, s, "v1", "model-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM vec_model`); err != nil {
		t.Fatal(err)
	}
	err := s.CheckEmbedModel(context.Background(), "model-a")
	if err == nil || !strings.Contains(err.Error(), "re-index") {
		t.Fatalf("err = %v, want a refusal naming the re-index", err)
	}
	if got := recorded(t, s); got != "" {
		t.Fatalf("a refused check recorded %q", got)
	}
}

// Writing vectors records their model, and a write from a different model is
// refused rather than mixed into the table.
func TestReplaceVideoChunks_recordsAndGuardsTheModel(t *testing.T) {
	_, s := vecModelDB(t)
	if err := indexWith(t, s, "v1", "model-a"); err != nil {
		t.Fatal(err)
	}
	if got := recorded(t, s); got != "model-a" {
		t.Fatalf("recorded = %q after a write, want model-a", got)
	}
	if err := indexWith(t, s, "v2", "model-b"); err == nil {
		t.Fatal("a write from a different model must be refused")
	}
}

// With no vectors stored there is nothing a model could mismatch: an empty
// library may switch models freely, and only a write that stores vectors pins
// one.
func TestCheckEmbedModel_emptyLibraryMaySwitchModels(t *testing.T) {
	_, s := vecModelDB(t)
	ctx := context.Background()
	if err := s.CheckEmbedModel(ctx, "model-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckEmbedModel(ctx, "model-b"); err != nil {
		t.Fatalf("empty library refused a switch: %v", err)
	}
	if got := recorded(t, s); got != "model-b" {
		t.Fatalf("recorded = %q, want model-b", got)
	}
}

// Deleting every video's chunks empties the table; the old record must not
// hold a new model hostage.
func TestCheckEmbedModel_libraryEmptiedByDeletesMaySwitchModels(t *testing.T) {
	_, s := vecModelDB(t)
	ctx := context.Background()
	if err := indexWith(t, s, "v1", "model-a"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteVideoChunks(ctx, "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckEmbedModel(ctx, "model-b"); err != nil {
		t.Fatalf("emptied library refused a switch: %v", err)
	}
	if got := recorded(t, s); got != "model-b" {
		t.Fatalf("recorded = %q, want model-b", got)
	}
	if err := indexWith(t, s, "v2", "model-b"); err != nil {
		t.Fatalf("write under the new model refused: %v", err)
	}
}

// The remedy must say to reset embed_rev: rebuilding the table alone leaves
// every video marked indexed, so nothing would re-embed.
func TestCheckEmbedModel_remedyResetsEmbedRev(t *testing.T) {
	_, s := vecModelDB(t)
	if err := indexWith(t, s, "v1", "model-a"); err != nil {
		t.Fatal(err)
	}
	err := s.CheckEmbedModel(context.Background(), "model-b")
	if err == nil || !strings.Contains(err.Error(), "embed_rev") {
		t.Fatalf("err = %v, want the remedy to reset embed_rev", err)
	}
	if err := CheckVecWidth(1536, "m", 8); err == nil || !strings.Contains(err.Error(), "embed_rev") {
		t.Fatalf("width err = %v, want the remedy to reset embed_rev", err)
	}
}
