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

// What gets indexed: one section each, paragraphs rejoined, the lead left out
// (the summary chunk already carries it). A section with no stamp, or one past
// the video's end, has no true moment and is not indexed: parked at 0s it
// would seek to the wrong place, borrow the first chapter's name and pose as
// a moment. A real [0:00] stamp is kept.
func TestInDepthSectionsForIndex(t *testing.T) {
	body := "Lead.\n\n### Intro point [0:00]\n\nZero.\n\n### First [1:05]\n\nOne.\n\nTwo.\n\n" +
		"### Unstamped\n\nThree.\n\n### Past the end [20:00]\n\nFour."
	got := inDepthSections(body, 600)
	want := []rag.InDepthSection{
		{Heading: "Intro point", StartSeconds: 0, Body: "Zero."},
		{Heading: "First", StartSeconds: 65, Body: "One.\n\nTwo."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sections = %+v, want %+v", got, want)
	}
	// An unknown duration bounds nothing.
	if got := inDepthSections(body, 0); len(got) != 3 {
		t.Errorf("unknown duration: %d sections, want 3 (only the unstamped one dropped)", len(got))
	}
	if got := inDepthSections("", 600); got != nil {
		t.Errorf("empty body = %+v, want nil", got)
	}
}
