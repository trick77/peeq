package httpapi

import (
	"reflect"
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/videos"
)

// The filter parse is deliberately paranoid, because its failure mode is
// invisible: a filter the model imagined SHRINKS the search, and a reader
// cannot tell a library that holds nothing from a search that was narrowed past
// what they asked for. These cases are the ones a small model actually produces.
func TestParseUnderstandingFilters(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		want        queryFilters
		wantDropped []string
	}{
		{
			name: "no filters key at all",
			raw:  `{"topic":"ontology","counting":false}`,
			want: queryFilters{},
		},
		{
			name: "empty filters object",
			raw:  `{"topic":"ontology","counting":false,"filters":{}}`,
			want: queryFilters{},
		},
		{
			name: "unwatched",
			raw:  `{"topic":"ontology","counting":false,"filters":{"watched":"unwatched"}}`,
			want: queryFilters{Watched: watchedUnwatched},
		},
		{
			name: "one channel",
			raw:  `{"topic":"ontology","counting":false,"filters":{"channels":["Veritasium"]}}`,
			want: queryFilters{Channels: []string{"Veritasium"}},
		},
		{
			name: "two channels, a comparison",
			raw:  `{"topic":"dark matter","counting":false,"filters":{"channels":["Veritasium","Kurzgesagt"]}}`,
			want: queryFilters{Channels: []string{"Veritasium", "Kurzgesagt"}},
		},
		{
			name: "everything at once",
			raw: `{"topic":"ontology","counting":false,"filters":{"channels":["Veritasium"],` +
				`"watched":"watched","favorite":true,"category":"science",` +
				`"after":"2026-01-01","before":"2026-06-30"}}`,
			want: queryFilters{
				Channels: []string{"Veritasium"}, Watched: watchedWatched, Favorite: true,
				Category: "science", After: "2026-01-01", Before: "2026-06-30",
			},
		},
		{
			// A model improvising a third watch state. It maps to no column, so
			// it is dropped rather than guessed at.
			name:        "invented watch state is dropped",
			raw:         `{"topic":"x","counting":false,"filters":{"watched":"partially"}}`,
			want:        queryFilters{},
			wantDropped: []string{"watched:partially"},
		},
		{
			name:        "invented category is dropped",
			raw:         `{"topic":"x","counting":false,"filters":{"category":"philosophy"}}`,
			want:        queryFilters{},
			wantDropped: []string{"category:philosophy"},
		},
		{
			// 'uncategorized' is a state peeq assigns to unclassified videos,
			// never an answer to a question about a subject area.
			name:        "uncategorized is not a filterable category",
			raw:         `{"topic":"x","counting":false,"filters":{"category":"uncategorized"}}`,
			want:        queryFilters{},
			wantDropped: []string{"category:uncategorized"},
		},
		{
			// The display label is a legitimate reply shape; NormalizeCategory
			// maps it back to the id rather than dropping a correct answer.
			name: "category display label resolves to its id",
			raw:  `{"topic":"x","counting":false,"filters":{"category":"Science & Research"}}`,
			want: queryFilters{Category: "science"},
		},
		{
			name:        "a date the model did not resolve is dropped",
			raw:         `{"topic":"x","counting":false,"filters":{"after":"last week"}}`,
			want:        queryFilters{},
			wantDropped: []string{"after:last week"},
		},
		{
			name:        "a bare year is not a date",
			raw:         `{"topic":"x","counting":false,"filters":{"after":"2026"}}`,
			want:        queryFilters{},
			wantDropped: []string{"after:2026"},
		},
		{
			// An inverted range admits nothing, which the reader would read as
			// "your library has nothing on this". Both bounds go.
			name:        "inverted date range drops both bounds",
			raw:         `{"topic":"x","counting":false,"filters":{"after":"2026-06-01","before":"2026-01-01"}}`,
			want:        queryFilters{},
			wantDropped: []string{"dates:inverted"},
		},
		{
			name:        "too many channels",
			raw:         `{"topic":"x","counting":false,"filters":{"channels":["a","b","c","d","e","f"]}}`,
			want:        queryFilters{Channels: []string{"a", "b", "c", "d"}},
			wantDropped: []string{"channels:too-many"},
		},
		{
			// A blank entry is nothing to report. An overlong one is a constraint
			// the reader may have asked for and did not get, so it is named in
			// the log rather than vanishing.
			name: "blank channel names are skipped, overlong ones are named",
			raw: `{"topic":"x","counting":false,"filters":{"channels":["  ","` +
				strings.Repeat("z", understandMaxChannelRunes+1) + `","Veritasium"]}}`,
			want: queryFilters{Channels: []string{"Veritasium"}},
			wantDropped: []string{
				"channel:" + strings.Repeat("z", understandMaxChannelRunes) + "…",
			},
		},
		{
			name: "control characters are stripped from a channel name",
			raw:  "{\"topic\":\"x\",\"counting\":false,\"filters\":{\"channels\":[\"Verita\\nsium\"]}}",
			want: queryFilters{Channels: []string{"Verita sium"}},
		},
		{
			// The nested filters object must not confuse the outermost-object
			// scan, which takes the first { and the last }.
			name: "filters survive surrounding prose",
			raw:  `Here you go: {"topic":"x","counting":false,"filters":{"watched":"unwatched"}} done`,
			want: queryFilters{Watched: watchedUnwatched},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, dropped, ok := parseUnderstanding(tc.raw)
			if !ok {
				t.Fatal("parse failed")
			}
			if !reflect.DeepEqual(got.Filters, tc.want) {
				t.Errorf("filters = %+v, want %+v", got.Filters, tc.want)
			}
			if !reflect.DeepEqual(dropped, tc.wantDropped) {
				t.Errorf("dropped = %v, want %v", dropped, tc.wantDropped)
			}
		})
	}
}

