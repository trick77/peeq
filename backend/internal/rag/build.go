package rag

import (
	"sort"
	"strings"

	"github.com/trick77/peeq/internal/subtitles"
)

// ChunkRecipeRev is the version of WHAT a video's index contains — not of the
// embedding model, which embed_model/embed_dim already record.
//
//	1  transcript windows + one summary chunk
//	2  the above, plus one chunk per chapter
//	3  the above, plus one chunk per in-depth section
//
// Bump this whenever BuildVideoChunks starts or stops emitting a kind of chunk,
// or changes how one is composed. Every video whose videos.embed_rev is below
// the current value is stale. Nothing re-indexes a stale video on its own
// (0019 dropped the sweep); a reprocess does. Forgetting to bump it means new
// videos get the new recipe and old ones silently keep the old one, with nothing to
// distinguish them afterwards.
const ChunkRecipeRev = 3

// SearchableRecipeRev is the oldest recipe whose index still counts as
// searchable (videos.Video.Indexed). It is separate from ChunkRecipeRev because
// a recipe that only ADDS a kind leaves the older index working: rev 3 added
// in-depth sections, and with nothing to re-index the library on its own,
// tying Indexed to the newest rev would mark every video analysed before it
// "not searchable yet". Raise it only when an older index is genuinely unfit.
const SearchableRecipeRev = 2

// Chapter is one entry of the videos.chapters JSON column. It mirrors
// summarize.Chapter's wire shape; rag cannot import summarize (summarize
// imports rag), and duplicating three fields is cheaper than a shared package
// for one struct.
type Chapter struct {
	TS    int    `json:"ts"`
	Title string `json:"title"`
}

// InDepthSection is one "### heading [m:ss]" section of a video's in-depth
// summary. summarize owns that format and parses it; rag only indexes the
// result, for the same import-cycle reason as Chapter.
type InDepthSection struct {
	Heading      string
	StartSeconds int
	Body         string
}

// BuildVideoChunks is the single definition of what gets indexed for a video.
//
// It emits, in order:
//
//	transcript  overlapping ~600-token windows, timestamped from the cue index
//	summary     the whole-video prose summary, one chunk, no timestamp
//	chapter     each chapter's title plus the transcript spanning its range
//	indepth     each in-depth section, heading plus paragraph, at its stamp
//
// Ordinals are assigned by a single counter across all four groups. This is
// load-bearing: search fuses hits keyed by video_id + ordinal, and rag.Chunk
// restarts its own Ordinal at 0 on every call, so letting sub-chunk ordinals
// through would make two unrelated chunks collide and silently merge in the
// ranking.
func BuildVideoChunks(parsed subtitles.Parsed, summaryText string, chapters []Chapter, inDepth []InDepthSection) []ChunkRow {
	rows := make([]ChunkRow, 0, 32)
	next := func() int { return len(rows) }

	cueWordStarts := cueWordStartIndex(parsed.Cues)
	for _, c := range Chunk(parsed.Transcript, DefaultChunkOptions()) {
		rows = append(rows, ChunkRow{
			Ordinal:      next(),
			Text:         c.Text,
			Kind:         KindTranscript,
			TokenCount:   c.TokenCount,
			StartSeconds: cueStartForWordOffset(c.WordOffset, parsed.Cues, cueWordStarts),
		})
	}

	// The summary describes the whole video, so it carries no timestamp; the
	// search UI badges it and opens at 0.
	if s := strings.TrimSpace(summaryText); s != "" {
		rows = append(rows, ChunkRow{
			Ordinal:      next(),
			Text:         s,
			Kind:         KindSummary,
			TokenCount:   estimateTokens(s),
			StartSeconds: 0,
		})
	}

	for _, ch := range normalizeChapters(chapters) {
		for _, r := range chapterRows(ch, parsed.Cues) {
			r.Ordinal = next()
			rows = append(rows, r)
		}
	}

	// A section is the in-depth summary's statement of one point with its
	// reasoning: denser than the transcript window it came from, and anchored
	// where the point is first made, so a hit seeks like a transcript one. The
	// lead paragraph is not passed in: it restates what the summary chunk holds.
	for _, s := range inDepth {
		body := strings.TrimSpace(s.Body)
		if body == "" {
			continue
		}
		prefix := ""
		if h := strings.TrimSpace(s.Heading); h != "" {
			prefix = h + "\n\n"
		}
		// A reply that put one heading over everything is one huge section;
		// split it like a long chapter, heading on every part.
		parts := []string{body}
		if opts := DefaultChunkOptions(); estimateTokens(prefix+body) > opts.MaxTokens {
			// The heading rides on every part, so it comes out of each
			// part's budget rather than on top of it.
			opts.MaxTokens -= estimateTokens(prefix)
			opts.TargetTokens = min(opts.TargetTokens, opts.MaxTokens)
			parts = parts[:0]
			for _, p := range Chunk(body, opts) {
				parts = append(parts, p.Text)
			}
		}
		for _, p := range parts {
			text := prefix + p
			rows = append(rows, ChunkRow{
				Ordinal:      next(),
				Text:         text,
				Kind:         KindInDepth,
				TokenCount:   estimateTokens(text),
				StartSeconds: max(s.StartSeconds, 0),
			})
		}
	}
	return rows
}

// Chunk kinds, as stored in transcript_chunks.kind and echoed to the UI, which
// badges chapter, summary and in-depth hits differently from transcript ones.
const (
	KindTranscript = "transcript"
	KindSummary    = "summary"
	KindChapter    = "chapter"
	KindInDepth    = "indepth"
)

