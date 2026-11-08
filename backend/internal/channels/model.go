package channels

// Channel mirrors one row of the channels table. A Channel may exist purely
// as a metadata cache entry: AddedAt is empty for a channel the user has
// visited but never added.
// Subscribers is 0 when YouTube did not report a count (it is hidden, or the
// channel has never been resolved) — callers must treat 0 as "unknown", not
// as a real zero. ResolveOk records whether the LAST resolve attempt actually
// succeeded; ResolvedAt is stamped either way, so ResolveOk is the only thing
// that distinguishes fresh metadata from a failed attempt that gave up.
//
// Table: channels
type Channel struct {
	ID          string
	Handle      string
	Name        string
	Description string
	// HasAvatar and HasBanner are whether artwork is stored for this channel.
	// Migration 0028 took the avatar_path/banner_path columns that used to
	// stand in for them; the images themselves are rows (0027).
	HasAvatar bool
	HasBanner bool
	// AvatarVersion and BannerVersion are each image's updated_at as a unix
	// stamp, empty when there is no such image. They go in the artwork URLs so
	// those can be cached as immutable — the URL changes exactly when the bytes
	// do, which is also what makes a weekly metadata refresh show up at once
	// instead of whenever the cache window happens to lapse.
	AvatarVersion string
	BannerVersion string
	Subscribers   int64
	Verified      bool
	ResolvedAt    string
	ResolveOk     bool
	AddedAt       string
	FirstSeenAt   string
	// AutoSummary is whether peeq reads this channel's new videos — fetching
	// their captions and writing a summary — before the user decides whether to
	// download them. Default true (migration 0024); the switch exists for the
	// channel subscribed to for one video in ten, where the other nine are
	// summaries nobody asked for.
	AutoSummary bool
	// KeepReads decides what of a video survives its file, for every video of
	// the channel. On (the default since 0042): a delete tombstones, keeping
	// transcript, summary and search index; an inbox read is indexed and kept
	// when ignored — searchable and nothing else, it stays status 'new', which
	// every Library query excludes. Off: a delete or an ignore purges the row,
	// and turning it off purges what it kept so far (videos.Store.PurgeKept).
	KeepReads bool
}

// Subscription mirrors one row of the subscriptions table. BaselinedAt and
// LastScannedAt are empty strings when the underlying column is NULL
// (BaselinedAt is NULL until the subscription's first scan completes).
//
// Table: subscriptions
type Subscription struct {
	ChannelID      string
	Autodownload   bool
	FormatOverride string
	BaselinedAt    string
	LastScannedAt  string
	NextScanAt     string
	CreatedAt      string
	// ScanRequestedAt is set while a user-pressed "Scan now" is still waiting
	// for the scan loop to reach this channel, and "" for an ordinary automatic
	// scan. The scanner reads it to decide whether the pass owes the user a
	// receipt even when it found nothing new — see migration 0011.
	ScanRequestedAt string
	// NextMetaRefreshAt is when the weekly metadata refresh is next due, or ""
	// when this channel has no rotation slot at all — a subscription predating
	// migration 0005, or one never scheduled. The column is nullable and is read
	// as "" here, so callers must treat empty as "not scheduled" rather than as
	// an instant.
	NextMetaRefreshAt string
	// StreamsMissingAt is when a scan was last told this channel has no
	// /streams tab, or "" when the tab answered or was never asked for. The
	// scan skips the tab while this is recent — see migration 0037.
	StreamsMissingAt string
}

// ListItem is a channel joined with its (optional) subscription state, plus
// counts used by the channels list UI.
//
// Table: channels
type ListItem struct {
	Channel
	Subscribed      bool
	Autodownload    bool
	AutoSummary     bool
	FormatOverride  string
	PendingCount    int
	DownloadedCount int
	// LastVideoAt is the discovered_at of the channel's most recently seen
	// video, or "" if none has ever been discovered (added-but-unscanned,
	// or genuinely brand new).
	LastVideoAt string
	// Dormant mirrors DormantChannels' predicate for this one channel: it is
	// subscribed, has seen at least one video, that video is older than
	// DormantAfter relative to now, and dormancy has not been dismissed
	// since. Always false for an added-but-unsubscribed channel.
	Dormant bool
}

// VideoRef identifies one of a channel's downloaded videos and the video file
// that belongs to it. It is read BEFORE a cascade delete so the HTTP handler can
// unlink the file after the rows are gone.
//
// Just the media path: the poster, transcript and everything else a video owns
// are rows that go with it on the cascade (0026, 0027), and whatever a
// pre-migration library still has beside the file is collected by
// media.RemoveVideoFiles taking the whole per-video directory.
//
// Table: videos
type VideoRef struct {
	VideoID   string
	MediaPath string
}

// DueChannel is one upcoming scheduled channel task for the Activity page's
// future projection: a channel id + display name (frozen for display) + the
// instant it is due.
//
// Table: subscriptions
type DueChannel struct {
	ChannelID string
	Name      string
	At        string
}

// Stats are the channel page's header numbers. All but IndexedCount count
// only downloaded videos: a queued, errored, or tombstoned row is not on disk,
// so counting it would overstate what the user actually has.
//
// IndexedCount is the exception, deliberately: it is how many of the channel's
// videos are in the search index, whatever their status. A tombstone takes the
// media file only, and a kept inbox read (keep_reads) was never downloaded, yet
// both stay searchable for good — this is the number that says so.
//
// Table: -
type Stats struct {
	ArchivedCount     int
	IndexedCount      int
	RuntimeSeconds    int64
	DiskBytes         int64
	NewestPublishedAt string
}

// AutoUnsubscribed is a channel joined with the record of why (and when)
// peeq unsubscribed it on its own.
//
// Table: auto_unsubscribes
type AutoUnsubscribed struct {
	Channel
	Reason string
	At     string
}

// Dormant is a subscribed channel that has gone quiet for longer than
// DormantAfter, surfaced for a human to review and decide on.
//
// Table: subscriptions
type Dormant struct {
	ChannelID   string
	Name        string
	LastVideoAt string
}

// Image is one stored channel avatar or banner.
//
// Table: channel_images
type Image struct {
	Mime      string
	Bytes     []byte
	UpdatedAt string
}

// SearchableChannel is a channel whose videos the MCP server offers: the
// narrow projection the channel hint and list_youtube_channels read.
//
// Table: channels
type SearchableChannel struct {
	ID          string
	Handle      string
	Name        string
	Description string
	Subscribed  bool
	// KeepReads is whether videos peeq only read (never downloaded) stay
	// searchable: keep_reads AND auto_summary. keep_reads alone is on for
	// nearly every channel since 0042, and with auto_summary off nothing is
	// read to keep.
	KeepReads bool
}

// SubcategorizeCandidate is a channel the subcategory sweep owes an answer,
// with what the classifier reads.
//
// Table: channels
type SubcategorizeCandidate struct {
	ID          string
	Name        string
	Handle      string
	Description string
	Videos      int
}