// Every category the prompt offers has to be one the parse will accept, or the
// model is being told to pick from a list whose answers get thrown away.
func TestUnderstandPromptCategoriesRoundTrip(t *testing.T) {
	for _, id := range videos.CategoryIDs() {
		if !strings.Contains(understandSystemPrompt, id) {
			t.Errorf("category %q is missing from the prompt", id)
		}
	}
	for _, id := range videos.CategoryIDs() {
		if id == videos.UncategorizedCategory {
			continue
		}
		raw := `{"topic":"x","counting":false,"filters":{"category":"` + id + `"}}`
		got, dropped, ok := parseUnderstanding(raw)
		if !ok || got.Filters.Category != id {
			t.Errorf("category %q did not survive the parse: %+v dropped=%v", id, got.Filters, dropped)
		}
	}
}

// The prompt has to carry the two questions this whole feature was built for,
// as worked examples. Losing them in an edit is silent and expensive.
func TestUnderstandPromptCarriesTheFilterExamples(t *testing.T) {
	for _, want := range []string{
		`{"watched": "unwatched"}`,
		`{"channels": ["Veritasium"]}`,
		"OMIT ANY FILTER THE QUESTION DOES NOT ACTUALLY STATE",
	} {
		if !strings.Contains(understandSystemPrompt, want) {
			t.Errorf("prompt is missing %q", want)
		}
	}
}

func TestQueryFiltersEmpty(t *testing.T) {
	if !(queryFilters{}).empty() {
		t.Fatal("zero queryFilters must be empty")
	}
	for name, f := range map[string]queryFilters{
		"channels": {Channels: []string{"a"}},
		"watched":  {Watched: watchedUnwatched},
		"favorite": {Favorite: true},
		"category": {Category: "science"},
		"after":    {After: "2026-01-01"},
		"before":   {Before: "2026-01-01"},
	} {
		if f.empty() {
			t.Errorf("%s: empty() = true, want false", name)
		}
	}
}

func TestDescribeFilterShowsOnlyWhatWasApplied(t *testing.T) {
	// A channel the model named but the library does not have must NOT appear
	// as though it narrowed the search.
	got := describeFilter(
		queryFilters{Channels: []string{"Veritaseum"}, Watched: watchedUnwatched},
		channelResolution{Unresolved: []string{"Veritaseum"}},
	)
	if !reflect.DeepEqual(got, []string{"unwatched"}) {
		t.Fatalf("describeFilter = %v, want only [unwatched]", got)
	}

	// A resolved typo shows the CANONICAL name, so the correction is visible
	// even though no sentence was spent on it.
	got = describeFilter(
		queryFilters{Channels: []string{"Veritaseum"}},
		channelResolution{Matched: []string{"Veritasium"}, IDs: []string{"UCA"}},
	)
	if !reflect.DeepEqual(got, []string{"Veritasium"}) {
		t.Fatalf("describeFilter = %v, want [Veritasium]", got)
	}

	// Categories are shown by label, not by their machine id.
	got = describeFilter(queryFilters{Category: "science"}, channelResolution{})
	if !reflect.DeepEqual(got, []string{"Science & Research"}) {
		t.Fatalf("describeFilter = %v, want the display label", got)
	}
}

func TestBuildFilterWatchedIsThreeValued(t *testing.T) {
	s := &server{}
	if f := s.buildFilter(queryFilters{}, channelResolution{}); f.Watched != nil {
		t.Fatal("an unsaid watch state must stay nil, not default to false")
	}
	f := s.buildFilter(queryFilters{Watched: watchedUnwatched}, channelResolution{})
	if f.Watched == nil || *f.Watched {
		t.Fatalf("unwatched must map to watched=false, got %v", f.Watched)
	}
	f = s.buildFilter(queryFilters{Watched: watchedWatched}, channelResolution{})
	if f.Watched == nil || !*f.Watched {
		t.Fatalf("watched must map to watched=true, got %v", f.Watched)
	}
	// There is no way to ask for non-favorites, and none is wanted.
	if f := s.buildFilter(queryFilters{Favorite: false}, channelResolution{}); f.Favorite != nil {
		t.Fatal("favorite=false must not become a constraint")
	}
}
