package auth

import (
	"path/filepath"
	"testing"

	"github.com/trick77/vark/internal/store"
)

func openTestDB(t *testing.T) DBTX {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
