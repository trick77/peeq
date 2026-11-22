package videos

import (
	"context"
	"fmt"
)

// channelSearchableSQL is channels' indexedCountQuery for every channel at
// once: a video is searchable when it has a transcript_chunks row, whatever
// its status. idx_videos_channel_search hands over the ids in channel order,
// so the GROUP BY needs no sort and no video row is opened (a row keeps its id
// behind the big text columns); each id probes idx_transcript_chunks_video.
const channelSearchableSQL = `
SELECT v.channel_id, COUNT(*)
FROM videos v INDEXED BY idx_videos_channel_search
WHERE v.channel_id <> ''
  AND EXISTS (SELECT 1 FROM transcript_chunks c WHERE c.video_id = v.id)
GROUP BY v.channel_id`

// ChannelSearchableCounts returns, per channel id, how many of its videos
// search can reach. A channel with none is absent; rows recorded without a
// channel id are left out, as in ChannelCategoryStats.
func (s *Store) ChannelSearchableCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, channelSearchableSQL)
	if err != nil {
		return nil, fmt.Errorf("channel searchable counts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]int{}
	for rows.Next() {
		var ch string
		var n int
		if err := rows.Scan(&ch, &n); err != nil {
			return nil, fmt.Errorf("channel searchable counts: %w", err)
		}
		out[ch] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("channel searchable counts: %w", err)
	}
	return out, nil
}
