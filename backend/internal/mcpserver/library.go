// Package mcpserver serves peeq's library to MCP clients: one stateless
// Streamable HTTP endpoint (POST /mcp, JSON responses only) speaking both the
// 2026-07-28 protocol and the older initialize-based one.
//
// It reads the library through Library, which httpapi implements over the
// stores and the search lanes the Search page uses, so the two cannot drift.
package mcpserver

import (
	"context"

	"github.com/trick77/peeq/internal/subtitles"
)

// Library is everything the tools read. Implementations scope nothing by user:
// peeq is single-library, and the bearer token is the whole access decision.
type Library interface {
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
	// Transcript returns one video's parsed transcript, or nil when the video
	// or its transcript does not exist.
	Transcript(ctx context.Context, id string) (*Transcript, error)
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
	YouTubeCategories []string
	Summary           string
	KeyPoints         []Moment
	Chapters          []Moment
	InDepth           string
	Tags              []string
}

// Moment is a timestamped line: a chapter title or a key point.
type Moment struct {
	Seconds int
	Text    string
}

// Transcript is one video's parsed caption track.
type Transcript struct {
	VideoID string
	Title   string
	Cues    []subtitles.Cue
}
