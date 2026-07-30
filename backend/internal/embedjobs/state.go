// Package embedjobs: state.go names the re-embed-queue state enum, matching the
// embed_jobs.state CHECK constraint in 0018_embed_rev.sql exactly. See
// videos/status.go for why these are named rather than left as literals.
package embedjobs

// Re-embed job state enum values.
//
// Identical in shape to summaryjobs.State* and still separate on purpose: they
// are governed by different CHECK constraints and only coincidentally agree, so
// pointing one at the other would couple two tables that are free to diverge.
const (
	StatePending = "pending"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)

// States is the fixed enum accepted by the embed_jobs.state CHECK constraint.
var States = []string{
	StatePending,
	StateRunning,
	StateDone,
	StateFailed,
}
