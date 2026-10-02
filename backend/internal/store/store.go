// Package store opens the SQLite database (pure-Go ncruces driver with
// sqlite-vec linked in) and applies embedded migrations.
package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	// sqlite-vec WASM build for ncruces; provides the SQLite WASM binary AND
	// the vec0 virtual table + vec_* functions. Replaces ncruces/go-sqlite3/embed.
	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	// registers the "sqlite3" database/sql driver.
	_ "github.com/ncruces/go-sqlite3/driver"
)

// Pool bounds. maxConns is above anything one page load plus the background
// workers ask for at once; a connection idle past connMaxIdle gives its
// memory back.
const (
	maxConns    = 16
	connMaxIdle = 10 * time.Minute
)

// Open opens (creating if needed) the SQLite database at path and applies
// PRAGMAs for safe concurrent use. Callers must run Migrate separately.
func Open(path string) (*sql.DB, error) {
	return open(path, 10*time.Second)
}

// open is Open with the busy timeout the tests shorten.
func open(path string, busyTimeout time.Duration) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dsn(path, busyTimeout))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// database/sql keeps two idle connections by default. A page load fires a
	// dozen requests at once, so most of them opened a connection of their own
	// and closed it again: with this driver that is a fresh SQLite instance
	// and an empty page cache every time. Keeping the pool warm is the fix; the
	// ceiling bounds memory, since each connection carries its own instance.
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxIdleTime(connMaxIdle)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
}

// dsn builds the connection string. Every transaction is BEGIN IMMEDIATE
// (_txlock=immediate): every BeginTx in this tree is a write transaction,
// and several read before they write (SetResume, the share-link store).
// Under WAL a deferred BEGIN takes a read snapshot on the first SELECT and
// has to upgrade it on the first write; when another pooled connection
// committed in between, SQLite refuses the upgrade at once with
// BUSY_SNAPSHOT, and busy_timeout is never consulted for that. Taking the
// write lock at BEGIN turns it into the other connection waiting on
// busy_timeout instead, which is what the timeout is for. Plain queries
// outside a transaction are untouched, and a transaction that only reads
// must say so — sql.TxOptions{ReadOnly: true} is the one case the driver
// still opens deferred — or it will queue behind every writer for nothing.
func dsn(path string, busyTimeout time.Duration) string {
	return fmt.Sprintf(
		"file:%s?_pragma=journal_mode(wal)&_pragma=busy_timeout(%d)&_pragma=foreign_keys(on)&_txlock=immediate",
		url.PathEscape(path), busyTimeout.Milliseconds(),
	)
}

// VecLiteral encodes a float32 vector as the JSON-array text sqlite-vec
// accepts. Unused until embeddings land in a later phase; kept exported for
// callers in future store code.
func VecLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}
