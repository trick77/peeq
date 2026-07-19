package subtitles

import (
	"strings"
	"testing"
)

const sample = `WEBVTT

00:00:01.000 --> 00:00:03.000
Hello and <c>welcome</c>

00:00:03.000 --> 00:00:05.000
Hello and welcome
to the show

00:01:48.500 --> 00:01:50.000 align:start position:0%
Let's start with the frame
`

func TestParseVTTDedupsAndTimestamps(t *testing.T) {
	p, err := ParseVTT(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Transcript, "<c>") {
		t.Fatal("tags not stripped")
	}
	// "Hello and welcome" must not appear twice back-to-back (rolling dup).
	if strings.Count(p.Transcript, "Hello and welcome") > 1 {
		t.Fatalf("rolling duplicate not collapsed:\n%s", p.Transcript)
	}
	if !strings.Contains(p.Transcript, "to the show") {
		t.Fatal("new text dropped")
	}
	// A cue at 1:48 => 108s exists.
	found := false
	for _, c := range p.Cues {
		if c.StartSeconds == 108 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a cue at 108s; got %+v", p.Cues)
	}
}

const slidingWindowSample = `WEBVTT

00:00:01.000 --> 00:00:03.000
the titanium frame is
lighter this year

00:00:03.000 --> 00:00:05.000
lighter this year
by twelve grams

00:00:05.000 --> 00:00:07.000
by twelve grams
over the last model
`

func TestParseVTTCollapsesSlidingWindow(t *testing.T) {
	p, err := ParseVTT(strings.NewReader(slidingWindowSample))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"the titanium frame is",
		"lighter this year",
		"by twelve grams",
		"over the last model",
	} {
		if c := strings.Count(p.Transcript, line); c != 1 {
			t.Fatalf("expected %q exactly once, got %d in:\n%s", line, c, p.Transcript)
		}
	}
}

const distantRepeatSample = `WEBVTT

00:00:01.000 --> 00:00:03.000
the chorus repeats here

00:00:03.000 --> 00:00:05.000
some unrelated verse line

00:00:05.000 --> 00:00:07.000
another different line entirely

00:00:07.000 --> 00:00:09.000
yet another distinct thought

00:00:09.000 --> 00:00:11.000
the chorus repeats here
`

func TestParseVTTKeepsDistantRepeat(t *testing.T) {
	p, err := ParseVTT(strings.NewReader(distantRepeatSample))
	if err != nil {
		t.Fatal(err)
	}
	if c := strings.Count(p.Transcript, "the chorus repeats here"); c != 2 {
		t.Fatalf("expected distant repeat to survive twice, got %d in:\n%s", c, p.Transcript)
	}
}

func TestParseVTTEmpty(t *testing.T) {
	p, err := ParseVTT(strings.NewReader("WEBVTT\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Transcript != "" || len(p.Cues) != 0 {
		t.Fatalf("expected empty parse, got %+v", p)
	}
}
