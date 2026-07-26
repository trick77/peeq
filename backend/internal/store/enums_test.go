package store_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"

	"github.com/trick77/peeq/internal/channelvideos"
	"github.com/trick77/peeq/internal/jobs"
	"github.com/trick77/peeq/internal/settings"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
)

// TestEnumConstantsMatchTheCheckConstraints pins every named wire enum to the
// CHECK constraint that is its actual authority. The constants exist so a typo
// is a compile error instead of a row that fails its CHECK at write time —
// which only holds while the two agree.
//
// It reads 0001_init.sql off disk rather than through store's embed, because
// the point is to compare against the migration a reviewer reads, and because
// an external test package (store_test) cannot see the unexported FS. That
// mirrors what ui/src/enumsync.test.ts does from the other side, and the two
// together are what let the TypeScript unions be checked against Go.
//
// It deliberately does NOT cover summarize.Phases: nothing persists a phase, so
// there is no constraint to compare it to. That set's only contract is with the
// SPA, and guarding it is the frontend's job.
func TestEnumConstantsMatchTheCheckConstraints(t *testing.T) {
	// Table + column as they appear in the migration -> the Go set that mirrors
	// it. The table is part of the key on purpose: three tables carry a column
	// called "state", and disambiguating by anything other than the table (by
	// value set, say) lets one enum's constraint vouch for another's Go set —
	// download_jobs.state losing 'canceled' would then silently match
	// summary_jobs.state and pass.
	cases := []struct {
		table  string
		column string
		got    []string
	}{
		{"videos", "status", videos.Statuses},
		{"videos", "summary_status", videos.SummaryStatuses},
		{"videos", "availability", videos.Availabilities},
		{"settings", "cookie_status", settings.CookieStatuses},
		{"download_jobs", "state", jobs.States},
		{"summary_jobs", "state", summaryjobs.States},
		{"channel_videos", "state", channelvideos.States},
	}

	src := readMigration(t, "0001_init.sql")
	for _, c := range cases {
		t.Run(c.table+"."+c.column, func(t *testing.T) {
			name := c.table + "." + c.column
			want := checkValues(t, src, c.table, c.column)
			got := append([]string(nil), c.got...)
			sort.Strings(got)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("%s: %d Go values, %d in the CHECK constraint\n go: %v\nsql: %v",
					name, len(got), len(want), got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("%s: Go and the CHECK constraint disagree\n go: %v\nsql: %v",
						name, got, want)
				}
			}
		})
	}
}

// readMigration loads a migration by name from the package's own directory.
func readMigration(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("migrations", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

var checkRe = regexp.MustCompile(`CHECK\s*\(\s*(\w+)\s+IN\s*\(([^)]*)\)\s*\)`)
var quotedRe = regexp.MustCompile(`'([^']*)'`)

// tableBlock returns the CREATE TABLE body for table, so a column lookup cannot
// stray into a neighbouring table. Anchoring on the "CREATE TABLE " prefix (and
// a word boundary after the name) is what keeps "videos" from matching
// "channel_videos", and the block ends at the next top-level CREATE.
func tableBlock(t *testing.T, src, table string) string {
	t.Helper()
	start := regexp.MustCompile(`(?im)^CREATE TABLE\s+` + regexp.QuoteMeta(table) + `\s*\(`)
	loc := start.FindStringIndex(src)
	if loc == nil {
		t.Fatalf("no CREATE TABLE %s in the migration — was it renamed or dropped?", table)
	}
	rest := src[loc[1]:]
	if end := regexp.MustCompile(`(?im)^CREATE\s`).FindStringIndex(rest); end != nil {
		rest = rest[:end[0]]
	}
	return rest
}

// checkValues returns the values listed in the CHECK constraint on
// table.column. Exactly one such constraint must exist: zero means the column
// or its constraint was renamed or dropped, and more than one means the
// migration is ambiguous and the test would be guessing.
func checkValues(t *testing.T, src, table, column string) []string {
	t.Helper()
	block := tableBlock(t, src, table)

	var found [][]string
	for _, m := range checkRe.FindAllStringSubmatch(block, -1) {
		if m[1] != column {
			continue
		}
		var vals []string
		for _, q := range quotedRe.FindAllStringSubmatch(m[2], -1) {
			vals = append(vals, q[1])
		}
		if len(vals) == 0 {
			continue
		}
		found = append(found, vals)
	}
	switch len(found) {
	case 1:
		return found[0]
	case 0:
		t.Fatalf("no CHECK constraint on %s.%s — was it renamed or dropped?", table, column)
	default:
		t.Fatalf("%d CHECK constraints on %s.%s: %v", len(found), table, column, found)
	}
	return nil
}
