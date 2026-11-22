// Package mcpserver serves peeq's library to MCP clients: one stateless
// Streamable HTTP endpoint (POST /mcp, JSON responses only) speaking both the
// 2026-07-28 protocol and the older initialize-based one.
//
// It reads the library through Library, which httpapi implements over the
// stores and the search lanes the Search page uses, so the two cannot drift,
// and adds videos through the same enqueue the web UI's Add view uses.
package mcpserver

import (
	"context"
	"errors"

	"github.com/trick77/peeq/internal/subtitles"
)

// Library is everything the tools read, plus the one write (AddVideos).
// Implementations scope nothing by user: peeq is single-library, and the
// bearer token is the whole access decision.
type Library interface {
	// AddVideos queues each URL for download, as the Add view does, and
	// answers one outcome per URL in the same order. A video with a pending
	// or running download job, or already downloaded, is left alone: a client
	// retrying a call must not queue it twice. It never fails as a whole.
	AddVideos(ctx context.Context, urls []string) []AddedVideo
	// Channels lists the subscribed channels plus any channel whose reads are
	// kept, in display order.
	Channels(ctx context.Context) ([]Channel, error)
	// ResolveChannel maps a name, @handle or id onto the library's channels.
	// A miss is not an error: Scope.Resolved() is false and Suggestions holds
	// the closest names.
	ResolveChannel(ctx context.Context, name string) (ChannelScope, error)
	// Search runs retrieval and groups the hits per video, best first.
	Search(ctx context.Context, q SearchQuery) (SearchResult, error)
	// Video returns one video with its analysis, or nil when there is none.
	Video(ctx context.Context, id string) (*Video, error)
	// Videos lists one page of every video peeq holds, whatever its status
	// and whether search can reach it, with the total for the same filter.
	Videos(ctx context.Context, q VideoListQuery) (VideoList, error)
	// Transcript returns one video's parsed transcript. A video without a
	// usable transcript (none stored, or one that does not parse) comes back
	// with its Title, Status and HasSummary and no Cues; ErrVideoNotFound
	// when there is no such video.
	Transcript(ctx context.Context, id string) (*Transcript, error)
}

// ErrVideoNotFound is Transcript's answer for an id peeq has no video for.
var ErrVideoNotFound = errors.New("mcp: video not found")

// AddOutcome is what AddVideos did with one URL.
type AddOutcome string

const (
	// AddQueued means a download job was queued.
	AddQueued AddOutcome = "queued"
	// AddAlready means the video had a pending or running job, or was downloaded.
	AddAlready AddOutcome = "already"
	// AddRejected means not added; Message says why.
	AddRejected AddOutcome = "rejected"
)

// AddedVideo is one URL's outcome.
type AddedVideo struct {
	URL     string
	VideoID string
	Outcome AddOutcome
	// Message is the client-safe reason a rejected URL was refused.
	Message string
}

// Channel is one row of list_youtube_channels and of the auto-search hint.
type Channel struct {
	ID          string
	Name        string
	Handle      string
	Description string
	Subscribed  bool
	KeepReads   bool
	Videos      int
	// TopCategories is the channel's peeq category histogram, largest first;
	// TopYouTubeCategories the same over YouTube's own labels.
	TopCategories        []CategoryCount
	TopYouTubeCategories []CategoryCount
	// Subcategories are the channel's labels ("sports/cycling"), primary
	// first: what search's channel_subcategory filter matches.
	Subcategories []string
}

// CategoryCount is one bucket of a channel's category histogram.
type CategoryCount struct {
	Category string
	Count    int
}

// ChannelScope is a resolved channel argument, ready to narrow a search.
type ChannelScope struct {
	IDs   []string
	Names []string
	// Matched is the display name of everything that resolved.
	Matched []string
	// Suggestions is filled on a miss: the closest channel names.
	Suggestions []string
	// NoVideos is set when everything that resolved is a channel peeq lists
	// but holds no videos of yet (just subscribed, nothing scanned).
	NoVideos bool
}

// Resolved reports whether the scope narrows anything.
func (c ChannelScope) Resolved() bool { return len(c.IDs) > 0 || len(c.Names) > 0 }

// SearchQuery is a validated search tool call.
type SearchQuery struct {
	Text    string
	Channel ChannelScope
	// Category is a valid videos category id or "".
	Category string
	// YouTubeCategory is one of YouTube's own labels or "".
	YouTubeCategory string
	// Subcategory and ChannelSubcategory are valid subcategory keys or "":
	// the video's own label, and its channel's.
	Subcategory        string
	ChannelSubcategory string
	// After and Before are inclusive 'YYYY-MM-DD' release-date bounds or "".
	After, Before string
	// Keyword skips the embedding: a literal FTS5 match only.
	Keyword bool
	// Videos caps how many videos come back.
	Videos int
}

// SearchResult is the grouped outcome of one search.
type SearchResult struct {
	Videos []VideoHits
	// SemanticDegraded is set when a hybrid search ran keyword-only because the
	// query could not be embedded or the vector lane failed.
	SemanticDegraded bool
	// KeywordFailed is the keyword lane failing before it found anything, so
	// the results are semantic matches only. KeywordPartial is a later
	// keyword rung failing after earlier ones found matches.
	KeywordFailed  bool
	KeywordPartial bool
}

// VideoHits is one video and the moments in it that matched, best first.
type VideoHits struct {
	Video   Video
	Matches []Match
}

// Match is one matched moment. Snippet may carry rag.HighlightStart/End
// around matched terms.
type Match struct {
	StartSeconds int
	Kind         string
	Snippet      string
}

// Video is a video's metadata and peeq's analysis of it. Search fills the
// metadata and Summary only; Video fills everything.
type Video struct {
	ID                string
	Title             string
	Channel           string
	PublishedAt       string
	DurationSeconds   int64
	Category          string
	Subcategories     []string
	YouTubeCategories []string
	Summary           string
	KeyPoints         []Moment
	Chapters          []Moment
	InDepth           string
	Tags              []string
}

// VideoListQuery is a validated list_videos call.
type VideoListQuery struct {
	Channel ChannelScope
	// Title is a case-insensitive title substring or "".
	Title string
	// After and Before are inclusive 'YYYY-MM-DD' release-date bounds or "".
	After, Before string
	// Sort is one of videos.BrowseSorts.
	Sort          string
	Limit, Offset int
}

// VideoList is one page of list_videos and the total for its filter.
type VideoList struct {
	Videos []ListedVideo
	Total  int
}

// ListedVideo is one list_videos item.
type ListedVideo struct {
	ID              string
	Title           string
	Channel         string
	PublishedAt     string
	DurationSeconds int64
	// Status (videos.status) and SummaryStatus pick the not-searchable
	// reason; never shown raw: "queued" or "new" alone mean nothing to a
	// client.
	Status        string
	SummaryStatus string
	HasTranscript bool
	// Searchable is whether the search index holds anything of the video.
	Searchable bool
}

// Moment is a timestamped line: a chapter title or a key point.
type Moment struct {
	Seconds int
	Text    string
}

// Transcript is one video's parsed caption track. Cues is empty when the
// video has none stored.
type Transcript struct {
	VideoID string
	Title   string
	// Status is the video's download status (videos.status), for logs only:
	// "queued" or "new" alone mean nothing to a client.
	Status string
	// HasSummary is whether get_video has a summary to offer instead.
	HasSummary bool
	Cues       []subtitles.Cue
}
