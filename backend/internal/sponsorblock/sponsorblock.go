// Package sponsorblock reads crowdsourced segment data (sponsor reads,
// self-promotion, intros, …) from the public SponsorBlock API at
// sponsor.ajay.app, so peeq's player can skip or mark them.
//
// The API is NOT YouTube. Nothing here goes through the cookie gate, the
// shared yt-dlp pacer, or the global YouTube kill-switch: those exist to
// protect a Google account, and gating a public segment API behind an expired
// cookie would park the whole backfill for no reason.
package sponsorblock

import "sort"

// Categories is the canonical set of SponsorBlock categories peeq stores, and
// the single source of truth for BOTH ingestion paths: it is sent as the
// API's `categories` parameter here, and used by the yt-dlp wrapper to filter
// what --sponsorblock-mark returns. Without one shared list the same video
// would show different bands depending on whether it was downloaded or
// backfilled.
//
// Deliberately excluded from yt-dlp's full set:
//
//   - "chapter" — crowdsourced navigation chapters. Not a segment at all;
//     drawing one as a band on the seek bar would mislabel a whole part of the
//     video as skippable.
//   - "poi_highlight" — a POINT ("jump to the good part"), which yt-dlp pads
//     to one second so it can be marked. The opposite of something to skip,
//     and a one-second band communicates nothing.
//   - "hook" — sparsely submitted, and overlaps "intro" in practice.
var Categories = []string{
	"sponsor",
	"selfpromo",
	"interaction",
	"intro",
	"outro",
	"preview",
	"filler",
	"music_offtopic",
}

// Wanted reports whether category is one peeq stores. Both ingestion paths
// filter through this, so a category added to Categories reaches the player
// from a fresh download and a backfill alike.
func Wanted(category string) bool {
	for _, c := range Categories {
		if c == category {
			return true
		}
	}
	return false
}

// Segment is one stored SponsorBlock segment, in the shape peeq persists in
// videos.sponsorblock_segments and serves to the player.
type Segment struct {
	Category  string  `json:"category"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
}

// Normalize prepares raw segments for storage, and is shared by BOTH ingestion
// paths — the API client and the yt-dlp info.json parser — for the same reason
// Categories is: a video must show the same bands, at the same boundaries,
// whether its segments arrived with a download or were filled in afterwards.
//
// duration is peeq's own duration for the video, or 0 when unknown (which only
// disables the end-of-video snap; nothing is rejected for it).
func Normalize(segs []Segment, duration float64) []Segment {
	out := make([]Segment, 0, len(segs))
	for _, s := range segs {
		start, end := s.StartTime, s.EndTime
		// A [0,0] segment labels the ENTIRE video ("all of this is
		// self-promotion"). Skipping it would skip the video, so it is not
		// something peeq can act on.
		if start == 0 && end == 0 {
			continue
		}
		if end <= start {
			continue
		}
		if !Wanted(s.Category) {
			continue
		}
		// Snap sub-second gaps at both ends, so a segment starting at 0.4s
		// doesn't leave 400ms of an ad playing, and one ending 800ms early
		// doesn't leave a stub behind.
		if start <= 1 {
			start = 0
		}
		if duration > 0 && duration-end <= 1 {
			end = duration
		}
		out = append(out, Segment{Category: s.Category, StartTime: start, EndTime: end})
	}
	// Sorted so the player can rely on the order: segments arrive in
	// submission order from the API, not in playback order.
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime < out[j].StartTime })
	return out
}
