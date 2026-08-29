package summarize

import (
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/subtitles"
)

func cues(pairs ...any) []subtitles.Cue {
	out := make([]subtitles.Cue, 0, len(pairs)/2)
	for i := 0; i < len(pairs); i += 2 {
		out = append(out, subtitles.Cue{StartSeconds: pairs[i].(int), Text: pairs[i+1].(string)})
	}
	return out
}

func TestSuppressedSpans_selectsCategoriesAndMerges(t *testing.T) {
	// intro is deliberately absent from the result: it is a real part of the
	// video and a legitimate chapter, and it covers t=0 where the first chapter
	// sits. The two sponsor entries overlap and must merge into one span.
	raw := `[
		{"category":"intro","start_time":0,"end_time":12},
		{"category":"sponsor","start_time":60,"end_time":90},
		{"category":"sponsor","start_time":85,"end_time":120},
		{"category":"outro","start_time":600,"end_time":640},
		{"category":"chapter","start_time":300,"end_time":310}
	]`
	got := suppressedSpans(raw)
	want := []span{{60, 120}, {600, 640}}
	if len(got) != len(want) {
		t.Fatalf("spans = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("spans = %v, want %v", got, want)
		}
	}
	if covers(got, 5) {
		t.Error("an intro second was suppressed; intro is a valid chapter")
	}
	if !covers(got, 100) || !covers(got, 60) || !covers(got, 119) {
		t.Error("a sponsor second was not covered")
	}
	// The end is exclusive: second 120 is where content resumes, and a chapter
	// a creator placed exactly there must survive.
	if covers(got, 120) {
		t.Error("the first second after a sponsor read was suppressed")
	}
}

func TestSuppressedSpans_widensFractionalBoundsOutward(t *testing.T) {
	// Cue and chapter timestamps are whole seconds. A segment ending at 90.4
	// still owns second 90, and rounding down would leak it to the model.
	got := suppressedSpans(`[{"category":"sponsor","start_time":60.7,"end_time":90.4}]`)
	if len(got) != 1 || got[0].start != 60 || got[0].end != 91 {
		t.Fatalf("span = %v, want {60 91}", got)
	}
}

func TestSuppressedSpans_toleratesUnusableInput(t *testing.T) {
	// A quality filter on a best-effort crowd feed must never fail a summary.
	for _, raw := range []string{"", "   ", "not json", "{}", "[]",
		`[{"category":"sponsor","start_time":90,"end_time":60}]`} {
		if got := suppressedSpans(raw); got != nil {
			t.Errorf("suppressedSpans(%q) = %v, want nil", raw, got)
		}
	}
}

func TestStripCues_removesCoveredCuesAndRebuildsTranscript(t *testing.T) {
	p := subtitles.Parsed{
		Cues: cues(0, "welcome back", 60, "this video is sponsored by", 75, "use code PEEQ",
			130, "anyway the actual topic"),
	}
	p.Transcript = strings.Join([]string{"welcome back", "this video is sponsored by",
		"use code PEEQ", "anyway the actual topic"}, " ")

	got, fellBack := stripCues(p, []span{{60, 120}})
	if fellBack {
		t.Fatal("the filter reported a fallback it did not take")
	}

	if len(got.Cues) != 2 {
		t.Fatalf("cues = %v, want the two outside the span", got.Cues)
	}
	// Surviving cues keep their ORIGINAL timestamps: nothing is re-timed, so a
	// chapter the model proposes still lines up with the video.
	if got.Cues[0].StartSeconds != 0 || got.Cues[1].StartSeconds != 130 {
		t.Fatalf("timestamps were rewritten: %v", got.Cues)
	}
	if got.Transcript != "welcome back anyway the actual topic" {
		t.Fatalf("transcript = %q", got.Transcript)
	}
	if strings.Contains(got.Transcript, "PEEQ") {
		t.Error("sponsor text survived into the transcript the model reads")
	}
}

func TestStripCues_keepsEverythingWhenNothingIsSuppressed(t *testing.T) {
	p := subtitles.Parsed{Cues: cues(0, "a", 10, "b"), Transcript: "a b"}
	got, fellBack := stripCues(p, nil)
	if fellBack {
		t.Fatal("no spans must not read as a fallback")
	}
	if len(got.Cues) != 2 || got.Transcript != "a b" {
		t.Fatalf("unfiltered parse was modified: %+v", got)
	}
}

func TestStripCues_fallsBackWhenSegmentsWouldEmptyTheTranscript(t *testing.T) {
	// Bad segment data must not be able to turn a real video into "no
	// transcript", which is what the worker does with an empty one.
	p := subtitles.Parsed{Cues: cues(0, "a", 10, "b"), Transcript: "a b"}
	got, fellBack := stripCues(p, []span{{0, 3600}})
	if len(got.Cues) != 2 || got.Transcript != "a b" {
		t.Fatalf("an all-covering span emptied the transcript: %+v", got)
	}
	// The caller has to hear about it: the same bad spans would otherwise strip
	// every chapter and key point in the output backstop.
	if !fellBack {
		t.Error("the fallback was taken silently; the caller still trusts the spans")
	}
}

func TestDropCovered_removesChaptersAndKeyPointsInsideSpans(t *testing.T) {
	chapters := []Chapter{
		{TS: 0, Title: "Intro", Source: "yt-dlp"},
		{TS: 70, Title: "The NordVPN offer", Source: "llm"},
		{TS: 130, Title: "The actual topic", Source: "llm"},
	}
	keyPoints := []KeyPoint{
		{TS: 65, Text: "use code PEEQ for 70% off"},
		{TS: 140, Text: "a real point"},
	}
	gotC, gotK := dropCovered([]span{{60, 120}}, chapters, keyPoints)

	if len(gotC) != 2 || gotC[0].Title != "Intro" || gotC[1].Title != "The actual topic" {
		t.Fatalf("chapters = %+v", gotC)
	}
	if len(gotK) != 1 || gotK[0].Text != "a real point" {
		t.Fatalf("key points = %+v", gotK)
	}
}

func TestDropCovered_alsoDropsAYtdlpChapterInsideASponsorRead(t *testing.T) {
	// A creator who titles their own chapter over the sponsor read is naming the
	// same thing the model would have. The reader is complaining about what the
	// Player shows, not about which component wrote it.
	chapters := []Chapter{{TS: 70, Title: "Sponsor", Source: "yt-dlp"}}
	gotC, _ := dropCovered([]span{{60, 120}}, chapters, nil)
	if len(gotC) != 0 {
		t.Fatalf("chapters = %+v, want the sponsor chapter dropped", gotC)
	}
}

func TestDropCovered_isANoOpWithoutSpans(t *testing.T) {
	chapters := []Chapter{{TS: 70, Title: "Kept", Source: "llm"}}
	keyPoints := []KeyPoint{{TS: 70, Text: "kept"}}
	gotC, gotK := dropCovered(nil, chapters, keyPoints)
	if len(gotC) != 1 || len(gotK) != 1 {
		t.Fatalf("chapters=%+v key_points=%+v", gotC, gotK)
	}
}
