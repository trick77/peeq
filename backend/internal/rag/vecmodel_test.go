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
	if err := store.Migrate(db); err != nil {
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

// A database indexed before the record existed: the model is adopted from the
// videos whose vectors are stored, not from config, so the first boot after an
// upgrade cannot wave a model swap through.
func TestCheckEmbedModel_adoptsTheModelOfVectorsStoredBeforeTheRecord(t *testing.T) {
	db, s := vecModelDB(t)
	if err := indexWith(t, s, "v1", "model-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM vec_model`); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckEmbedModel(context.Background(), "model-b"); err == nil {
		t.Fatal("stored model-a vectors, configured model-b: want a refusal")
	}
	if got := recorded(t, s); got != "model-a" {
		t.Fatalf("recorded = %q, want the stored vectors' model-a", got)
	}
}

// Vectors whose model cannot be told from the videos (none recorded, or more
// than one): the configured model is adopted, as the brief for an unknown
// history allows no better.
func TestCheckEmbedModel_adoptsTheConfiguredModelWhenStoredOnesAreAmbiguous(t *testing.T) {
	db, s := vecModelDB(t)
	if err := indexWith(t, s, "v1", "model-a"); err != nil {
		t.Fatal(err)
	}
	if err := indexWith(t, s, "v2", "model-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM vec_model; UPDATE videos SET embed_model = 'model-x' WHERE id = 'v2'`); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckEmbedModel(context.Background(), "model-b"); err != nil {
		t.Fatalf("ambiguous history refused: %v", err)
	}
	if got := recorded(t, s); got != "model-b" {
		t.Fatalf("recorded = %q, want the configured model-b", got)
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
