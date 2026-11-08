package store

import (
	"path/filepath"
	"testing"

	"github.com/trick77/ship/kit/schemadoc"
)

// TestSchemaDoc keeps docs/schema.md in step with the migrations and the
// model.go row types. A file database, not :memory:: the pool would hand each
// connection its own empty database.
func TestSchemaDoc(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	s, err := schemadoc.Describe(t.Context(), db, schemadoc.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	es, err := schemadoc.Entities(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := schemadoc.Render(s, es)
	if err != nil {
		t.Fatal(err)
	}
	schemadoc.Golden(t, doc, filepath.Join("..", "..", "..", "docs", "schema.md"))
}
