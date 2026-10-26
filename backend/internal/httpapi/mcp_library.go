package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/trick77/peeq/internal/mcpserver"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/subtitles"
	"github.com/trick77/peeq/internal/videos"
)

// mcpLibrary is the MCP server's view of the library. It runs the same lanes,
// fusion, grouping (groupHits) and channel resolution (resolveChannelsIn) as
// the Search page, minus the query-understanding call: an MCP client's model
// passes filters as arguments, so there is nothing to extract.
//
// Every store read takes the tool call's context, so the tool deadline bounds
// it. The search lanes' sqlite reads do too (askLanes threads ctx).
type mcpLibrary struct{ s *server }

// mcpSuggestions is how many close channel names a miss offers.
const mcpSuggestions = 5

var errMCPNotConfigured = errors.New("mcp: library store not configured")

func (l mcpLibrary) Channels(ctx context.Context) ([]mcpserver.Channel, error) {
	if l.s.channels == nil || l.s.videos == nil {
		return nil, errMCPNotConfigured
	}
	chans, err := l.s.channels.Searchable(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := l.s.videos.ChannelCategoryStats(ctx)
	if err != nil {
		return nil, err
	}
	// Labels are an extra on the list, not the list: a failed read drops them.
	subs, err := l.s.channels.AllSubcategories(ctx)
	if err != nil {
		slog.WarnContext(ctx, "mcp: channel subcategories unavailable", "err", err)
	}
	out := make([]mcpserver.Channel, 0, len(chans))
	for _, c := range chans {
		mc := mcpserver.Channel{
			ID:            c.ID,
			Name:          cmp.Or(c.Name, c.Handle, c.ID),
			Handle:        c.Handle,
			Description:   c.Description,
			Subscribed:    c.Subscribed,
			KeepReads:     c.KeepReads,
			Subcategories: subs[c.ID],
		}
		if st := stats[c.ID]; st != nil {
			mc.Videos = st.Videos
			mc.TopCategories = toMCPCounts(st.Categories)
			mc.TopYouTubeCategories = toMCPCounts(st.YouTube)
		}
		out = append(out, mc)
	}
	return out, nil
}

func toMCPCounts(cs []videos.CategoryCount) []mcpserver.CategoryCount {
	out := make([]mcpserver.CategoryCount, len(cs))
	for i, c := range cs {
		out[i] = mcpserver.CategoryCount{Category: c.ID, Count: c.Count}
	}
	return out
}

// ResolveChannel resolves against what list_youtube_channels shows (subscribed and
// keep_reads channels, videos or not) plus every channel that has videos
// here. A channel id is taken as is; anything else runs the Ask resolver's
// name/handle/typo ladder.
func (l mcpLibrary) ResolveChannel(ctx context.Context, name string) (mcpserver.ChannelScope, error) {
	if l.s.channels == nil || l.s.videos == nil {
		return mcpserver.ChannelScope{}, errMCPNotConfigured
	}
	listed, err := l.Channels(ctx)
	if err != nil {
		return mcpserver.ChannelScope{}, err
	}
	withVideos, err := l.s.videos.ChannelDirectoryCtx(ctx)
	if err != nil {
		return mcpserver.ChannelScope{}, err
	}
	videoCount := map[string]int{}
	dir := make([]videos.ChannelRef, 0, len(listed)+len(withVideos))
	seen := map[string]bool{}
	for _, c := range listed {
		dir = append(dir, videos.ChannelRef{ID: c.ID, Name: c.Name, Handle: c.Handle})
		seen[c.ID] = true
		videoCount[c.ID] = c.Videos
	}
	// Directory rows are added even for a listed id: each carries the name
	// its videos were recorded under, so a renamed channel still answers to
	// its old name. The resolver dedups ids after matching.
	listedName := map[string]string{}
	for _, c := range listed {
		listedName[c.ID] = c.Name
	}
	for _, c := range withVideos {
		dir = append(dir, c)
		if !seen[c.ID] {
			videoCount[c.ID] = -1 // has videos, count not needed
		}
	}

	want := strings.TrimSpace(name)
	var res channelResolution
	for _, c := range dir {
		if c.ID != "" && c.ID == want {
			res = channelResolution{IDs: []string{c.ID}, Matched: []string{c.Name}}
			break
		}
	}
	if !res.any() {
		res = resolveChannelsIn(dir, []string{want})
	}
	scope := mcpserver.ChannelScope{IDs: res.IDs, Names: res.Names}
	// Shown under the name list_youtube_channels uses, once per channel, however
	// many of its names matched.
	shown := map[string]bool{}
	for _, id := range res.IDs {
		if n := listedName[id]; n != "" && !shown[n] {
			shown[n] = true
			scope.Matched = append(scope.Matched, n)
		}
	}
	if len(scope.Matched) == 0 {
		// Nothing listed matched: only channels known by their videos.
		scope.Matched = res.Matched
	}
	if !scope.Resolved() {
		scope.Suggestions = closestChannels(dir, want, mcpSuggestions)
		return scope, nil
	}
	// Name-only matches are legacy rows, which exist only because they have
	// videos; an id match may be a channel nothing has been scanned for yet.
	scope.NoVideos = len(res.Names) == 0
	for _, id := range res.IDs {
		if videoCount[id] != 0 {
			scope.NoVideos = false
		}
	}
	return scope, nil
}

// closestChannels ranks the directory by edit distance to name.
func closestChannels(dir []videos.ChannelRef, name string, n int) []string {
	want := normalizeChannel(name)
	type cand struct {
		name string
		dist int
	}
	seen := map[string]bool{}
	var cands []cand
	for _, c := range dir {
		if c.Name == "" || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		cands = append(cands, cand{c.Name, editDistance(normalizeChannel(c.Name), want)})
	}
	slices.SortFunc(cands, func(a, b cand) int {
		return cmp.Or(cmp.Compare(a.dist, b.dist), cmp.Compare(a.name, b.name))
	})
	out := make([]string, 0, n)
	for i := 0; i < len(cands) && i < n; i++ {
		out = append(out, cands[i].name)
	}
	return out
}

// editDistance is the plain Levenshtein distance between a and b, by rune.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

// errSearchLanesFailed is a search that found nothing because a lane failed,
// which must not read as "no videos match".
var errSearchLanesFailed = errors.New("mcp: keyword and semantic lanes both failed")

func (l mcpLibrary) Search(ctx context.Context, q mcpserver.SearchQuery) (mcpserver.SearchResult, error) {
	s := l.s
	if s.rag == nil || s.videos == nil {
		return mcpserver.SearchResult{}, errMCPNotConfigured
	}
	filter := rag.Filter{
		ChannelIDs:         q.Channel.IDs,
		ChannelNames:       q.Channel.Names,
		Category:           q.Category,
		YouTubeCategory:    q.YouTubeCategory,
		Subcategory:        q.Subcategory,
		ChannelSubcategory: q.ChannelSubcategory,
		After:              q.After,
		Before:             q.Before,
	}
	var hits []rag.Hit
	var res mcpserver.SearchResult
	if q.Keyword {
		// Find's literal match, narrowed by the filter.
		if match := rag.ParseFTSQuery(q.Text); match != "" {
			var err error
			if hits, err = s.rag.SearchFTSFiltered(ctx, match, searchCandidates, filter); err != nil {
				return res, err
			}
		}
	} else {
		lanes, diag := s.askLanes(ctx, q.Text, "", filter, nil)
		hits = rag.FuseWeighted(lanes, searchCandidates)
		diag.log(q.Text, hits)
		// The lanes fail soft for Ask. Here an empty list must mean "nothing
		// matches": empty after a deadline or a failed keyword rung is an
		// error. Anything a lane did find is returned, with a note saying
		// which part of the search was missing.
		if len(hits) == 0 {
			if err := ctx.Err(); err != nil {
				return res, fmt.Errorf("mcp search: %w", err)
			}
			if diag.ftsFailed || (diag.semRaw.failed && !diag.ftsQueried) {
				return res, errSearchLanesFailed
			}
		}
		keywordLanes := 0
		for i := range lanes {
			if i != diag.rawLane && i != diag.topicLane {
				keywordLanes++
			}
		}
		res.SemanticDegraded = !diag.semRaw.ran
		res.KeywordFailed = diag.ftsFailed && keywordLanes == 0
		res.KeywordPartial = diag.ftsFailed && keywordLanes > 0
	}

	byID, err := s.videos.GetManyCtx(ctx, idsOf(hits, func(h rag.Hit) string { return h.VideoID }))
	if err != nil {
		return res, err
	}
	groups := groupHits(hits, byID, q.Videos, 0)
	subs, err := s.videos.SubcategoriesMany(ctx, idsOf(groups, func(g *searchResult) string { return g.Video.ID }))
	if err != nil {
		slog.WarnContext(ctx, "mcp: video subcategories unavailable", "err", err)
	}
	for _, g := range groups {
		vh := mcpserver.VideoHits{Video: toMCPVideo(byID[g.Video.ID])}
		vh.Video.Subcategories = subs[g.Video.ID]
		for _, m := range g.Matches {
			vh.Matches = append(vh.Matches, mcpserver.Match{StartSeconds: m.StartSeconds, Kind: m.Kind, Snippet: m.Snippet})
		}
		res.Videos = append(res.Videos, vh)
	}
	return res, nil
}

func (l mcpLibrary) Video(ctx context.Context, id string) (*mcpserver.Video, error) {
	if l.s.videos == nil {
		return nil, errMCPNotConfigured
	}
	v, err := l.s.videos.GetCtx(ctx, id)
	if err != nil || v == nil {
		return nil, err
	}
	out := toMCPVideo(v)
	if out.Subcategories, err = l.s.videos.SubcategoriesCtx(ctx, id); err != nil {
		slog.WarnContext(ctx, "mcp: video subcategories unavailable", "video_id", id, "err", err)
	}
	var chapters []struct {
		TS    int    `json:"ts"`
		Title string `json:"title"`
	}
	if json.Unmarshal([]byte(v.Chapters), &chapters) == nil {
		for _, c := range chapters {
			out.Chapters = append(out.Chapters, mcpserver.Moment{Seconds: c.TS, Text: c.Title})
		}
	}
	var points []struct {
		TS   int    `json:"ts"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(v.KeyPoints), &points) == nil {
		for _, p := range points {
			out.KeyPoints = append(out.KeyPoints, mcpserver.Moment{Seconds: p.TS, Text: p.Text})
		}
	}
	_ = json.Unmarshal([]byte(v.YTTags), &out.Tags)
	if out.InDepth, err = l.s.videos.InDepthCtx(ctx, id); err != nil {
		return nil, err
	}
	return &out, nil
}

func (l mcpLibrary) Transcript(ctx context.Context, id string) (*mcpserver.Transcript, error) {
	if l.s.videos == nil {
		return nil, errMCPNotConfigured
	}
	v, err := l.s.videos.GetCtx(ctx, id)
	if err != nil || v == nil {
		return nil, err
	}
	t, err := l.s.videos.GetTranscriptCtx(ctx, id)
	if err != nil || t == nil {
		return nil, err
	}
	parsed, err := subtitles.ParseVTT(strings.NewReader(t.VTT))
	if err != nil {
		return nil, err
	}
	return &mcpserver.Transcript{VideoID: v.ID, Title: v.Title, Cues: parsed.Cues}, nil
}

// toMCPVideo copies the metadata and short summary a search result shows.
func toMCPVideo(v *videos.Video) mcpserver.Video {
	out := mcpserver.Video{
		ID:              v.ID,
		Title:           v.Title,
		Channel:         v.ChannelName,
		PublishedAt:     v.PublishedAt,
		DurationSeconds: v.DurationSeconds,
		Category:        v.Category,
		Summary:         v.Summary,
	}
	_ = json.Unmarshal([]byte(v.YTCategories), &out.YouTubeCategories)
	return out
}
