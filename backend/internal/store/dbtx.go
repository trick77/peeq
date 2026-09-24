package store

import (
	"context"
	"database/sql"
)

// DBTX is the two-method subset of *sql.DB and *sql.Tx that a statement
// needs to run: what a store helper takes when it must work both on its own
// and inside another store's transaction.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
