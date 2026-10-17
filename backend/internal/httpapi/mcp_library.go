package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/trick77/peeq/internal/mcpserver"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/subtitles"
	"github.com/trick77/peeq/internal/videos"
)

// mcpLibrary is the MCP server's view of the library. It runs the same lanes,
// fusion, grouping and channel resolution as the Search page, minus the
// query-understanding call: an MCP client's model passes filters as
// arguments, so there is nothing to extract.
type mcpLibrary struct{ s *server }

// mcpSuggestions is how many close channel names a miss offers.
const mcpSuggestions = 5

var errMCPNotConfigured = errors.New("mcp: library store not configured")

func (l mcpLibrary) Channels(ctx context.Context) ([]mcpserver.Channel, error) {
	if l.s.channels == nil {
		return nil, errMCPNotConfigured
	}
	items, err := l.s.channels.List("all")
	if err != nil {
		return nil, err
	}
	stats := map[string]*videos.ChannelStats{}
	if l.s.videos != nil {
		if stats, err = l.s.videos.ChannelCategoryStats(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]mcpserver.Channel, 0, len(items))
	for _, it := range items {
		if !it.Subscribed && !it.KeepReads {
			continue
		}
		c := mcpserver.Channel{
			ID:          it.ID,
			Name:        cmp.Or(it.Name, it.Handle, it.ID),
			Handle:      it.Handle,
			Description: it.Description,
			Subscribed:  it.Subscribed,
			KeepReads:   it.KeepReads,
		}
		if st := stats[it.ID]; st != nil {
			c.Videos = st.Videos
			c.TopCategories = toMCPCounts(st.Categories)
			c.TopYouTubeCategories = toMCPCounts(st.YouTube)
		}
		out = append(out, c)
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

// ResolveChannel accepts a channel id as is, else runs the Ask resolver's
// name/handle/typo ladder over the channels that have videos here.
func (l mcpLibrary) ResolveChannel(_ context.Context, name string) (mcpserver.ChannelScope, error) {
	if l.s.videos == nil {
		return mcpserver.ChannelScope{}, errMCPNotConfigured
	}
	dir, err := l.s.videos.ChannelDirectory()
	if err != nil {
		return mcpserver.ChannelScope{}, err
	}
	want := strings.TrimSpace(name)
	for _, c := range dir {
		if c.ID != "" && c.ID == want {
			return mcpserver.ChannelScope{IDs: []string{c.ID}, Matched: []string{c.Name}}, nil
		}
	}
	var scope mcpserver.ChannelScope
	seen := map[string]bool{}
	for _, m := range matchChannels(dir, want) {
		if m.ID != "" {
			scope.IDs = append(scope.IDs, m.ID)
		} else {
			scope.Names = append(scope.Names, m.Name)
		}
		if !seen[m.Name] {
			seen[m.Name] = true
			scope.Matched = append(scope.Matched, m.Name)
		}
	}
	if !scope.Resolved() {
		scope.Suggestions = closestChannels(dir, want, mcpSuggestions)
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

func (l mcpLibrary) Search(ctx context.Context, q mcpserver.SearchQuery) (mcpserver.SearchResult, error) {
	s := l.s
	if s.rag == nil || s.videos == nil {
		return mcpserver.SearchResult{}, errMCPNotConfigured
	}
	filter := rag.Filter{
		ChannelIDs:      q.Channel.IDs,
		ChannelNames:    q.Channel.Names,
		Category:        q.Category,
		YouTubeCategory: q.YouTubeCategory,
		After:           q.After,
		Before:          q.Before,
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
		res.SemanticDegraded = !diag.semRaw.ran
	}

	byID, err := s.videos.GetMany(idsOf(hits, func(h rag.Hit) string { return h.VideoID }))
	if err != nil {
		return res, err
	}
	// The Search page's grouping: per-video cap, one hit per moment.
	order := []string{}
	groups := map[string]*searchResult{}
	for _, h := range hits {
		g, ok := groups[h.VideoID]
		if !ok {
			if len(order) >= q.Videos || byID[h.VideoID] == nil {
				continue
			}
			g = &searchResult{}
			groups[h.VideoID] = g
			order = append(order, h.VideoID)
		}
		if len(g.Matches) >= maxMatchesPerVideo || !g.admits(h) {
			continue
		}
		g.Matches = append(g.Matches, searchMatch{StartSeconds: h.StartSeconds, Snippet: matchSnippet(h), Kind: h.Kind})
	}
	for _, id := range order {
		vh := mcpserver.VideoHits{Video: toMCPVideo(byID[id])}
		for _, m := range groups[id].Matches {
			vh.Matches = append(vh.Matches, mcpserver.Match{StartSeconds: m.StartSeconds, Kind: m.Kind, Snippet: m.Snippet})
		}
		res.Videos = append(res.Videos, vh)
	}
	return res, nil
}

func (l mcpLibrary) Video(_ context.Context, id string) (*mcpserver.Video, error) {
	if l.s.videos == nil {
		return nil, errMCPNotConfigured
	}
	v, err := l.s.videos.Get(id)
	if err != nil || v == nil {
		return nil, err
	}
	out := toMCPVideo(v)
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
	if out.InDepth, err = l.s.videos.InDepth(id); err != nil {
		return nil, err
	}
	return &out, nil
}

func (l mcpLibrary) Transcript(_ context.Context, id string) (*mcpserver.Transcript, error) {
	if l.s.videos == nil {
		return nil, errMCPNotConfigured
	}
	v, err := l.s.videos.Get(id)
	if err != nil || v == nil {
		return nil, err
	}
	t, err := l.s.videos.GetTranscript(id)
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
