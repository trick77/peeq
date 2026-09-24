package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
)

// seedCounter creates a one-row table both connections will fight over.
func seedCounter(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE counter (n INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO counter (n) VALUES (0)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

// readThenWrite is the shape of SetResume and the share-link store: a
// transaction that reads first and writes second. Under WAL, with a deferred
// BEGIN, the read takes a snapshot and the later write must upgrade it; if
// another connection committed in between, SQLite refuses the upgrade at
// once with BUSY_SNAPSHOT — the busy handler is never consulted for that.
// With BEGIN IMMEDIATE the write lock is taken up front, so the OTHER
// connection is the one that waits (on busy_timeout) and this transaction
// commits.
//
// Both outcomes are deterministic, so the test drives each variant through
// the same script with a short busy_timeout and asserts which side fails.
func readThenWrite(t *testing.T, db *sql.DB) (txErr, otherErr error) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT n FROM counter`).Scan(&n); err != nil {
		t.Fatalf("read inside tx: %v", err)
	}
	// Another pooled connection writes while the transaction is open.
	_, otherErr = db.ExecContext(ctx, `UPDATE counter SET n = n + 10`)
	if _, err := tx.ExecContext(ctx, `UPDATE counter SET n = ?`, n+1); err != nil {
		return err, otherErr
	}
	return tx.Commit(), otherErr
}

// The deferred half characterises SQLite itself, with a DSN built here: it is
// the failure the production DSN exists to rule out, and the pair is what
// shows the immediate variant is doing the work.
func TestOpen_deferredTxlockDiesOnSnapshotConflict(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "d.db")+
		"?_pragma=journal_mode(wal)&_pragma=busy_timeout(200)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedCounter(t, db)
	txErr, otherErr := readThenWrite(t, db)
	if otherErr != nil {
		t.Fatalf("the autocommit write should have gone through, got %v", otherErr)
	}
	if !errors.Is(txErr, sqlite3.BUSY) {
		t.Fatalf("deferred read-then-write should die with BUSY(_SNAPSHOT), got %v", txErr)
	}
}

func TestOpen_immediateTxlockPreventsSnapshotConflict(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "i.db"), 200*time.Millisecond)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedCounter(t, db)
	txErr, otherErr := readThenWrite(t, db)
	if txErr != nil {
		t.Fatalf("immediate read-then-write should commit, got %v", txErr)
	}
	if !errors.Is(otherErr, sqlite3.BUSY) {
		t.Fatalf("the concurrent write should have waited out busy_timeout and failed with BUSY, got %v", otherErr)
	}
	var n int
	if err := db.QueryRow(`SELECT n FROM counter`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("counter = %d err=%v, want 1 (only the transaction's write landed)", n, err)
	}
}

// TestOpen_usesImmediateTxlock pins the production DSN to the immediate
// variant, so the property above holds for every BeginTx in the tree.
func TestOpen_usesImmediateTxlock(t *testing.T) {
	got := dsn("/tmp/x.db", 10*time.Second)
	for _, want := range []string{"_txlock=immediate", "_pragma=busy_timeout(10000)", "_pragma=journal_mode(wal)", "_pragma=foreign_keys(on)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dsn %q lacks %q", got, want)
		}
	}
}
