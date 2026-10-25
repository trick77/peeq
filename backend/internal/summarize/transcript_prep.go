package summarize

import (
	"fmt"
	"strings"

	"github.com/trick77/peeq/internal/subtitles"
	"github.com/trick77/peeq/internal/videos"
)

// preparedTranscript is a stored transcript made ready for the summarizer. A
// job and the in-depth retry both build theirs here, so a retried text reads
// exactly what the job's would have.
type preparedTranscript struct {
	// parsed is the whole transcript: embedding keeps sponsor reads.
	parsed subtitles.Parsed
	// forSummary has the suppressed segments withheld; every prompt reads it.
	forSummary subtitles.Parsed
	// spans are the suppressed segments, nil when they proved unusable.
	spans []span
	// empty says why there is nothing to summarize; "" when there is.
	empty string
}

// prepareTranscript parses the stored .vtt and withholds the sponsor reads. It
// only reports: what to do about an empty transcript is the caller's.
func (w *Worker) prepareTranscript(video *videos.Video, vtt string) (preparedTranscript, error) {
	parsed, err := subtitles.ParseVTT(strings.NewReader(vtt))
	if err != nil {
		return preparedTranscript{}, fmt.Errorf("parse vtt: %w", err)
	}
	if parsed.Transcript == "" {
		return preparedTranscript{empty: "empty transcript"}, nil
	}
	// Captions that are just music/ambience with the odd lyric fragment are not
	// speech. Summarizing them produces a confident description of a video that
	// says nothing, so treat them like a missing transcript. A job checks this
	// before startRun, so a music video never opens an analysis log line either.
	if parsed.IsNonSpeech(int(video.DurationSeconds)) {
		return preparedTranscript{empty: "no speech (music only)"}, nil
	}

	// Sponsor reads and the other off-topic segments are taken out of what the
	// summarizer reads, so no chapter, key point or summary sentence can be drawn
	// from one. See sponsor.go for why this is done to the INPUT rather than only
	// to the results, and why "intro" is not among them.
	//
	// Deliberately applied to a copy: `parsed` stays whole for embedAndStore, so
	// this narrows what the Player captions without narrowing what search finds.
	spans := suppressedSpans(video.SponsorblockSegments)
	forSummary, spansUnusable := stripCues(parsed, spans)
	if spansUnusable {
		// The spans covered every cue. The transcript falls back to the
		// unfiltered parse (see stripCues), and the spans have to be dropped
		// here too: leaving them in place would let the same bad data strip
		// every chapter and key point in the job's backstop, which is the
		// failure the fallback exists to prevent.
		w.d.Logger.Warn("summarize worker: sponsor segments cover the whole transcript, filter skipped",
			"video_id", video.ID, "spans", len(spans))
		spans = nil
	}
	if n := len(parsed.Cues) - len(forSummary.Cues); n > 0 {
		w.d.Logger.Debug("summarize worker: sponsor segments withheld from summarizer",
			"video_id", video.ID, "cues_dropped", n, "spans", len(spans))
	}
	return preparedTranscript{parsed: parsed, forSummary: forSummary, spans: spans}, nil
}
