package rag

import (
	"strings"
	"testing"

	"github.com/trick77/peeq/internal/subtitles"
)

// cues builds a cue list at 10-second spacing, one cue per text.
func cues(texts ...string) []subtitles.Cue {
	out := make([]subtitles.Cue, 0, len(texts))
	for i, t := range texts {
		out = append(out, subtitles.Cue{StartSeconds: i * 10, Text: t})
	}
	return out
}

func parsedFrom(cs []subtitles.Cue) subtitles.Parsed {
	texts := make([]string, 0, len(cs))
	for _, c := range cs {
		texts = append(texts, c.Text)
	}
	return subtitles.Parsed{Cues: cs, Transcript: strings.Join(texts, " ")}
}

func kindsOf(rows []ChunkRow) map[string]int {
	m := map[string]int{}
	for _, r := range rows {
		m[r.Kind]++
	}
	return m
}

func TestBuildVideoChunksEmitsAllThreeKinds(t *testing.T) {
	cs := cues("intro words here", "more talk about sodium", "and now potassium", "closing remarks")
	rows := BuildVideoChunks(parsedFrom(cs), "a prose summary", []Chapter{
		{TS: 0, Title: "Intro"},
		{TS: 20, Title: "Minerals"},
	})
	got := kindsOf(rows)
	if got[KindTranscript] == 0 {
		t.Error("no transcript chunks")
	}
	if got[KindSummary] != 1 {
		t.Errorf("summary chunks = %d, want 1", got[KindSummary])
	}
	if got[KindChapter] != 2 {
		t.Errorf("chapter chunks = %d, want 2", got[KindChapter])
	}
}

// Ordinals key search hits across lanes, and rag.Chunk restarts its own Ordinal
// at 0 on every call. A per-group counter would therefore make two unrelated
// chunks share an identity and silently merge during fusion.
func TestBuildVideoChunksOrdinalsAreUniqueAndDense(t *testing.T) {
	// A chapter long enough to be split, so the sub-chunk ordinals rag.Chunk
	// hands back would collide with the transcript ones if they were used.
	long := strings.Repeat("word ", 2000)
	cs := []subtitles.Cue{
		{StartSeconds: 0, Text: "opening"},
		{StartSeconds: 10, Text: long},
		{StartSeconds: 20, Text: "closing"},
	}
	rows := BuildVideoChunks(parsedFrom(cs), "summary text", []Chapter{
		{TS: 0, Title: "One"},
		{TS: 10, Title: "Two"},
	})
	if len(rows) < 4 {
		t.Fatalf("expected several chunks, got %d", len(rows))
	}
	seen := map[int]bool{}
	for i, r := range rows {
		if seen[r.Ordinal] {
			t.Fatalf("duplicate ordinal %d at index %d", r.Ordinal, i)
		}
		seen[r.Ordinal] = true
		if r.Ordinal != i {
			t.Errorf("ordinal %d at index %d — ordinals must be dense and in order", r.Ordinal, i)
		}
	}
}

func TestChapterChunkCarriesTitleAndItsOwnSpan(t *testing.T) {
	cs := cues("nothing to do with it", "sodium losses matter", "potassium is different", "wrap up")
	rows := BuildVideoChunks(parsedFrom(cs), "", []Chapter{
		{TS: 10, Title: "Sodium"},
		{TS: 20, Title: "Potassium"},
	})
	var sodium, potassium ChunkRow
	for _, r := range rows {
		if r.Kind != KindChapter {
			continue
		}
		switch {
		case strings.Contains(r.Text, "Chapter: Sodium"):
			sodium = r
		case strings.Contains(r.Text, "Chapter: Potassium"):
			potassium = r
		}
	}
	if sodium.Text == "" || potassium.Text == "" {
		t.Fatalf("both chapters should produce a chunk: %+v", rows)
	}
	if !strings.Contains(sodium.Text, "sodium losses matter") {
		t.Errorf("chapter chunk missing its own transcript: %q", sodium.Text)
	}
	// The span is exclusive of the next chapter's start, or every chapter would
	// carry the rest of the video and the vectors would be near-identical.
	if strings.Contains(sodium.Text, "potassium is different") {
		t.Errorf("chapter chunk bled into the next chapter: %q", sodium.Text)
	}
	if strings.Contains(sodium.Text, "nothing to do with it") {
		t.Errorf("chapter chunk included text before its start: %q", sodium.Text)
	}
	if sodium.StartSeconds != 10 {
		t.Errorf("start = %d, want the chapter's own timestamp 10", sodium.StartSeconds)
	}
}

