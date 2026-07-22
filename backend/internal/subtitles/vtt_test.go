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

// musicSample is trimmed from the real auto-captions of a music video that
// peeq summarized into a confident description of a song it could not hear:
// every cue is a [Music] marker plus a stray lyric fragment.
const musicSample = `WEBVTT

00:00:04.000 --> 00:00:07.000
[Music] I play games with

00:00:07.000 --> 00:00:10.000
[Music] you yeah

00:00:10.000 --> 00:00:13.000
[Music] I'm give a

00:00:13.000 --> 00:00:16.000
[Music] back I give it

00:00:16.000 --> 00:00:19.000
[Music]

00:00:19.000 --> 00:00:22.000
yeah I would never

00:00:22.000 --> 00:00:25.000
[Music] you scar

00:00:25.000 --> 00:00:28.000
[Music] [Applause]

00:00:28.000 --> 00:00:31.000
[Music] I get my
`

func TestParseVTTStripsSoundEvents(t *testing.T) {
	p, err := ParseVTT(strings.NewReader(musicSample))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.Transcript, "[") {
		t.Fatalf("expected all bracketed markers stripped, got:\n%s", p.Transcript)
	}
	if !strings.Contains(p.Transcript, "I play games with") {
		t.Fatalf("expected the lyric words to survive, got:\n%s", p.Transcript)
	}
	// The marker-only cues carry no words, so they must not become empty cues.
	for _, c := range p.Cues {
		if strings.TrimSpace(c.Text) == "" {
			t.Fatalf("emitted an empty cue: %+v", p.Cues)
		}
	}
	if p.SoundEventCues == 0 {
		t.Fatal("expected the surviving cues to be counted as sound-event cues")
	}
}

func TestStripSoundEvents(t *testing.T) {
	cases := []struct {
		in, want string
		stripped bool
	}{
		{"[Music] hello", "hello", true},
		{"[Music]", "", true},
		{"♪ la la la ♪", "la la la", true},
		{"(applause) thanks", "thanks", true},
		{"(APPLAUSE) thanks", "thanks", true},
		// Real speech uses parentheses; an open rule would eat this.
		{"the result (roughly) doubled", "the result (roughly) doubled", false},
		{"plain words", "plain words", false},
	}
	for _, c := range cases {
		got, stripped := stripSoundEvents(c.in)
		if got != c.want || stripped != c.stripped {
			t.Errorf("stripSoundEvents(%q) = (%q, %v), want (%q, %v)",
				c.in, got, stripped, c.want, c.stripped)
		}
	}
}

func TestIsNonSpeechFlagsAMusicVideo(t *testing.T) {
	p, err := ParseVTT(strings.NewReader(musicSample))
	if err != nil {
		t.Fatal(err)
	}
	if !p.IsNonSpeech(180) {
		t.Fatalf("expected a music video to be flagged; %d words over %d cues (%d marked)",
			len(strings.Fields(p.Transcript)), len(p.Cues), p.SoundEventCues)
	}
}

func TestIsNonSpeechSpares(t *testing.T) {
	dense := Parsed{
		Transcript:     strings.Repeat("word ", 300),
		Cues:           []Cue{{StartSeconds: 0, Text: "x"}},
		SoundEventCues: 1,
	}
	if dense.IsNonSpeech(120) {
		t.Error("a normal talk (150 wpm) must never be flagged")
	}

	// A quiet documentary or a silent screencast: barely any words, but no
	// sound-event markers either. The marker guard is what protects it.
	quiet := Parsed{
		Transcript:     "a few words only",
		Cues:           []Cue{{StartSeconds: 0, Text: "a few words only"}},
		SoundEventCues: 0,
	}
	if quiet.IsNonSpeech(600) {
		t.Error("a caption track with no sound-event markers must never be flagged")
	}

	// A short clip must not divide by ~0 and look like dense speech.
	short := Parsed{
		Transcript:     "la la",
		Cues:           []Cue{{StartSeconds: 0, Text: "la la"}},
		SoundEventCues: 1,
	}
	if !short.IsNonSpeech(2) {
		t.Error("a 2-second music clip should still be flagged via the duration floor")
	}

	empty := Parsed{}
	if empty.IsNonSpeech(100) {
		t.Error("nothing parsed at all is the caller's empty-transcript case, not this one")
	}
}