// IsAnalysis reports whether a chunk kind is peeq's own reading of the video —
// the summary or an in-depth section — rather than words from it. The answer
// prompt labels these, and search exempts them from moment dedup. A new
// analysis kind joins here, once.
func IsAnalysis(kind string) bool {
	return kind == KindSummary || kind == KindInDepth
}

// chapterRange pairs a chapter with the exclusive end of its span.
type chapterRange struct {
	Chapter
	end int
}

// normalizeChapters sorts chapters by timestamp and computes each one's end.
//
// Sorting matters because chapters are not guaranteed ordered: yt-dlp's are,
// but the LLM synthesizes them when yt-dlp supplies none, and an out-of-order
// entry would otherwise be given a negative span and silently produce an empty
// chunk. Untitled entries and duplicate timestamps are dropped — a duplicate
// would emit the same text twice under two ordinals, which is pure noise in
// the ranking.
func normalizeChapters(chapters []Chapter) []chapterRange {
	cleaned := make([]Chapter, 0, len(chapters))
	for _, c := range chapters {
		if strings.TrimSpace(c.Title) == "" || c.TS < 0 {
			continue
		}
		cleaned = append(cleaned, c)
	}
	sort.SliceStable(cleaned, func(i, j int) bool { return cleaned[i].TS < cleaned[j].TS })

	// Dedupe BEFORE computing spans, not while computing them. A chapter's end
	// is the next KEPT chapter's start: if a dropped duplicate were still used
	// as the boundary, the entry before it would get a zero-width span, find no
	// cues, and be silently dropped too — losing a real chapter to a bad one.
	deduped := make([]Chapter, 0, len(cleaned))
	for i, c := range cleaned {
		if i > 0 && c.TS == cleaned[i-1].TS {
			continue
		}
		deduped = append(deduped, c)
	}

	out := make([]chapterRange, 0, len(deduped))
	for i, c := range deduped {
		// The last chapter runs to the end of the video, expressed as a
		// sentinel beyond any real cue timestamp.
		end := int(^uint(0) >> 1)
		if i+1 < len(deduped) {
			end = deduped[i+1].TS
		}
		out = append(out, chapterRange{Chapter: c, end: end})
	}
	return out
}

// chapterRows builds the chunk(s) for one chapter: its title prefixed to the
// transcript spanning its time range.
//
// The title alone is never indexed. A chapter heading is ~5 tokens, and a
// vector built from that little text sits near far too many queries — it would
// add noise to every search rather than precision to the ones about that
// topic. A chapter whose span contains no cues is therefore skipped entirely.
//
// A long chapter is split so no chunk exceeds the embedding window, with the
// title repeated on each part (it is the part's only clue to what it covers)
// and each part timestamped to where it actually starts.
func chapterRows(ch chapterRange, cues []subtitles.Cue) []ChunkRow {
	span := make([]subtitles.Cue, 0, 16)
	for _, c := range cues {
		if c.StartSeconds >= ch.TS && c.StartSeconds < ch.end {
			span = append(span, c)
		}
	}
	if len(span) == 0 {
		return nil
	}
	texts := make([]string, 0, len(span))
	for _, c := range span {
		texts = append(texts, c.Text)
	}
	body := strings.TrimSpace(strings.Join(texts, " "))
	if body == "" {
		return nil
	}

	prefix := "Chapter: " + strings.TrimSpace(ch.Title) + "\n"
	if estimateTokens(prefix+body) <= DefaultChunkOptions().MaxTokens {
		return []ChunkRow{{
			Text:         prefix + body,
			Kind:         KindChapter,
			TokenCount:   estimateTokens(prefix + body),
			StartSeconds: ch.TS,
		}}
	}

	spanWordStarts := cueWordStartIndex(span)
	parts := Chunk(body, DefaultChunkOptions())
	out := make([]ChunkRow, 0, len(parts))
	for _, p := range parts {
		text := prefix + p.Text
		out = append(out, ChunkRow{
			Text:       text,
			Kind:       KindChapter,
			TokenCount: estimateTokens(text),
			// Resolved within the chapter's own cue slice, so a later part
			// opens where it really begins rather than at the chapter head.
			StartSeconds: cueStartForWordOffset(p.WordOffset, span, spanWordStarts),
		})
	}
	return out
}

// cueWordStartIndex returns, for each cue, the cumulative word count of all
// preceding cues' text — i.e. the word-offset (into the joined transcript,
// which is strings.Join(cueTexts, " ")) at which that cue's text begins. This
// lets a chunk's WordOffset be mapped back to the cue it actually starts in,
// which is exact and monotonic, unlike prefix-matching the chunk's (possibly
// overlap-shifted) leading text against cue text.
func cueWordStartIndex(cues []subtitles.Cue) []int {
	starts := make([]int, len(cues))
	total := 0
	for i, c := range cues {
		starts[i] = total
		total += len(strings.Fields(c.Text))
	}
	return starts
}

// cueStartForWordOffset returns the StartSeconds of the last cue whose
// word-start is <= wordOffset, i.e. the cue that word belongs to. Falls back
// to 0 when cues is empty.
func cueStartForWordOffset(wordOffset int, cues []subtitles.Cue, cueWordStarts []int) int {
	best := 0
	for i, ws := range cueWordStarts {
		if ws <= wordOffset {
			best = cues[i].StartSeconds
		} else {
			break
		}
	}
	return best
}
