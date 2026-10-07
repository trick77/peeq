// Package summarize: phase.go names the summarize progress phases pushed to
// the SPA over SSE. See videos/status.go for why these are named rather than
// left as literals.
//
// Unlike the other named wire enums, this one has no CHECK constraint to be
// the authority — nothing persists a phase. Its only contract is with the
// Player's progress line, mirrored by SUMMARY_PHASES in ui/src/format.ts, so
// this file IS the authority and drift here is invisible until a user watches
// a summary run.
package summarize

// Summarize phase values, emitted alongside a status on the SSE "summary"
// event. The empty phase is also valid and means "no stage in flight" — it is
// what the terminal done/error emits carry.
//
// DO NOT confuse these with pipelineStages in worker.go. That list —
// "summary", "classify", "indepth", "keypoints", "embedding" — is the LOG
// vocabulary used to number stage lines ("stage 2/5 done"), and two of its
// entries are deliberately different words from the phases here. They are
// separate vocabularies that happen to describe the same five steps, and merging them
// would change either the log format or the wire contract.
const (
	PhaseSummarizing = "summarizing"
	PhaseClassifying = "classifying"
	PhaseInDepth     = "indepth"
	PhaseEmbedding   = "embedding"
	PhaseKeypoints   = "keypoints"
)

// Phases is the ordered set of non-empty phases the worker can emit. Nothing
// in Go reads it: it is the list the SPA's enum-sync test (wireenums.test.ts)
// compares its own phase names against, which is that contract's only guard.
var Phases = []string{
	PhaseSummarizing,
	PhaseClassifying,
	PhaseInDepth,
	PhaseKeypoints,
	PhaseEmbedding,
}
