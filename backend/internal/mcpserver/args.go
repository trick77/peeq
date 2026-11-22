package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	mcp "github.com/trick77/toolkit/mcpserver"
)

// resolveChannelArg resolves the channel argument search and list_videos
// share. A non-nil stop is the call's answer instead: the channel list failed
// (tool names the call in the error), no channel matched (without says what
// to try instead), or the channel holds no videos yet (not an error; the
// caller adds what its contract needs, such as search's empty sources).
func (h *handler) resolveChannelArg(ctx context.Context, name, tool, without string) (ChannelScope, *mcp.Result) {
	scope, err := h.lib.ResolveChannel(ctx, name)
	if err != nil {
		slog.Warn("mcp: channel resolve failed", "err", err)
		r := mcp.ErrorResult(tool + " failed: the channel list could not be read")
		return ChannelScope{}, &r
	}
	if !scope.Resolved() {
		msg := fmt.Sprintf("No channel in peeq matches %q.", name)
		if len(scope.Suggestions) > 0 {
			msg += " Closest: " + strings.Join(scope.Suggestions, ", ") + "."
		}
		r := mcp.ErrorResult(msg + " Call list_youtube_channels for every channel, or " + without + ".")
		return ChannelScope{}, &r
	}
	if scope.NoVideos {
		return ChannelScope{}, &mcp.Result{
			Text: fmt.Sprintf("No videos yet in %s: peeq follows the channel but holds none of its videos so far.", strings.Join(scope.Matched, ", ")),
		}
	}
	return scope, nil
}

// checkDateArgs validates the after/before release-date bounds: "" when both
// are absent or YYYY-MM-DD, else the error text.
func checkDateArgs(after, before string) string {
	for _, d := range []struct{ name, val string }{{"after", after}, {"before", before}} {
		if d.val == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", d.val); err != nil {
			return fmt.Sprintf("%s must be a date as YYYY-MM-DD, got %q", d.name, d.val)
		}
	}
	return ""
}
