package mcpserver

import (
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
// first, then the channels entries.
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
	if name := strings.TrimSpace(a.Channel); name != "" {
		names = append(names, name)
	}
	// Absent decodes to nil, [] to an empty list: only the latter is wrong.
	if a.Channels != nil && (len(a.Channels) == 0 || len(a.Channels) > maxSearchChannels) {
		r := mcp.ErrorResult(fmt.Sprintf("channels must list 1 to %d channels, got %d", maxSearchChannels, len(a.Channels)))
		return nil, &r
	}
	for i, c := range a.Channels {
		c = strings.TrimSpace(c)
		if c == "" {
			r := mcp.ErrorResult(fmt.Sprintf("channels entry %d is blank", i+1))
			return nil, &r
		}
		names = append(names, c)
	}
	return names, nil
}

// searchVideoMiss is the answer for a video_id search cannot run in: no such
// video, or one with nothing indexed. ok is false for any other error.
func searchVideoMiss(id string, err error) (res mcp.Result, ok bool) {
	switch {
	case errors.Is(err, ErrVideoNotFound):
		slog.Info("mcp: search video miss", "video_id", logVideoID(id), "reason", "unknown_video")
		return mcp.ErrorResult(unknownVideoText(id)), true
	case errors.Is(err, ErrVideoNotSearchable):
		slog.Info("mcp: search video miss", "video_id", logVideoID(id), "reason", "not_indexed")
		return mcp.ErrorResult(fmt.Sprintf("Video %q is in peeq but not searchable: nothing of it is indexed yet. "+
			"Call get_video for its details or get_transcript for its transcript.", id)), true
	}
	return mcp.Result{}, false
}

// filtered reports whether anything beyond the query text narrows the search.
func (q SearchQuery) filtered() bool {
	return len(q.Channel.Matched) > 0 || q.Category != "" || q.YouTubeCategory != "" || q.After != "" ||
		q.Before != "" || q.Subcategory != "" || q.ChannelSubcategory != "" || q.VideoID != "" ||
		q.Watched != nil || q.Favorite != nil
}
