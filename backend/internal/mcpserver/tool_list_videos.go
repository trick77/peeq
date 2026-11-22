package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/trick77/peeq/internal/videos"
	mcp "github.com/trick77/toolkit/mcpserver"
)

const (
	defaultListVideos = 25
	// listChannelRunes trims an item's channel name; listEchoRunes an echoed
	// filter in the header line.
	listChannelRunes = 100
	listEchoRunes    = 100
)

// listVideosDescription says when to list rather than search: search reaches
// only indexed videos and never matches a title or a channel name.
const listVideosDescription = "List the videos in the user's peeq library without a query, paged: every video peeq holds, " +
	"searchable or not, each with its id (for get_video and get_transcript), release date, duration and whether " +
	"search can reach it. Use it instead of search to enumerate a channel's videos (channel), to find a video by its " +
	"title (title), or to see the newest additions (sort=added_newest). It lists metadata only; search reads what the " +
	"videos say."

type listVideosArgs struct {
	Channel string `json:"channel"`
	Title   string `json:"title"`
	After   string `json:"after"`
	Before  string `json:"before"`
	Sort    string `json:"sort"`
	Limit   *int   `json:"limit"`
	Offset  *int   `json:"offset"`
}

// listSortLabels describe each sort in the header line.
var listSortLabels = map[string]string{
	"newest":       "sorted by release date, newest first",
	"oldest":       "sorted by release date, oldest first",
	"added_newest": "sorted by date added, newest first",
	"title":        "sorted by title",
}

func runListVideos(ctx context.Context, h *handler, raw json.RawMessage) mcp.Result {
	var a listVideosArgs
	if err := mcp.DecodeArgs(raw, &a); err != nil {
		return mcp.ErrorResult("invalid arguments: " + err.Error())
	}
	q := VideoListQuery{Title: strings.TrimSpace(a.Title), After: a.After, Before: a.Before,
		Sort: a.Sort, Limit: defaultListVideos}
	if q.Sort == "" {
		q.Sort = videos.BrowseSorts[0]
	}
	if !slices.Contains(videos.BrowseSorts, q.Sort) {
		return mcp.ErrorResult(fmt.Sprintf("sort must be one of %s, got %q", strings.Join(videos.BrowseSorts, ", "), q.Sort))
	}
	if msg := checkDateArgs(a.After, a.Before); msg != "" {
		return mcp.ErrorResult(msg)
	}
	if a.Limit != nil {
		q.Limit = min(max(*a.Limit, 1), videos.BrowseMaxLimit)
	}
	if a.Offset != nil {
		if *a.Offset < 0 {
			return mcp.ErrorResult("offset must be 0 or more")
		}
		q.Offset = *a.Offset
	}
	if name := strings.TrimSpace(a.Channel); name != "" {
		scope, stop := h.resolveChannelArg(ctx, name, "list_videos", "call list_videos without a channel")
		if stop != nil {
			return *stop
		}
		q.Channel = scope
	}

	res, err := h.lib.Videos(ctx, q)
	if err != nil {
		slog.Warn("mcp: list videos failed", "err", err)
		// The deadline interrupting a running statement comes back as the
		// driver's "interrupted"; the call's own context says what it was.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || ctx.Err() != nil {
			return mcp.ErrorResult("listing videos failed (timed out); try again")
		}
		return mcp.ErrorResult("listing videos failed; try again")
	}
	return h.renderVideoList(q, res)
}

// renderVideoList renders a page: the range line, then whole items while they
// fit, then where to continue when anything is left. Items are built first:
// the range and the next offset depend on what survived the size cap.
func (h *handler) renderVideoList(q VideoListQuery, res VideoList) mcp.Result {
	filters := listFilters(q)
	// A total below what the page holds (a library that changed between the
	// count and the page) is raised to it: the range, the continuation and
	// the empty texts below all read this one number.
	if len(res.Videos) > 0 {
		res.Total = max(res.Total, q.Offset+len(res.Videos))
	}
	if res.Total == 0 {
		if len(filters) == 0 {
			return mcp.Result{Text: "peeq holds no videos yet."}
		}
		return mcp.Result{Text: "No videos in peeq match (" + strings.Join(filters, " · ") + "). Try again without some of the filters."}
	}
	if len(res.Videos) == 0 {
		return mcp.Result{Text: fmt.Sprintf("offset %d is past the end: %d videos match. Call list_videos with an offset below %d.",
			q.Offset, res.Total, res.Total)}
	}
	header := func(shown int) string {
		parts := append([]string{fmt.Sprintf("Videos %d–%d of %d", q.Offset+1, q.Offset+shown, res.Total)}, filters...)
		return strings.Join(append(parts, listSortLabels[q.Sort]), " · ") + "\n"
	}
	// The full page's header is the longest this one can be.
	limit := maxToolTextBytes - len(header(len(res.Videos))) - continuationRoom
	var items strings.Builder
	shown := 0
	for _, v := range res.Videos {
		item := h.listItem(v)
		// The first item always goes in, every field of it trimmed: an empty
		// page would advertise its own offset and page in place forever.
		if shown > 0 && items.Len()+len(item) > limit {
			break
		}
		items.WriteString(item)
		shown++
	}
	text := header(shown) + items.String()
	if next := q.Offset + shown; next < res.Total {
		text += fmt.Sprintf("[%d more videos; keep the other arguments and call list_videos again with offset=%d to continue]",
			res.Total-next, next)
	}
	return mcp.Result{Text: strings.TrimRight(text, "\n")}
}

