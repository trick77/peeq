package summarize

import (
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/subtitles"
)

// The short summary and the in-depth summary read the same transcript, back to
// back. Sent as the same leading bytes — this system message, then the title,
// length and cue index — the second call's prefill is the provider's prompt
// cache rather than a second full read. So everything that differs between the
// two goes AFTER the cue index, in the task, and nothing above it may vary
// between them: one changed byte before the cue index and the cache misses.
//
// It names no task and carries none of the phrases the step fakes in the tests
// route on, so a fake answers by what the task asks for.
const videoSystemPrompt = "You work with the transcript of one video, given as a cue index: one caption cue per line, starting with its timestamp in square brackets. Sponsor reads have been removed. Do the task that follows the cue index."

// videoChunks cuts the cue index at the summary's chunk budget. One chunk —
// every video short of several hours — means both calls take the shared
// prefix; more means both take their map paths, which share nothing.
func (s *Summarizer) videoChunks(cues []subtitles.Cue) []rag.TextChunk {
	budget := s.summaryChunkTokens
	return rag.Chunk(formatClockCues(cues), rag.ChunkOptions{
		TargetTokens: budget, MaxTokens: budget + budget/8, OverlapTokens: 500,
	})
}

// videoPrefix is the shared opening of the user message, ending where the
// task begins.
func videoPrefix(title string, durationSeconds int, cueIndex string) string {
	return "TITLE: " + title + "\nLENGTH: " + clock(durationSeconds) +
		"\n\nCUE INDEX:\n" + cueIndex + "\nTASK\n"
}
