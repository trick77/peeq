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
//
// StateUnavailable (added by 0014) is the odd one out: alone among these it is
// neither terminal nor a user decision. It marks a video peeq knows about but
// cannot fetch — members-only, age-gated, geo-blocked, private, deleted — and
// a scan pass revisits it, returning it to StatePending once the gate lifts.
// That non-terminality is the whole point: Exists matches on video_id with no
// state predicate, so any state a scan does not revisit buries the video for
// good, which is the wrong answer for a wall that can come down. See
// Store.SetUnavailable and Entry.UnavailableAt for how the gate is re-checked
// when the channel listing cannot be trusted to report it.
const (
	StateSeen        = "seen"
	StatePending     = "pending"
	StateIgnored     = "ignored"
	StateQueued      = "queued"
	StateUnavailable = "unavailable"
)

// States is the fixed enum accepted by the channel_videos.state CHECK
// constraint.
var States = []string{
	StateSeen,
	StatePending,
	StateIgnored,
	StateQueued,
	StateUnavailable,
}
