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

// entitySample is shared verbatim with the TypeScript mirror's
// "decodes the HTML entities YouTube escapes caption text with" case in
// ui/src/vtt.test.tsx. Both parsers must turn it into entityWant below — the
// panel reads from one and the summary from the other, so a difference here is
// a difference the user sees.
const entitySample = `WEBVTT

00:00:01.000 --> 00:00:03.000
Tom &amp; Jerry &gt; everything else

00:00:03.000 --> 00:00:05.000
He said &quot;don&#39;t&quot; &amp;lt;not a tag&amp;gt;

00:00:05.000 --> 00:00:07.000
spaced&nbsp;out words
`

const entityWant = `Tom & Jerry > everything else He said "don't" &lt;not a tag&gt; spaced out words`

func TestParseVTTDecodesEntities(t *testing.T) {
	p, err := ParseVTT(strings.NewReader(entitySample))
	if err != nil {
		t.Fatal(err)
	}
	if p.Transcript != entityWant {
		t.Fatalf("transcript mismatch:\n got: %s\nwant: %s", p.Transcript, entityWant)
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

// speakerSample is a broadcast-style track: ">>" opening most lines, spaced and
// tight, one line carrying two speakers, one cue that is nothing but a marker,
// and a right-shift operator spoken on screen.
const speakerSample = `WEBVTT

00:00:00.000 --> 00:00:03.000
&gt;&gt; Good evening and welcome

00:00:03.000 --> 00:00:06.000
>>Thanks for having me

00:00:06.000 --> 00:00:09.000
>>>

00:00:09.000 --> 00:00:12.000
So you write cout>>x to shift it

00:00:12.000 --> 00:00:15.000
and >>= does the same in place

00:00:15.000 --> 00:00:18.000
I agree entirely >> So do I
`

func TestParseVTTStripsSpeakerMarkers(t *testing.T) {
	p, err := ParseVTT(strings.NewReader(speakerSample))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		// Escaped as "&gt;&gt;" in the file — the entity decode has to run first
		// or there is nothing for the strip to match.
		"Good evening and welcome",
		// Tight spelling, no space after the marker.
		"Thanks for having me",
		// A right-shift spoken on screen keeps whatever sits tight against it.
		"So you write cout>>x to shift it",
		"and >>= does the same in place",
		// Two speakers in one cue: the marker goes, the sentences stay apart.
		"I agree entirely So do I",
	}
	got := make([]string, len(p.Cues))
	for i, c := range p.Cues {
		got[i] = c.Text
	}
	if len(got) != len(want) {
		t.Fatalf("got %d cues, want %d:\n%q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cue %d = %q, want %q", i, got[i], want[i])
		}
	}
	// The ">>>"-only cue carried no words, so it must be dropped rather than
	// emitted empty — the same way a marker-only sound-event line is.
	for _, c := range p.Cues {
		if strings.TrimSpace(c.Text) == "" {
			t.Fatalf("emitted an empty cue: %+v", p.Cues)
		}
	}
}

