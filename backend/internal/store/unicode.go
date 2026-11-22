package store

import (
	"github.com/ncruces/go-sqlite3"
	"golang.org/x/text/cases"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// SQLite's LIKE, lower() and NOCASE fold ASCII only: "über" never matches
// "Über", and NOCASE sorts "Über" after "Zebra". registerUnicode adds, on
// every pooled connection, what a query opts into by name:
//   - casefold(x): x with Unicode case folding (Fold on the Go side);
//   - COLLATE unicode: Unicode root collation order.
//
// The built-ins are left alone on purpose (the ncruces unicode extension
// would replace LIKE, lower() and upper() for every query), so no existing
// statement changes meaning.
func registerUnicode(c *sqlite3.Conn) error {
	// One folder and one collator per connection: neither is safe for
	// concurrent use, and a connection runs one statement at a time.
	fold := cases.Fold()
	err := c.CreateFunction("casefold", 1, sqlite3.DETERMINISTIC|sqlite3.INNOCUOUS,
		func(ctx sqlite3.Context, arg ...sqlite3.Value) {
			if arg[0].Type() == sqlite3.NULL {
				ctx.ResultNull()
				return
			}
			ctx.ResultRawText(fold.Bytes(arg[0].RawText()))
		})
	if err != nil {
		return err
	}
	return c.CreateCollation("unicode", collate.New(language.Und).Compare)
}

// Fold is casefold() in Go: fold a value before comparing it against
// casefold(column).
func Fold(s string) string { return cases.Fold().String(s) }
