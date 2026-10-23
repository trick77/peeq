package videos

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// CategoryCount is one bucket of a category histogram.
type CategoryCount struct {
	ID    string
	Count int
}

// ChannelStats is what one channel's videos are about.
type ChannelStats struct {
	Videos int
	// Categories is peeq's category histogram, largest first, without
	// 'uncategorized'. YouTube is the same over yt_categories entries.
	Categories []CategoryCount
	YouTube    []CategoryCount
}

// channelCategoryStatsSQL groups on every column it reads so the covering
// index from migration 0040 answers it without opening a video row.
const channelCategoryStatsSQL = `
SELECT channel_id, category, yt_categories, COUNT(*)
FROM videos INDEXED BY idx_videos_channel_categories
WHERE channel_id <> ''
GROUP BY channel_id, category, yt_categories`

// ChannelCategoryStats returns per-channel video counts and category
// histograms, keyed by channel id. Rows recorded without a channel id are
// left out.
func (s *Store) ChannelCategoryStats(ctx context.Context) (map[string]*ChannelStats, error) {
	rows, err := s.db.QueryContext(ctx, channelCategoryStatsSQL)
	if err != nil {
		return nil, fmt.Errorf("channel category stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	cats := map[string]map[string]int{}
	yts := map[string]map[string]int{}
	out := map[string]*ChannelStats{}
	for rows.Next() {
		var ch, cat, ytRaw string
		var n int
		if err := rows.Scan(&ch, &cat, &ytRaw, &n); err != nil {
			return nil, fmt.Errorf("channel category stats: %w", err)
		}
		st := out[ch]
		if st == nil {
			st = &ChannelStats{}
			out[ch] = st
			cats[ch] = map[string]int{}
			yts[ch] = map[string]int{}
		}
		st.Videos += n
		if cat != "" && cat != UncategorizedCategory {
			cats[ch][cat] += n
		}
		// A malformed column (yt-dlp output) counts toward no YouTube label.
		var labels []string
		if json.Unmarshal([]byte(ytRaw), &labels) == nil {
			for _, l := range labels {
				if l != "" {
					yts[ch][l] += n
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("channel category stats: %w", err)
	}
	for ch, st := range out {
		st.Categories = sortedCounts(cats[ch])
		st.YouTube = sortedCounts(yts[ch])
	}
	return out, nil
}

// sortedCounts orders a histogram largest first, ties by name, so output is
// deterministic.
func sortedCounts(m map[string]int) []CategoryCount {
	out := make([]CategoryCount, 0, len(m))
	for id, n := range m {
		out = append(out, CategoryCount{ID: id, Count: n})
	}
	slices.SortFunc(out, func(a, b CategoryCount) int {
		if c := cmp.Compare(b.Count, a.Count); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}
