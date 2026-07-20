// Package videos: availability.go defines the fixed video-availability enum
// (the authority; matches the CHECK constraint on videos.availability in
// 0001_init.sql) plus normalization from yt-dlp's own vocabulary onto it.
// yt-dlp reports availability as one of "public", "unlisted", "private",
// "premium_only", "subscriber_only", "needs_auth" (or omits the field
// entirely); none of those strings except "private" are valid here, so raw
// yt-dlp output must never be written to the column directly.
package videos

import "strings"

// Availability enum values, matching the videos.availability CHECK
// constraint in 0001_init.sql exactly.
const (
	AvailabilityAvailable = "available"
	AvailabilityDeleted   = "deleted"
	AvailabilityPrivate   = "private"
	AvailabilityGeo       = "geo"
	AvailabilityUnknown   = "unknown"
)

// Availabilities is the fixed enum accepted by the DB CHECK constraint.
// AvailabilityDeleted and AvailabilityGeo are never produced by
// NormalizeAvailability: yt-dlp's availability field has no equivalent. They
// are reserved for future download-error classification (the shape
// ytdlp.TerminalError would feed) — no code writes them today — but they
// remain part of the enum this package owns and the CHECK accepts.
var Availabilities = []string{
	AvailabilityAvailable,
	AvailabilityDeleted,
	AvailabilityPrivate,
	AvailabilityGeo,
	AvailabilityUnknown,
}

// ValidAvailability reports whether id is an exact enum value.
func ValidAvailability(id string) bool {
	for _, a := range Availabilities {
		if a == id {
			return true
		}
	}
	return false
}

// NormalizeAvailability maps yt-dlp's raw "availability" metadata field onto
// peeq's fixed enum. yt-dlp's "public" and "unlisted" both mean the video is
// downloadable, so both map to AvailabilityAvailable. "private" maps
// directly. Gated states peeq has no dedicated column value for
// ("premium_only", "subscriber_only", "needs_auth") fall back to
// AvailabilityUnknown, as does an absent field and anything unrecognized.
func NormalizeAvailability(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "public", "unlisted":
		return AvailabilityAvailable
	case "private":
		return AvailabilityPrivate
	default:
		return AvailabilityUnknown
	}
}
