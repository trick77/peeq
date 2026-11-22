package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
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
	return h.resolveChannelArgs(ctx, []string{name}, tool, without)
}

// resolveChannelArgs is resolveChannelArg over several names, unioned into
// one scope. Any name that matches nothing stops the call with its own miss:
// a search over fewer channels than asked would read as complete. A channel
// without videos among others narrows nothing away; only when every one is
// empty is that the answer.
func (h *handler) resolveChannelArgs(ctx context.Context, names []string, tool, without string) (ChannelScope, *mcp.Result) {
	var union ChannelScope
	empty := true
	seen := map[string]bool{}
	for _, name := range names {
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
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
		union.IDs = appendNew(union.IDs, scope.IDs)
		union.Names = appendNew(union.Names, scope.Names)
		union.Matched = appendNew(union.Matched, scope.Matched)
		empty = empty && scope.NoVideos
	}
	if empty {
		return ChannelScope{}, &mcp.Result{
			Text: fmt.Sprintf("No videos yet in %s: peeq follows the channel but holds none of its videos so far.", strings.Join(union.Matched, ", ")),
		}
	}
	return union, nil
}

// appendNew appends the entries of add that dst does not hold yet.
func appendNew(dst, add []string) []string {
	for _, s := range add {
		if !slices.Contains(dst, s) {
			dst = append(dst, s)
		}
	}
	return dst
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
