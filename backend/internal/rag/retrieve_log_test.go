package rag

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/store"
)

func retrieveStore(t *testing.T) *Store {
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
	s := NewStore(db)
	if err := s.ReplaceVideoChunks(context.Background(), "v1", IndexMeta{Model: "e5", Dim: 1536, Rev: ChunkRecipeRev},
		[]ChunkRow{{Text: "titanium frame"}}, [][]float32{unitOf(1536, 1)}); err != nil {
		t.Fatal(err)
	}
	return s
}

func captureDefault(t *testing.T) *strings.Builder {
	t.Helper()
	var b strings.Builder
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&b, nil)))
	t.Cleanup(func() { slog.SetDefault(restore) })
	return &b
}

func TestRetrieve_logsASlowSearch(t *testing.T) {
	// A search reading a bloated vec_chunks takes minutes and showed nothing
	// until whatever waited on it gave up. A slow one says so.
	s := retrieveStore(t)
	out := captureDefault(t)
	threshold := slowVectorSearch
	slowVectorSearch = 0
	t.Cleanup(func() { slowVectorSearch = threshold })

	if _, err := s.RetrieveWithinFiltered(context.Background(), unitOf(1536, 1), 10, 0, Filter{}); err != nil {
		t.Fatal(err)
	}

	wantAll(t, out.String(), "level=WARN", `msg="slow vector search"`, "k=10", "hits=1", "filtered=false", "took=")
}

func TestRetrieve_quietWhenFast(t *testing.T) {
	s := retrieveStore(t)
	out := captureDefault(t)

	if _, err := s.RetrieveWithinFiltered(context.Background(), unitOf(1536, 1), 10, 0, Filter{}); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("log = %q, want nothing", out.String())
	}
}

func TestRetrieve_anInterruptSaysSo(t *testing.T) {
	// sqlite-vec reports an interrupt as "SQL logic error: chunks iter
	// error", which reads as corruption. The error names the cancel.
	s := retrieveStore(t)
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errors.New("reader went away"))

	_, err := s.RetrieveWithinFiltered(ctx, unitOf(1536, 1), 10, 0, Filter{})

	if err == nil || !strings.Contains(err.Error(), "vector search interrupted after") ||
		!strings.Contains(err.Error(), "reader went away") {
		t.Errorf("err = %v, want the interrupt, its duration and its cause", err)
	}
}
