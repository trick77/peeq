package httpapi

import (
	"reflect"
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

func directory() []videos.ChannelRef {
	return []videos.ChannelRef{
		{ID: "UC1", Name: "Veritasium", Handle: "@veritasium"},
		{ID: "UC2", Name: "Kurzgesagt – In a Nutshell", Handle: "@kurzgesagt"},
		{ID: "UC3", Name: "The Verge", Handle: "@theverge"},
		{ID: "UC4", Name: "3Blue1Brown", Handle: "@3blue1brown"},
		{ID: "", Name: "Old Import", Handle: ""},
	}
}

func TestMatchChannelsLadder(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string // matched ids, in directory order
	}{
		{"exact name", "Veritasium", []string{"UC1"}},
		{"exact name, wrong case", "veritasium", []string{"UC1"}},
		{"handle with sigil", "@theverge", []string{"UC3"}},
		{"handle without sigil", "theverge", []string{"UC3"}},
		{"normalized punctuation", "the-verge", []string{"UC3"}},
		{"normalized spacing", "  The   Verge ", []string{"UC3"}},
		// The reader says the short name; the library holds the long one.
		{"substring", "Kurzgesagt", []string{"UC2"}},
		{"typo, one substitution", "Veritaseum", []string{"UC1"}},
		{"typo, transposition", "Vertiasium", []string{"UC1"}},
		{"digits survive normalization", "3blue1brown", []string{"UC4"}},
		// A legacy row with no id is still a real channel and matches by name.
		{"legacy row without an id", "Old Import", []string{""}},
		{"nothing like it", "Numberphile", nil},
		{"empty", "   ", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, c := range matchChannels(directory(), tc.input) {
				got = append(got, c.ID)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("matchChannels(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

// The typo tier must not reach from one real channel to another. It is the only
// tier that can invent a match, so it is withheld from names short enough for
// two edits to rewrite wholesale.
func TestMatchChannelsTypoTierIsBounded(t *testing.T) {
	dir := []videos.ChannelRef{
		{ID: "UCA", Name: "Vox"},
		{ID: "UCB", Name: "Fox"},
	}
	if got := matchChannels(dir, "Sox"); got != nil {
		t.Fatalf("a three-letter name must not fuzzy-match: %+v", got)
	}
	// And three edits is beyond the budget even on a long name.
	// "Verxtybium" is i→x, a→y, s→b against "Veritasium".
	if got := matchChannels(directory(), "Verxtybium"); len(got) != 0 {
		t.Fatalf("distance-3 should not match: %+v", got)
	}
}

// An exact match must never be diluted by the looser tiers that would also have
// matched. Tiers stop at the first one to hit anything.
func TestMatchChannelsExactWinsOverSubstring(t *testing.T) {
	dir := []videos.ChannelRef{
		{ID: "UCA", Name: "Verge"},
		{ID: "UCB", Name: "The Verge Reviews"},
	}
	got := matchChannels(dir, "Verge")
	if len(got) != 1 || got[0].ID != "UCA" {
		t.Fatalf("exact match should win alone, got %+v", got)
	}
}

// Several matches at one tier are all kept. Picking one would be a guess the
// reader cannot see; searching all of them is a wider answer they can.
func TestMatchChannelsAmbiguousKeepsAll(t *testing.T) {
	dir := []videos.ChannelRef{
		{ID: "UCA", Name: "Science Weekly"},
		{ID: "UCB", Name: "Science Monthly"},
	}
	got := matchChannels(dir, "Science")
	if len(got) != 2 {
		t.Fatalf("both substring matches should be kept, got %+v", got)
	}
}

func TestResolveChannels(t *testing.T) {
	deps, db, _ := searchTestDepsWithStores(t)
	if _, err := db.Exec(`INSERT INTO videos (id, url, channel_id, channel_name) VALUES
		('v1','u','UC1','Veritasium'),
		('v2','u','UC2','Kurzgesagt – In a Nutshell'),
		('v3','u','','Old Import')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO channels (id, handle, name) VALUES ('UC1','@veritasium','Veritasium')`); err != nil {
		t.Fatal(err)
	}
	// A channel peeq knows of but holds no videos from. Resolving to it would
	// produce a filter that can only ever return nothing.
	if _, err := db.Exec(`INSERT INTO channels (id, handle, name) VALUES ('UCZ','@ghost','Ghost Channel')`); err != nil {
		t.Fatal(err)
	}
	testee := &server{videos: deps.Videos}

	t.Run("typo resolves silently to the canonical name", func(t *testing.T) {
		got := testee.resolveChannels([]string{"Veritaseum"})
		if !reflect.DeepEqual(got.IDs, []string{"UC1"}) {
			t.Fatalf("ids = %v, want [UC1]", got.IDs)
		}
		if !reflect.DeepEqual(got.Matched, []string{"Veritasium"}) {
			t.Fatalf("matched = %v, want the corrected name", got.Matched)
		}
		if len(got.Unresolved) != 0 || got.Ambiguous {
			t.Fatalf("unexpected %+v", got)
		}
	})

	t.Run("a name the library does not have is reported, not guessed", func(t *testing.T) {
		got := testee.resolveChannels([]string{"Numberphile"})
		if got.any() {
			t.Fatalf("no constraint should be built: %+v", got)
		}
		if !reflect.DeepEqual(got.Unresolved, []string{"Numberphile"}) {
			t.Fatalf("unresolved = %v", got.Unresolved)
		}
	})

	t.Run("a cache-only channel with no videos does not resolve", func(t *testing.T) {
		got := testee.resolveChannels([]string{"Ghost Channel"})
		if got.any() {
			t.Fatalf("a channel with nothing on the shelf must not become a filter: %+v", got)
		}
	})

	t.Run("legacy row lands in the by-name arm", func(t *testing.T) {
		got := testee.resolveChannels([]string{"Old Import"})
		if len(got.IDs) != 0 {
			t.Fatalf("a row with no channel_id must not produce an id: %v", got.IDs)
		}
		if !reflect.DeepEqual(got.Names, []string{"Old Import"}) {
			t.Fatalf("names = %v, want [Old Import]", got.Names)
		}
	})

	t.Run("two named channels both resolve", func(t *testing.T) {
		got := testee.resolveChannels([]string{"Veritasium", "Kurzgesagt"})
		if !reflect.DeepEqual(got.IDs, []string{"UC1", "UC2"}) {
			t.Fatalf("ids = %v", got.IDs)
		}
		if got.Ambiguous {
			t.Fatal("two deliberately named channels are not an ambiguous match")
		}
	})

	t.Run("one resolved and one not", func(t *testing.T) {
		got := testee.resolveChannels([]string{"Veritasium", "Numberphile"})
		if !reflect.DeepEqual(got.IDs, []string{"UC1"}) {
			t.Fatalf("ids = %v, want the one that resolved", got.IDs)
		}
		if !reflect.DeepEqual(got.Unresolved, []string{"Numberphile"}) {
			t.Fatalf("unresolved = %v", got.Unresolved)
		}
	})

	t.Run("no names means no work and no constraint", func(t *testing.T) {
		if got := testee.resolveChannels(nil); got.any() || len(got.Unresolved) != 0 {
			t.Fatalf("unexpected %+v", got)
		}
	})
}

func TestEditDistanceWithin(t *testing.T) {
	cases := []struct {
		a, b string
		max  int
		want bool
	}{
		{"veritasium", "veritasium", 2, true},
		{"veritasium", "veritaseum", 2, true},
		{"veritasium", "vertiasium", 2, true},
		{"veritasium", "veritablum", 2, true},  // two substitutions
		{"veritasium", "verxtybium", 2, false}, // three
		{"veritasium", "kurzgesagt", 2, false},
		{"abc", "abcde", 2, true},
		{"abc", "abcdef", 2, false},
		{"", "ab", 2, true},
	}
	for _, c := range cases {
		if got := editDistanceWithin(c.a, c.b, c.max); got != c.want {
			t.Errorf("editDistanceWithin(%q,%q,%d) = %v, want %v", c.a, c.b, c.max, got, c.want)
		}
	}
}

func TestNormalizeChannel(t *testing.T) {
	cases := map[string]string{
		"Kurzgesagt – In a Nutshell": "kurzgesagtinanutshell",
		"The Verge":                  "theverge",
		"the-verge":                  "theverge",
		"3Blue1Brown":                "3blue1brown",
		"!!!":                        "",
	}
	for in, want := range cases {
		if got := normalizeChannel(in); got != want {
			t.Errorf("normalizeChannel(%q) = %q, want %q", in, got, want)
		}
	}
}
