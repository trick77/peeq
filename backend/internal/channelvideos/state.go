// Package channelvideos: state.go names the per-channel scan ledger's state
// enum, matching the channel_videos.state CHECK constraint in 0001_init.sql
// exactly. See videos/status.go for why these are named rather than left as
// literals.
package channelvideos

// Ledger state enum values.
//
// This is the ledger's own vocabulary and is NOT the video lifecycle: a row
// here records what peeq decided about a video it saw on a channel, while
// videos.status records what happened to the file. StateQueued in particular
// is a different value from videos.StatusQueued despite the shared word — this
// one means "the user approved it from the inbox", and the video row gets its
// own StatusQueued separately.
//
// StateSeen is the baseline a scan writes for anything it is not offering:
// already-known videos, and everything predating the channel's baselined_at.
// StatePending is the inbox. StateIgnored is a dismissal that must survive
// later scans, which is the whole reason the ledger is durable rather than
// derived.
const (
	StateSeen    = "seen"
	StatePending = "pending"
	StateIgnored = "ignored"
	StateQueued  = "queued"
)

// States is the fixed enum accepted by the channel_videos.state CHECK
// constraint.
var States = []string{
	StateSeen,
	StatePending,
	StateIgnored,
	StateQueued,
}
