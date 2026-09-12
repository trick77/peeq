package store_test

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/trick77/peeq/internal/rag"
)

// The vector width lives in exactly two places: the embedding model's llmwire
// profile, which is where the width comes FROM, and the migration that created
// vec_chunks, which is where the width is STORED. The second cannot be derived
// from the first — a migration that has run anywhere real is never edited, so
// the DDL is a literal — and this test is what keeps them equal.
//
// If it fails, the fix is not to touch 0001_init.sql. It is a new migration that
// rebuilds vec_chunks at the new width, because every row already in it was
// written by the old model and is wrong for the new one.
//
// An external test package on purpose: rag imports store, so store's own tests
// cannot import rag, but store_test can.
func TestVecChunksWidthMatchesTheEmbeddingModel(t *testing.T) {
	ddl, err := os.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	m := regexp.MustCompile(`embedding float\[(\d+)\]`).FindSubmatch(ddl)
	if m == nil {
		t.Fatal("0001_init.sql no longer declares vec_chunks.embedding as float[N]")
	}
	built, _ := strconv.Atoi(string(m[1]))
	if built != rag.EmbedDim() {
		t.Fatalf("vec_chunks is built at %d but %s produces %d-wide vectors; "+
			"a model change needs a NEW migration that rebuilds the table",
			built, rag.EmbedModel, rag.EmbedDim())
	}
}
