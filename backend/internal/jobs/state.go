// Package jobs: state.go names the download-queue state enum, matching the
// download_jobs.state CHECK constraint in 0001_init.sql exactly (the package is
// jobs, the table is download_jobs). See videos/status.go for why these are
// named rather than left as literals, and why they are untyped string
// constants.
package jobs

// Download job state enum values.
//
// The queue is single-lane by design (one worker, one download at a time, so
// YouTube does not block us), so at most one row is StateRunning. StateCanceled
// is what the user's cancel produces and is the only terminal state that is not
// the worker's doing — which is why Cancel reports whether it actually changed
// anything rather than assuming it did.
//
// Note this enum has a StateCanceled that the summary-job enum
// (summaryjobs.State*) does not: a download can be abandoned mid-flight, a
// summarize pass cannot.
const (
	StatePending  = "pending"
	StateRunning  = "running"
	StateDone     = "done"
	StateFailed   = "failed"
	StateCanceled = "canceled"
)

// States is the fixed enum accepted by the download_jobs.state CHECK
// constraint.
var States = []string{
	StatePending,
	StateRunning,
	StateDone,
	StateFailed,
	StateCanceled,
}