// A heading alone is ~5 tokens. A vector built from that little text sits close
// to far too many queries, so it would add noise to every search rather than
// precision to the ones about that chapter.
func TestChapterWithNoTranscriptIsNotIndexed(t *testing.T) {
	cs := cues("only cue here")
	rows := BuildVideoChunks(parsedFrom(cs), "", []Chapter{
		{TS: 0, Title: "Real"},
		{TS: 9999, Title: "Beyond the end of the video"},
	})
	for _, r := range rows {
		if r.Kind == KindChapter && strings.Contains(r.Text, "Beyond the end") {
			t.Fatalf("empty chapter was indexed: %q", r.Text)
		}
	}
	if kindsOf(rows)[KindChapter] != 1 {
		t.Errorf("chapter chunks = %d, want 1", kindsOf(rows)[KindChapter])
	}
}

func TestChapterSplitsWhenTooLongAndKeepsTitleOnEveryPart(t *testing.T) {
	// One cue per 10s, each a sentence, adding up well past MaxTokens.
	texts := make([]string, 40)
	for i := range texts {
		texts[i] = strings.Repeat("some transcript words ", 20)
	}
	cs := cues(texts...)
	rows := BuildVideoChunks(parsedFrom(cs), "", []Chapter{{TS: 0, Title: "Long"}})

	parts := make([]ChunkRow, 0)
	for _, r := range rows {
		if r.Kind == KindChapter {
			parts = append(parts, r)
		}
	}
	if len(parts) < 2 {
		t.Fatalf("a long chapter should split, got %d part(s)", len(parts))
	}
	maxTok := DefaultChunkOptions().MaxTokens
	starts := make([]int, 0, len(parts))
	for i, p := range parts {
		if !strings.HasPrefix(p.Text, "Chapter: Long\n") {
			t.Errorf("part %d lost its title: %q", i, p.Text[:min(40, len(p.Text))])
		}
		// The title prefix rides on top of a body sized to the same budget, so
		// allow a little headroom for it rather than asserting a hard ceiling.
		if estimateTokens(p.Text) > maxTok+estimateTokens("Chapter: Long\n")+1 {
			t.Errorf("part %d is %d tokens, over the %d budget", i, estimateTokens(p.Text), maxTok)
		}
		starts = append(starts, p.StartSeconds)
	}
	// Later parts must open where they actually begin, not all at the chapter
	// head — otherwise every hit in a long chapter jumps to the same second.
	if starts[len(starts)-1] <= starts[0] {
		t.Errorf("part timestamps did not advance: %v", starts)
	}
}

func TestNormalizeChaptersSortsAndDrops(t *testing.T) {
	// LLM-synthesized chapters are not guaranteed ordered; an out-of-order entry
	// would otherwise be given a negative span and vanish.
	cs := cues("first bit", "second bit", "third bit")
	rows := BuildVideoChunks(parsedFrom(cs), "", []Chapter{
		{TS: 20, Title: "Third"},
		{TS: 0, Title: "First"},
		{TS: 10, Title: "Second"},
		{TS: 10, Title: "Duplicate timestamp"},
		{TS: 5, Title: "   "},
		{TS: -3, Title: "Negative"},
	})
	titles := make([]string, 0)
	for _, r := range rows {
		if r.Kind != KindChapter {
			continue
		}
		titles = append(titles, strings.SplitN(strings.TrimPrefix(r.Text, "Chapter: "), "\n", 2)[0])
	}
	want := []string{"First", "Second", "Third"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Errorf("chapters = %v, want %v (sorted, no blanks/dupes/negatives)", titles, want)
	}
}

func TestBuildVideoChunksWithoutChaptersMatchesRecipeOne(t *testing.T) {
	// A video with no chapters must still index exactly as before: transcript
	// windows plus one summary chunk, and nothing else.
	cs := cues("some words", "more words")
	rows := BuildVideoChunks(parsedFrom(cs), "the summary", nil)
	got := kindsOf(rows)
	if got[KindChapter] != 0 {
		t.Errorf("chapter chunks = %d, want 0", got[KindChapter])
	}
	if got[KindSummary] != 1 {
		t.Errorf("summary chunks = %d, want 1", got[KindSummary])
	}
	last := rows[len(rows)-1]
	if last.Kind != KindSummary || last.StartSeconds != 0 {
		t.Errorf("summary should be last and untimestamped, got %+v", last)
	}
}

func TestBuildVideoChunksBlankSummaryEmitsNoSummaryChunk(t *testing.T) {
	rows := BuildVideoChunks(parsedFrom(cues("words here")), "   ", nil)
	if kindsOf(rows)[KindSummary] != 0 {
		t.Error("a blank summary must not be indexed")
	}
}

func TestDecodeChaptersTolerantOfJunk(t *testing.T) {
	for _, in := range []string{"", "   ", "[]", "not json", "{}", "null"} {
		if got := DecodeChapters(in); len(got) != 0 {
			t.Errorf("DecodeChapters(%q) = %v, want empty", in, got)
		}
	}
	got := DecodeChapters(`[{"ts":42,"title":"Hydration","source":"llm"}]`)
	if len(got) != 1 || got[0].TS != 42 || got[0].Title != "Hydration" {
		t.Errorf("DecodeChapters lost data: %+v", got)
	}
}
