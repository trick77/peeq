package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	mcp "github.com/trick77/toolkit/mcpserver"
)

// maxSearchChannels bounds search's channels argument.
const maxSearchChannels = 4

// checkScopeArgs validates search's video_id and channels and copies
// video_id, watched and favorite into q. It reads nothing, so a bad argument
// costs no library call. It returns the channel names to resolve: channel
// first, then the channels entries. Blanks and an empty list are absent, as
// LLM clients send them for an optional argument.
func (h *handler) checkScopeArgs(a searchArgs, q *SearchQuery) ([]string, *mcp.Result) {
	if raw := strings.TrimSpace(a.VideoID); raw != "" {
		arg := h.parseVideoArg(raw)
		if arg.id == "" {
			// Never the argument: it can be a URL with a query.
			slog.Info("mcp: search video miss", "video_id", logVideoID(arg.id), "reason", "unknown_video")
			r := mcp.ErrorResult(fmt.Sprintf("video_id %q is not a video id or video link. Use the id from a search result.", arg.raw))
			return nil, &r
		}
		q.VideoID = arg.id
	}
	q.Watched, q.Favorite = a.Watched, a.Favorite

	var names []string
	for _, c := range a.Channels {
		if c = strings.TrimSpace(c); c != "" {
			names = append(names, c)
		}
	}
	if len(names) > maxSearchChannels {
		r := mcp.ErrorResult(fmt.Sprintf("channels takes at most %d channels, got %d", maxSearchChannels, len(names)))
		return nil, &r
	}
	if name := strings.TrimSpace(a.Channel); name != "" {
		names = append([]string{name}, names...)
	}
	return names, nil
}

// checkVideo is the answer when search cannot run in video id: no such
// video, nothing of it indexed, or the check failing; nil when it can, or
// when no video_id was given. One cheap read, before any channel is resolved:
// a channel's no-videos answer must not hide an unknown video.
func (h *handler) checkVideo(ctx context.Context, id string) *mcp.Result {
	if id == "" {
		return nil
	}
	err := h.lib.SearchableVideo(ctx, id)
	var r mcp.Result
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrVideoNotFound):
		slog.Info("mcp: search video miss", "video_id", logVideoID(id), "reason", "unknown_video")
		r = mcp.ErrorResult(unknownVideoText(id))
	case errors.Is(err, ErrVideoNotSearchable):
		slog.Info("mcp: search video miss", "video_id", logVideoID(id), "reason", "not_indexed")
		r = mcp.ErrorResult(fmt.Sprintf("Video %q is in peeq but not searchable: nothing of it is indexed yet. "+
			"Call get_video for its details or get_transcript for its transcript.", id))
	default:
		r = searchFailed(err)
	}
	return &r
}

// searchFailed is the answer for a search that could not run, its cause only
// in the log.
func searchFailed(err error) mcp.Result {
	slog.Warn("mcp: search failed", "err", err)
	reason := "the search index could not be read"
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		reason = "timed out"
	}
	return mcp.ErrorResult("search failed (" + reason + "), try again, or narrow the query")
}

// filtered reports whether anything beyond the query text and video_id
// narrows the search: what "try again without some of the filters" can drop.
func (q SearchQuery) filtered() bool {
	return len(q.Channel.Matched) > 0 || q.Category != "" || q.YouTubeCategory != "" || q.After != "" ||
		q.Before != "" || q.Subcategory != "" || q.ChannelSubcategory != "" ||
		q.Watched != nil || q.Favorite != nil
}