// listFilters echoes the active filters for the header line, each bounded.
func listFilters(q VideoListQuery) []string {
	var out []string
	if len(q.Channel.Matched) > 0 {
		out = append(out, "channel: "+mcp.OneLine(strings.Join(q.Channel.Matched, ", "), listEchoRunes))
	}
	if q.Title != "" {
		out = append(out, fmt.Sprintf("title contains %q", mcp.OneLine(q.Title, listEchoRunes)))
	}
	switch {
	case q.After != "" && q.Before != "":
		out = append(out, "released "+q.After+" to "+q.Before)
	case q.After != "":
		out = append(out, "released "+q.After+" or later")
	case q.Before != "":
		out = append(out, "released "+q.Before+" or earlier")
	}
	return out
}

// listItem is one video: "<title> — <channel>, <date>, <duration>", its id,
// its peeq link and whether search reaches it.
func (h *handler) listItem(v ListedVideo) string {
	title := mcp.OneLine(v.Title, titleRunes)
	if title == "" {
		title = v.ID
	}
	var meta []string
	if c := mcp.OneLine(v.Channel, listChannelRunes); c != "" {
		meta = append(meta, c)
	}
	if v.PublishedAt != "" {
		meta = append(meta, v.PublishedAt)
	}
	if v.DurationSeconds > 0 {
		meta = append(meta, clock(int(v.DurationSeconds)))
	}
	var b strings.Builder
	b.WriteString("- " + title)
	if len(meta) > 0 {
		b.WriteString(" — " + strings.Join(meta, ", "))
	}
	b.WriteString("\n   id: " + v.ID + "\n")
	b.WriteString("   peeq: " + h.videoURL(v.ID, 0) + "\n")
	if v.Searchable {
		b.WriteString("   searchable\n")
	} else {
		b.WriteString("   not searchable: " + notSearchableReason(v) + "\n")
	}
	return b.String()
}

// notSearchableReason says in plain words why search cannot reach a video
// (nothing of it in the search index), never as a raw status. Keyed on the
// transcript first: without one nothing can be analysed or indexed.
//   - No transcript: the caption fetcher gave up (summary no_transcript), a
//     download failed, or none is stored (an inbox read without captions, a
//     queued video: "queued" alone lies, a cancel leaves it).
//   - A transcript: the analysis is pending or running, failed, found no
//     speech (music or ambience, summary no_transcript), or is done and the
//     index is not written yet (summarize writes chunks after marking done).
func notSearchableReason(v ListedVideo) string {
	if !v.HasTranscript {
		switch {
		case v.SummaryStatus == videos.SummaryNoTranscript:
			return "no captions available"
		case v.Status == videos.StatusError:
			return "download failed, no transcript stored"
		}
		return "no transcript stored"
	}
	switch v.SummaryStatus {
	case videos.SummaryError:
		return "analysis failed"
	case videos.SummaryNoTranscript:
		return "no speech in the transcript"
	case videos.SummaryDone:
		return "not indexed yet"
	}
	return "not analysed yet"
}

func listVideosSchema() map[string]any {
	date := func(what string) map[string]any {
		return map[string]any{"type": "string", "pattern": `^\d{4}-\d{2}-\d{2}$`,
			"description": "Only videos released " + what + " this date (YYYY-MM-DD), inclusive."}
	}
	return mcp.ObjectSchema(map[string]any{
		"channel": map[string]any{"type": "string",
			"description": "Only this channel's videos: its name, @handle or id. Matched case-insensitively and tolerant of typos."},
		"title": map[string]any{"type": "string",
			"description": "Only videos whose title contains this text, case-insensitively."},
		"after":  date("on or after"),
		"before": date("on or before"),
		"sort": map[string]any{"type": "string", "enum": videos.BrowseSorts, "default": videos.BrowseSorts[0],
			"description": "newest (default) or oldest: by release date; added_newest: what peeq added last first; title: alphabetical."},
		"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": videos.BrowseMaxLimit, "default": defaultListVideos,
			"description": "Maximum number of videos to return."},
		"offset": map[string]any{"type": "integer", "minimum": 0, "default": 0,
			"description": "How many videos to skip: the offset a previous page said to continue from."},
	})
}
