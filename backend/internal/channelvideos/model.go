package channelvideos

// Entry mirrors one row of the channel_videos table. DecidedAt is an empty
// string when the underlying column is NULL (no decision made yet).
// DurationSeconds is 0 when unknown (NULL in the DB); callers filtering on
// duration should fail open on 0.
//
// Table: channel_videos
type Entry struct {
	VideoID         string
	ChannelID       string
	ChannelName     string
	Title           string
	DurationSeconds int
	URL             string
	ThumbnailURL    string
	State           string
	DiscoveredAt    string
	DecidedAt       string
	// PublishedAt is YYYY-MM-DD, or "" when not known. It is yt-dlp's
	// APPROXIMATE tab date, not the exact upload_date on videos.published_at,
	// and rows written before migration 0009 carry "" until a scan heals them.
	PublishedAt string
	// UnavailableReason is why State is StateUnavailable — one of the
	// ytdlp.TerminalError reasons (members, age, geo, private, deleted). It is
	// "" in every other state; SetState clears it on the way out.
	UnavailableReason string
	// UnavailableAt is when the row was last confirmed unavailable, "" in every
	// other state. It rate-limits the per-video availability probe the scan
	// runs when the channel listing says nothing — see StateUnavailable. It is
	// not a re-offer timer: nothing moves this row without an answer.
	UnavailableAt string
	// SummaryStatus is the videos row's summary_status for this video, and
	// AutoSummary whether its channel is opted in to being read at all. Both
	// are populated ONLY by the ListPending queries; Get leaves them zero,
	// because a ledger row on its own knows nothing about either.
	//
	// The Inbox card needs both, and neither alone is enough. A video that has
	// not been reached yet has no videos row, so its status is "" — which is
	// also what a video on an opted-out channel looks like. The flag is what
	// separates "not read yet" from "never will be", and therefore whether the
	// card shows a progress marker or nothing at all.
	SummaryStatus string
	AutoSummary   bool
	// SummaryGaveUp is whether this video's most recent summary job spent every
	// attempt. Populated by the ListPending queries only, like the two above.
	//
	// SummaryStatus cannot answer this. failJob writes 'error' on EVERY summary
	// failure, retryable or not, so 'error' means "the last attempt failed" and
	// says nothing about whether another is coming — and with the retry ladder
	// at 15m then 4h, a card can wear that status for hours while the queue is
	// still working on it. The job's own state is the only thing that knows the
	// difference.
	//
	// Most recent, not any: the Player's Reprocess inserts a NEW job row rather
	// than reviving the old one, so a video repaired that way keeps its old
	// 'failed' row forever and an EXISTS over all of them would never clear.
	SummaryGaveUp bool
	// HasSubtitles is whether captions are already on disk for this video.
	//
	// It is not implied by SummaryStatus. 'no_transcript' covers two different
	// videos: one YouTube has no captions for at all, and one whose captions
	// turned out to be music and so produced no summary. The second has a
	// readable transcript and the first has nothing, and only this flag tells
	// the Inbox card which of the two it is holding.
	HasSubtitles bool
	// ThumbnailVersion is the cached poster's updated_at as a unix stamp, and ""
	// when nothing has been cached for this video yet. Like SummaryStatus it is
	// populated only by the ListPending queries.
	//
	// The Inbox puts it in the poster URL so a cached poster can be served as
	// immutable. An empty value is not "no poster": the endpoint fetches one on
	// demand, so the card still asks — it just asks on the unversioned URL and
	// gets the ordinary revalidating response.
	ThumbnailVersion string
	// Category is the videos row's category, "" when peeq has no row for the
	// video yet. Populated by the ListPending queries only.
	Category string
}

// CaptionCandidate is a ledger row that is due a caption fetch. It is a
// narrower shape than Entry on purpose: adding the caption columns to Entry
// would change scanRow and every reader of it, for the benefit of exactly one
// caller.
//
// Table: channel_videos
type CaptionCandidate struct {
	VideoID         string
	ChannelID       string
	Title           string
	URL             string
	DurationSeconds int
	PublishedAt     string
	ThumbnailURL    string
	// Attempts is the count BEFORE this fetch. The worker needs it to know
	// whether the attempt it is about to make is the last one, and therefore
	// whether a miss should settle the row as having no transcript.
	Attempts int
}

// Thumbnail is one cached inbox poster: the copy peeq serves so the browser
// never loads an inbox card's image from YouTube's CDN directly.
//
// Table: pending_thumbnails
type Thumbnail struct {
	Mime      string
	Bytes     []byte
	UpdatedAt string
}
