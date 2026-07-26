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
	// Column name as it appears in the migration -> the Go set that mirrors it.
	cases := []struct {
		name   string
		column string
		got    []string
	}{
		{"videos.status", "status", videos.Statuses},
		{"videos.summary_status", "summary_status", videos.SummaryStatuses},
		{"settings.cookie_status", "cookie_status", settings.CookieStatuses},
		{"jobs.state", "state", jobs.States},
		{"summary_jobs.state", "state", summaryjobs.States},
		{"channel_videos.state", "state", channelvideos.States},
	}

	src := readMigration(t, "0001_init.sql")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want := checkValues(t, src, c.column, c.got)
			got := append([]string(nil), c.got...)
			sort.Strings(got)
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("%s: %d Go values, %d in the CHECK constraint\n go: %v\nsql: %v",
					c.name, len(got), len(want), got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("%s: Go and the CHECK constraint disagree\n go: %v\nsql: %v",
						c.name, got, want)
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

// checkValues returns the values listed in the CHECK constraint on column.
//
// Several tables share a column name ("state" appears on jobs, summary_jobs and
// channel_videos), so a name alone is ambiguous. want disambiguates: among the
// constraints on that column, the one whose value set matches is the one meant.
// That is not circular — it still fails when NO constraint matches, which is
// exactly the drift this test exists to catch, and it fails loudly by listing
// every candidate rather than silently passing.
func checkValues(t *testing.T, src, column string, want []string) []string {
	t.Helper()
	wantSet := map[string]bool{}
	for _, v := range want {
		wantSet[v] = true
	}

	var candidates [][]string
	for _, m := range checkRe.FindAllStringSubmatch(src, -1) {
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
		candidates = append(candidates, vals)
		if len(vals) == len(want) {
			same := true
			for _, v := range vals {
				if !wantSet[v] {
					same = false
					break
				}
			}
			if same {
				return vals
			}
		}
	}
	if len(candidates) == 0 {
		t.Fatalf("no CHECK constraint found on column %q — was it renamed or dropped?", column)
	}
	t.Fatalf("no CHECK constraint on column %q matches the Go set %v; candidates in the migration: %v",
		column, want, candidates)
	return nil
}
