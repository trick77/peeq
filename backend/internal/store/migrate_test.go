package store

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/trick77/ship/kit/migrate"
)

// legacyBodies is the migration set under the names the old runner recorded:
// the same files, renamed back through legacyNames.
func legacyBodies(t *testing.T) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for old, name := range legacyNames {
		body, err := fs.ReadFile(migrations(), name)
		if err != nil {
			t.Fatalf("%s maps to %s: %v", old, name, err)
		}
		out[old] = body
	}
	return out
}

// applyLegacy is the old runner (origin/master's Migrate before the switch),
// kept here as the reference the switch is measured against and as the
// previous release a rollback boots: schema_migrations(version), every file
// not recorded under version applied in lexicographic name order, each in its
// own transaction. It stops after stopAt ("" runs everything).
func applyLegacy(t *testing.T, db *sql.DB, stopAt string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		t.Fatal(err)
	}
	bodies := legacyBodies(t)
	names := make([]string, 0, len(bodies))
	for n := range bodies {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		var dummy int
		err := db.QueryRow(`SELECT 1 FROM schema_migrations WHERE version = ?`, name).Scan(&dummy)
		switch {
		case err == nil:
			// already applied
		case !errors.Is(err, sql.ErrNoRows):
			t.Fatal(err)
		default:
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(string(bodies[name])); err != nil {
				t.Fatalf("apply %s: %v", name, err)
			}
			if _, err := tx.Exec(`INSERT INTO schema_migrations (version) VALUES (?)`, name); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		if name == stopAt {
			return
		}
	}
	if stopAt != "" {
		t.Fatalf("no legacy migration named %q", stopAt)
	}
}

