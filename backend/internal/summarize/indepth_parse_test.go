package summarize

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/trick77/peeq/internal/rag"
)

// The UI reads the same pair (ui/src/inDepth.test.ts). If this fails after a
// change to one parser, the other has drifted: the reader would see sections
// Ask does not search, or the reverse.
func TestParseInDepthMatchesTheSharedFixture(t *testing.T) {
	text, err := os.ReadFile("testdata/indepth_parse.txt")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/indepth_parse.json")
	if err != nil {
		t.Fatal(err)
	}
	var want struct {
		Lead     []string `json:"lead"`
		Sections []struct {
			Heading string   `json:"heading"`
			Stamp   *string  `json:"stamp"`
			Seconds *int     `json:"seconds"`
			Body    []string `json:"body"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}

	got := parseInDepth(string(text))
	if !reflect.DeepEqual(got.Lead, want.Lead) {
		t.Errorf("lead = %q, want %q", got.Lead, want.Lead)
	}
	if len(got.Sections) != len(want.Sections) {
		t.Fatalf("sections = %d, want %d: %+v", len(got.Sections), len(want.Sections), got.Sections)
	}
	for i, w := range want.Sections {
		g := got.Sections[i]
		wantStamp, wantSeconds := "", -1
		if w.Stamp != nil {
			wantStamp, wantSeconds = *w.Stamp, *w.Seconds
		}
		if g.Heading != w.Heading || g.Stamp != wantStamp || g.Seconds != wantSeconds || !reflect.DeepEqual(g.Body, w.Body) {
			t.Errorf("section %d = %+v, want %+v (stamp %q, seconds %d)", i, g, w, wantStamp, wantSeconds)
		}
	}
}

// What gets indexed: one section each, paragraphs rejoined, an unstamped one
// at 0 like the summary chunk. The lead is left out; the summary chunk
// already carries it.
func TestInDepthSectionsForIndex(t *testing.T) {
	got := inDepthSections("Lead.\n\n### First [1:05]\n\nOne.\n\nTwo.\n\n### Unstamped\n\nThree.")
	want := []rag.InDepthSection{
		{Heading: "First", StartSeconds: 65, Body: "One.\n\nTwo."},
		{Heading: "Unstamped", StartSeconds: 0, Body: "Three."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sections = %+v, want %+v", got, want)
	}
	if got := inDepthSections(""); got != nil {
		t.Errorf("empty body = %+v, want nil", got)
	}
}
