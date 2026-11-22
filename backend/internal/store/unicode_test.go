package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// Every pooled connection folds case beyond ASCII (casefold) and orders text
// by Unicode rules (COLLATE unicode); Fold is the same folding on the Go side,
// for a pattern compared against casefold().
func TestUnicodeFunctionsOnEveryConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var folded string
	var null *string
	if err := db.QueryRow(`SELECT casefold('Über den GOTTHARD Straße'), casefold(NULL)`).Scan(&folded, &null); err != nil {
		t.Fatal(err)
	}
	if folded != "über den gotthard strasse" || null != nil {
		t.Errorf("casefold = %q, %v", folded, null)
	}
	if got := Fold("ÜBER Straße"); got != "über strasse" {
		t.Errorf("Fold = %q", got)
	}

	var like int
	if err := db.QueryRow(`SELECT casefold('Über den Gotthard') LIKE ? ESCAPE '\'`, "%"+Fold("über DEN")+"%").Scan(&like); err != nil {
		t.Fatal(err)
	}
	if like != 1 {
		t.Error("a folded pattern does not match a folded title")
	}

	rows, err := db.Query(`SELECT x FROM (SELECT 'Zebra' AS x UNION ALL SELECT 'über' UNION ALL SELECT 'Apple' UNION ALL SELECT 'apple' UNION ALL SELECT 'Ärger') ORDER BY x COLLATE unicode`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var order []string
	for rows.Next() {
		var x string
		if err := rows.Scan(&x); err != nil {
			t.Fatal(err)
		}
		order = append(order, x)
	}
	if got := strings.Join(order, ","); got != "apple,Apple,Ärger,über,Zebra" {
		t.Errorf("COLLATE unicode order = %s", got)
	}

	// The built-ins are untouched: LIKE still folds ASCII only.
	if err := db.QueryRow(`SELECT 'Über' LIKE 'über'`).Scan(&like); err != nil {
		t.Fatal(err)
	}
	if like != 0 {
		t.Error("the built-in LIKE was replaced")
	}
}
