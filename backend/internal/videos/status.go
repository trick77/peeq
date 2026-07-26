// Package videos: status.go names the two lifecycle enums stored on a video
// row — videos.status and videos.summary_status. Both were bare string
// literals spread across the backend until now, with the CHECK constraints in
// 0001_init.sql as their only authority.
//
// Naming them buys two things. A typo becomes a compile error rather than a
// row that silently fails its CHECK at write time; and the sets become
// greppable from a single place, which is what lets ui/src assert against them
// the way enumsync.test.ts already asserts against the category enum.
//
// These are untyped string constants, not a defined type, deliberately: the
// store methods take and return plain strings, the columns are TEXT, and a
// defined type would force a conversion at every call site for no added
// safety once the values are named.
package videos

// Video status enum values, matching the videos.status CHECK constraint in
// 0001_init.sql exactly.
//
// The lifecycle: a row is born StatusNew (recorded, nothing requested yet),
// becomes StatusQueued when a download job is enqueued, StatusDownloading
// while the worker holds it, and then either StatusDownloaded or StatusError.
// StatusTombstoned is terminal-but-recoverable: the media was reclaimed by the
// retention sweeper or deleted by hand, while the row and its analysis stay so
// the video can be re-downloaded from the Library.
//
// Beware two neighbours that share a word and are NOT this enum: the
// channel-list filter also has a "downloaded" value (channels.Store.List), and
// the per-channel scan ledger also has a "queued" state
// (channelvideos.State*). They are unrelated vocabularies.
const (
	StatusNew         = "new"
	StatusQueued      = "queued"
	StatusDownloading = "downloading"
	StatusDownloaded  = "downloaded"
	StatusTombstoned  = "tombstoned"
	StatusError       = "error"
)

// Statuses is the fixed enum accepted by the videos.status CHECK constraint,
// in lifecycle order.
var Statuses = []string{
	StatusNew,
	StatusQueued,
	StatusDownloading,
	StatusDownloaded,
	StatusTombstoned,
	StatusError,
}

// Summary status enum values, matching the videos.summary_status CHECK
// constraint in 0001_init.sql exactly.
//
// SummaryNoTranscript covers both "this video has no captions" and "the
// captions turned out to be music or ambience rather than speech" — the
// music-only guard in internal/summarize decides the second case. The UI's
// copy has to fit both readings, which is why it says "No speech in this
// video" rather than naming captions.
const (
	SummaryPending      = "pending"
	SummaryRunning      = "running"
	SummaryDone         = "done"
	SummaryError        = "error"
	SummaryNoTranscript = "no_transcript"
)

// SummaryStatuses is the fixed enum accepted by the videos.summary_status
// CHECK constraint.
var SummaryStatuses = []string{
	SummaryPending,
	SummaryRunning,
	SummaryDone,
	SummaryError,
	SummaryNoTranscript,
}
