package summarize

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/trick77/peeq/internal/sponsorblock"
	"github.com/trick77/peeq/internal/subtitles"
)

// Keeping sponsor reads out of the artifacts a reader sees.
//
// A sponsor read is the worst thing in a transcript to hand a summarizer. It is
// fluent, self-contained, emphatic and full of product nouns, so it looks
// exactly like the "notable moment" a key-points prompt asks for — which is how
// a chapter called "The NordVPN offer" ends up in the Player.
//
// Two defences, because they fail differently:
//
//   - The cues inside a suppressed segment are removed from what the model is
//     GIVEN (stripCues). This is the one that matters: content the model never
//     reads cannot be summarized, quoted, or titled, and it cleans the prose
//     summary too, which no output filter could reach.
//   - Whatever comes back is checked against the same spans anyway (DropCovered).
//     The model is handed a cue index with a hole in it, and a timestamp it
//     infers rather than reads can still land in that hole.
//
// The embedding index is deliberately NOT filtered. It is built from the same
// subtitles.Parsed (see worker.embedAndStore), but semantic search answering
// with a sponsor read is a different question from the Player captioning one,
// and narrowing what is searchable is not this change's business.

// span is one suppressed region, in whole seconds, half-open: [start, end).
// Cue and chapter timestamps are both integer seconds, so the fractional bounds
// SponsorBlock reports are widened outward here rather than compared as floats:
// a cue at second 12 that begins inside a segment ending at 12.4 is part of that
// segment, so the end is ceiled to 13 and second 12 falls inside [start, 13).
type span struct{ start, end int }

// suppressedSpans decodes the stored sponsorblock_segments JSON and returns the
// regions whose content must not reach the summarizer, merged and sorted.
//
// Malformed or absent JSON yields no spans, never an error: this is a quality
// filter on a best-effort crowd-sourced feed, and a video whose segments cannot
// be read must still get its summary.
func suppressedSpans(segmentsJSON string) []span {
	if strings.TrimSpace(segmentsJSON) == "" {
		return nil
	}
	var segs []sponsorblock.Segment
	if err := json.Unmarshal([]byte(segmentsJSON), &segs); err != nil {
		return nil
	}
	out := make([]span, 0, len(segs))
	for _, s := range segs {
		if !sponsorblock.NotContent(s.Category) {
			continue
		}
		// Floor the start and ceil the end so a segment never sheds a second at
		// either edge into the text the model reads.
		st := int(s.StartTime)
		en := int(s.EndTime)
		if float64(en) < s.EndTime {
			en++
		}
		if en <= st {
			continue
		}
		out = append(out, span{start: st, end: en})
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start < out[j].start })
	// Merge overlaps so covers() stays a simple scan and a video with three
	// stacked submissions on the same read does not pay for all three.
	merged := out[:1]
	for _, s := range out[1:] {
		last := &merged[len(merged)-1]
		if s.start <= last.end {
			if s.end > last.end {
				last.end = s.end
			}
			continue
		}
		merged = append(merged, s)
	}
	return merged
}

// covers reports whether ts (whole seconds) falls inside any span. The end is
// EXCLUSIVE, because it has already been ceiled outward in suppressedSpans: a
// SponsorBlock end time is where content resumes, so for a segment ending at an
// exact 90.0 second 90 is the first content second, and for one ending at 90.4
// the ceil to 91 is what keeps second 90 suppressed. Testing ts <= end on top of
// that widening would claim one further second in both cases — dropping the
// chapter a creator placed exactly at the end of the ad read.
func covers(spans []span, ts int) bool {
	for _, s := range spans {
		if ts >= s.start && ts < s.end {
			return true
		}
	}
	return false
}

// stripCues removes the cues inside a suppressed span and rebuilds the joined
// transcript from what survives, in the same shape subtitles.ParseVTT produces
// (its Transcript is exactly the cue texts joined by a space).
//
// Surviving cues keep their ORIGINAL timestamps. The gap that leaves is the
// point: nothing is re-timed to paper over the removed passage, so a chapter the
// model does propose still lines up with the video a reader is watching.
//
// SoundEventCues is left as-is. It is a ratio used to detect a music-only track,
// and it is computed before this runs — recomputing it against a filtered cue
// list would change which videos are rejected as non-speech, which is not this
// filter's business.
//
// The second return value reports that the filter was abandoned — the caller
// must then stop trusting the spans altogether, output backstop included, or
// the same bad data that would have emptied the transcript instead empties the
// chapter and key-point lists.
func stripCues(p subtitles.Parsed, spans []span) (subtitles.Parsed, bool) {
	if len(spans) == 0 {
		return p, false
	}
	kept := make([]subtitles.Cue, 0, len(p.Cues))
	texts := make([]string, 0, len(p.Cues))
	for _, c := range p.Cues {
		if covers(spans, c.StartSeconds) {
			continue
		}
		kept = append(kept, c)
		texts = append(texts, c.Text)
	}
	// A video that is nothing but suppressed segments would otherwise summarize
	// an empty transcript, which the worker treats as "no transcript" and closes
	// out. Bad segment data must not be able to do that, so an empty result falls
	// back to the unfiltered parse.
	if len(kept) == 0 {
		return p, true
	}
	p.Cues = kept
	p.Transcript = strings.Join(texts, " ")
	return p, false
}

// DropCovered removes chapters and key points whose timestamp falls in a
// suppressed span. It runs on the model's OUTPUT, after stripCues has already
// removed the source text, and catches the timestamp a model inferred rather
// than read.
//
// yt-dlp's own chapters are passed through this too. They are the creator's
// labels rather than the model's, but a creator who titles a chapter "Sponsor"
// is naming the same thing, and the reader's complaint is about what the Player
// shows, not about which component wrote it.
func dropCovered(spans []span, chapters []Chapter, keyPoints []KeyPoint) ([]Chapter, []KeyPoint) {
	if len(spans) == 0 {
		return chapters, keyPoints
	}
	ch := make([]Chapter, 0, len(chapters))
	for _, c := range chapters {
		if covers(spans, c.TS) {
			continue
		}
		ch = append(ch, c)
	}
	kp := make([]KeyPoint, 0, len(keyPoints))
	for _, k := range keyPoints {
		if covers(spans, k.TS) {
			continue
		}
		kp = append(kp, k)
	}
	return ch, kp
}
