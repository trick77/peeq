package store

import "strings"

// Placeholders renders n comma-separated "?" for an IN (...) list, so a
// query can bind a variable number of ids without building SQL from them.
// Zero renders as "", which makes IN () a syntax error — callers guard the
// empty case before reaching the query.
func Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
