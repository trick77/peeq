// Package summaryjobs: state.go names the summarize-queue state enum, matching
// the summary_jobs.state CHECK constraint in 0001_init.sql exactly. See
// videos/status.go for why these are named rather than left as literals.
package summaryjobs

// Summary job state enum values.
//
// Deliberately one value shorter than the download queue's (jobs.State*): a
// summarize pass has no cancel, so there is no StateCanceled here. The two
// enums are otherwise identical and are still separate on purpose — they are
// governed by different CHECK constraints and only coincidentally agree.
const (
	StatePending = "pending"
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)

// States is the fixed enum accepted by the summary_jobs.state CHECK
// constraint.
var States = []string{
	StatePending,
	StateRunning,
	StateDone,
	StateFailed,
}