// schemaOf is every object's DDL except the bookkeeping table, whose shape is
// the one thing the switch changes on purpose.
func schemaOf(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT type, name, coalesce(sql, '') FROM sqlite_master
		WHERE name NOT LIKE 'schema_migrations%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, ddl string
		if err := rows.Scan(&typ, &name, &ddl); err != nil {
			t.Fatal(err)
		}
		b.WriteString(typ + " " + name + "\n" + ddl + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func appliedNames(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name, applied_at FROM schema_migrations`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, at string
		if err := rows.Scan(&n, &at); err != nil {
			t.Fatal(err)
		}
		out[n] = at
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertNothingPending(t *testing.T, db *sql.DB) {
	t.Helper()
	pending, err := migrate.Pending(t.Context(), db, migrations(), migrateOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending after migrate: %v", pending)
	}
}

func columnsOf(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return cols
}

// bookkeeping is every schema_migrations row as version|name|applied_at, with
// NULL spelled out, sorted.
func bookkeeping(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT coalesce(version, 'NULL') || '|' || coalesce(name, 'NULL') || '|' || applied_at
		FROM schema_migrations ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The renumbering keeps the run order: the old names in the old runner's
// lexicographic order, mapped through legacyNames, are kit's first files in
// kit's version order, one for one. Files added since come after them.
func TestLegacyNames_keepTheOldRunOrder(t *testing.T) {
	files, err := migrate.Files(migrations())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < len(legacyNames) {
		t.Fatalf("legacyNames has %d entries, only %d files embedded", len(legacyNames), len(files))
	}
	old := make([]string, 0, len(legacyNames))
	for n := range legacyNames {
		old = append(old, n)
	}
	sort.Strings(old)
	for i, n := range old {
		if got, want := legacyNames[n], files[i].Name; got != want {
			t.Fatalf("position %d: %s maps to %s, kit runs %s there", i, n, got, want)
		}
		// The renumbering changed only the number.
		if suffix := func(s string) string { return s[strings.IndexByte(s, '_'):] }; suffix(n) != suffix(legacyNames[n]) {
			t.Fatalf("%s maps to %s: the name after the number changed", n, legacyNames[n])
		}
	}
}

func TestMigrate_freshSchemaMatchesTheOldRunner(t *testing.T) {
	// Given: one database built by the old runner, one by kit's.
	legacy := openTemp(t)
	applyLegacy(t, legacy, "")
	fresh := openTemp(t)

	// When: the new build migrates the fresh one.
	if err := Migrate(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}

	// Then: every table, index and trigger is the same DDL.
	if got, want := schemaOf(t, fresh), schemaOf(t, legacy); got != want {
		t.Fatalf("schema differs from the old runner's\n--- kit\n%s\n--- old\n%s", got, want)
	}
	// And: the bookkeeping table is kit's, with every current file recorded.
	files, err := migrate.Files(migrations())
	if err != nil {
		t.Fatal(err)
	}
	applied := appliedNames(t, fresh)
	for _, m := range files {
		if _, ok := applied[m.Name]; !ok {
			t.Fatalf("%s not recorded", m.Name)
		}
	}
	assertNothingPending(t, fresh)
}

func TestMigrate_adoptsAFullyMigratedOldDatabase(t *testing.T) {
	// Given: a database the old runner took all the way.
	db := openTemp(t)
	applyLegacy(t, db, "")
	want := schemaOf(t, db)

	// When: the new build boots twice.
	for i := range 2 {
		if err := Migrate(t.Context(), db); err != nil {
			t.Fatalf("Migrate #%d: %v", i+1, err)
		}
	}

	// Then: nothing ran, nothing is pending, and the rows carry today's names.
	if got := schemaOf(t, db); got != want {
		t.Fatalf("adopting changed the schema\n--- after\n%s\n--- before\n%s", got, want)
	}
	assertNothingPending(t, db)
	applied := appliedNames(t, db)
	if len(applied) != len(legacyNames) {
		t.Fatalf("%d rows recorded, want %d", len(applied), len(legacyNames))
	}
	// And: the old runner's version column stays, holding the old names.
	if cols := columnsOf(t, db, "schema_migrations"); !slices.Equal(cols, []string{"version", "applied_at", "name"}) {
		t.Fatalf("schema_migrations columns = %v, want [version applied_at name]", cols)
	}
	for old, name := range legacyNames {
		var version string
		if err := db.QueryRow(`SELECT version FROM schema_migrations WHERE name = ?`, name).Scan(&version); err != nil {
			t.Fatal(err)
		}
		if version != old {
			t.Errorf("%s: version = %s, want %s", name, version, old)
		}
	}
	// And: a later run of kit's runner has nothing to apply.
	ran, err := migrate.Up(t.Context(), db, migrations(), migrateOptions)
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Fatalf("second boot applied %v", ran)
	}
}

func TestMigrate_adoptsAPartlyMigratedOldDatabase(t *testing.T) {
	// Given: an old database stopped after the first of the two 0010 files,
	// with a marker on every recorded row.
	db := openTemp(t)
	applyLegacy(t, db, "0010_media_probe.sql")
	if _, err := db.Exec(`UPDATE schema_migrations SET applied_at = 'old'`); err != nil {
		t.Fatal(err)
	}

	// When: the new build boots.
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	// Then: the adopted files kept their rows (not run again) and only the
	// rest ran.
	applied := appliedNames(t, db)
	for old, name := range legacyNames {
		at, ok := applied[name]
		if !ok {
			t.Fatalf("%s not recorded", name)
		}
		if adopted := old <= "0010_media_probe.sql"; adopted != (at == "old") {
			t.Fatalf("%s (was %s) applied_at = %q, adopted = %v", name, old, at, adopted)
		}
	}
	assertNothingPending(t, db)
	// And: the result is the schema a fresh database gets.
	fresh := openTemp(t)
	if err := Migrate(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	if got, want := schemaOf(t, db), schemaOf(t, fresh); got != want {
		t.Fatalf("schema differs from a fresh one\n--- adopted\n%s\n--- fresh\n%s", got, want)
	}
}

func TestMigrate_refusesAnUnknownOldName(t *testing.T) {
	// Given: an old database that recorded a file this build does not have.
	db := openTemp(t)
	applyLegacy(t, db, "0004_category_manual.sql")
	if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES ('0004_from_a_branch.sql')`); err != nil {
		t.Fatal(err)
	}

	// When: the new build boots.
	err := Migrate(t.Context(), db)

	// Then: it stops with the name, and the table is left as the old runner
	// wrote it.
	if err == nil || !strings.Contains(err.Error(), "0004_from_a_branch.sql") {
		t.Fatalf("Migrate = %v, want an error naming the unknown file", err)
	}
	if cols := columnsOf(t, db, "schema_migrations"); !slices.Equal(cols, []string{"version", "applied_at"}) {
		t.Fatalf("a failed adopt left schema_migrations as %v", cols)
	}
}

func TestMigrate_stopsOnACanceledContext(t *testing.T) {
	// Given: a boot that is already shutting down.
	db := openTemp(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	// When / Then: Migrate returns the cancellation instead of touching the
	// database.
	if err := Migrate(ctx, db); !errors.Is(err, context.Canceled) {
		t.Fatalf("Migrate = %v, want context.Canceled", err)
	}
}

// A rollback to the previous release runs the old runner, with the old file
// set, against a database this build adopted. It must find every file it
// knows recorded under version and boot as a no-op, both when adoption found
// the old database complete and when kit applied the rest (its rows were
// inserted with no version, which the old TEXT PRIMARY KEY accepts, and
// mirrored afterwards).
func TestMigrate_adoptedDatabaseBootsThePreviousRelease(t *testing.T) {
	for _, stopAt := range []string{"", "0010_media_probe.sql"} {
		t.Run("legacy stopped at "+stopAt, func(t *testing.T) {
			// Given: an old database adopted and brought up to date by this build.
			db := openTemp(t)
			applyLegacy(t, db, stopAt)
			if err := Migrate(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE schema_migrations SET applied_at = 'adopted'`); err != nil {
				t.Fatal(err)
			}
			wantSchema, wantRows := schemaOf(t, db), bookkeeping(t, db)
			for _, r := range wantRows {
				if strings.HasPrefix(r, "NULL|") || strings.Contains(r, "|NULL|") {
					t.Fatalf("row left half recorded: %s", r)
				}
			}

			// When: the previous release boots on it.
			applyLegacy(t, db, "")

			// Then: it applied nothing and recorded nothing.
			if got := schemaOf(t, db); got != wantSchema {
				t.Fatalf("old runner changed the schema\n--- after\n%s\n--- before\n%s", got, wantSchema)
			}
			if got := bookkeeping(t, db); !slices.Equal(got, wantRows) {
				t.Fatalf("old runner changed schema_migrations\n--- after\n%v\n--- before\n%v", got, wantRows)
			}

			// And: rolling forward again boots with nothing pending.
			if err := Migrate(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			assertNothingPending(t, db)
		})
	}
}

func TestMigrate_namesARowTheOldRunnerAddedAfterARollback(t *testing.T) {
	// Given: an adopted database on which the previous release recorded a file
	// it applied, under version only.
	db := openTemp(t)
	applyLegacy(t, db, "")
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version = '0038_drop_sessions_users.sql'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (version) VALUES ('0038_drop_sessions_users.sql')`); err != nil {
		t.Fatal(err)
	}

	// When: this build boots again.
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	// Then: the row carries its current name, and the file did not run again.
	if _, ok := appliedNames(t, db)["0043_drop_sessions_users.sql"]; !ok {
		t.Fatal("0043_drop_sessions_users.sql not named")
	}
	assertNothingPending(t, db)
}

func TestMigrate_mirrorsAFileAddedAfterTheRenumbering(t *testing.T) {
	// Given: an adopted database and a file the old runner never had.
	db := openTemp(t)
	applyLegacy(t, db, "")
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations (name) VALUES ('0099_later.sql')`); err != nil {
		t.Fatal(err)
	}

	// When: this build boots again.
	if err := mirrorLegacy(t.Context(), db); err != nil {
		t.Fatal(err)
	}

	// Then: its version is its own name, which no old file matches.
	var version string
	if err := db.QueryRow(`SELECT version FROM schema_migrations WHERE name = '0099_later.sql'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "0099_later.sql" {
		t.Fatalf("version = %q, want the current name", version)
	}
}