func TestStripSpeakerMarkers(t *testing.T) {
	cases := []struct{ in, want string }{
		{">> Hello there", "Hello there"},
		{">>Hello there", "Hello there"},
		{">>> Hello there", "Hello there"},
		{">>", ""},
		{">>>", ""},
		{"one >> two", "one two"},
		{"trailing marker >>", "trailing marker"},
		// A run of markers separated by a space: the leading rule stops at the
		// space, and the mid-line rule finishes the job.
		{">> >> hello", "hello"},
		// The boundary rule: a right-shift keeps whatever sits tight against it.
		{"cout>>x", "cout>>x"},
		{"a >>= b", "a >>= b"},
		{"no markers here", "no markers here"},
		// A no-break space counts as the boundary. Go's \s does not cover one and
		// the JS mirror's does, so leaving it out here would have the transcript
		// panel drop a marker this kept — the divergence spaceRe's comment warns
		// about. These three are shared verbatim with the "no-break space" cases
		// in ui/src/vtt.test.tsx.
		{"one\u00a0>> two", "one\u00a0two"},
		{">>\u00a0Hello", "Hello"},
		{"a >>\u00a0b", "a b"},
	}
	for _, c := range cases {
		if got := StripSpeakerMarkers(c.in); got != c.want {
			t.Errorf("StripSpeakerMarkers(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseVTTCollapsesRollingDuplicatesAcrossMarkers(t *testing.T) {
	// YouTube re-emits a line with the next words appended, marker and all. The
	// collapse only sees the words that survive stripping, so the strip has to
	// run first or these three land as three separate cues.
	const sample = `WEBVTT

00:00:00.000 --> 00:00:02.000
>> I play

00:00:02.000 --> 00:00:04.000
>> I play games

00:00:04.000 --> 00:00:06.000
>> I play games with
`
	p, err := ParseVTT(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Cues) != 1 || p.Cues[0].Text != "I play games with" {
		t.Fatalf("expected one collapsed cue %q, got %+v", "I play games with", p.Cues)
	}
}

// A speaker echoing the words before them is not a rolling window. The marker
// is what tells the two apart, so the collapse has to compare text that still
// carries it — strip first and one speaker's line, and its start second, are
// silently swallowed. Mirrored in ui/src/vtt.test.tsx.
func TestParseVTTKeepsAnEchoAcrossASpeakerChange(t *testing.T) {
	for _, tc := range []struct {
		name string
		vtt  string
		want []Cue
	}{
		{
			name: "the next speaker extends the words before them",
			vtt: "WEBVTT\n\n00:00:00.000 --> 00:00:03.000\nYeah\n\n" +
				"00:00:03.000 --> 00:00:06.000\n>> Yeah, exactly.\n",
			want: []Cue{{StartSeconds: 0, Text: "Yeah"}, {StartSeconds: 3, Text: "Yeah, exactly."}},
		},
		{
			name: "the next speaker repeats them outright",
			vtt: "WEBVTT\n\n00:00:00.000 --> 00:00:03.000\nI think so.\n\n" +
				"00:00:03.000 --> 00:00:06.000\n>> I think so.\n",
			want: []Cue{{StartSeconds: 0, Text: "I think so."}, {StartSeconds: 3, Text: "I think so."}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := ParseVTT(strings.NewReader(tc.vtt))
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Cues) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", p.Cues, tc.want)
			}
			for i := range tc.want {
				if p.Cues[i] != tc.want[i] {
					t.Errorf("cue %d = %+v, want %+v", i, p.Cues[i], tc.want[i])
				}
			}
		})
	}
}

// A marker wedged against a sound event has no whitespace in front of it, so
// the mid-line rule cannot see it until the bracket is gone. Stripping after
// the sound-event pass is what catches this.
func TestParseVTTStripsAMarkerTightAgainstASoundEvent(t *testing.T) {
	p, err := ParseVTT(strings.NewReader(
		"WEBVTT\n\n00:00:00.000 --> 00:00:03.000\n[MUSIC]>> Hello there\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Cues) != 1 || p.Cues[0].Text != "Hello there" {
		t.Fatalf("got %+v, want one cue %q", p.Cues, "Hello there")
	}
}

// TestIsNonSpeechSurvivesMarkerStripping guards a regression the strip could
// otherwise introduce silently.
//
// IsNonSpeech divides by len(strings.Fields(Transcript)), and every ">>" used
// to be its own whitespace-delimited field — it counted as a word. Now that the
// parser removes them, the word count of every marker-carrying track drops. A
// sparse interview that also has [Music] in at least a quarter of its cues has
// the marker half of the test already satisfied, so if the word drop pushes it
// under 25 WPM it silently becomes summary_status=no_transcript, "no speech
// (music only)".
//
// If this ever fails, the thresholds in vtt.go are the thing to look at, not
// this test — they are documented as deliberately conservative on purpose.
func TestIsNonSpeechSurvivesMarkerStripping(t *testing.T) {
	const sample = `WEBVTT

00:00:00.000 --> 00:00:15.000
>> [Music] Welcome back to the show tonight everyone

00:00:15.000 --> 00:00:30.000
>> Thanks very much for having me here again

00:00:30.000 --> 00:00:45.000
>> [Music] So tell us how the new album came together

00:00:45.000 --> 00:01:00.000
>> It took the better part of two years to finish

00:01:00.000 --> 00:01:15.000
>> [Music] And then the band toured right through the winter

00:01:15.000 --> 00:01:30.000
>> That sounds completely exhausting to me honestly

00:01:30.000 --> 00:01:45.000
>> [Music] We loved every single minute of all of it

00:01:45.000 --> 00:02:00.000
>> Wonderful, thank you so much for coming in
`
	p, err := ParseVTT(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if p.SoundEventCues*4 < len(p.Cues) {
		t.Fatalf("sample no longer exercises the marker guard: %d of %d cues marked",
			p.SoundEventCues, len(p.Cues))
	}
	if p.IsNonSpeech(120) {
		t.Fatalf("a sparse interview must keep its summary; %d words over %d cues (%d marked)",
			len(strings.Fields(p.Transcript)), len(p.Cues), p.SoundEventCues)
	}
}
