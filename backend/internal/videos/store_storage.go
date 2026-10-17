package videos

import (
	"context"
	"fmt"

	"github.com/trick77/peeq/internal/rag"
)

// StorageGroup is what peeq stores for a set of videos: their media files on
// disk, and in the database their transcripts, summaries (prose, chapters, key
// points, in-depth text), search index and posters.
type StorageGroup struct {
	Videos          int   `json:"videos"`
	FileBytes       int64 `json:"file_bytes"`
	TranscriptBytes int64 `json:"transcript_bytes"`
	SummaryBytes    int64 `json:"summary_bytes"`
	// SearchTextBytes is the chunk text, counted twice: transcript_chunks
	// holds it and the full-text index keeps its own copy. FTS5's inverted
	// index on top is not attributable to a channel and is left out.
	SearchTextBytes int64 `json:"search_text_bytes"`
	// VectorBytes is chunks × vector width × 4 (float32): the live payload.
	// Space vec0 keeps after deletes belongs to no channel
	// (rag.CompactVectors reclaims it at boot).
	VectorBytes int64 `json:"vector_bytes"`
	PosterBytes int64 `json:"poster_bytes"`
}

// ChannelStorage splits a channel's storage by what the keep_reads switch is
// about: OnDisk is the downloaded videos, Kept is what survives a file
// (tombstones and ignored reads kept for search), Other is everything else
// (the New tab, queued or failed work).
type ChannelStorage struct {
	OnDisk StorageGroup `json:"on_disk"`
	Kept   StorageGroup `json:"kept"`
	Other  StorageGroup `json:"other"`
}

// channelStorageQuery sums per group in one statement, so the three parts are
// one snapshot. Every size is octet_length, which SQLite answers from the
// record header without loading the value: the transcripts, summaries and
// posters are the big values on these rows, and reading them would walk their
// overflow pages. The channel page asks for this on its own request, never on
// load, because it still visits every row of the channel.
const channelStorageQuery = `
SELECT CASE
         WHEN v.status = 'downloaded' THEN 'on_disk'
         WHEN v.status = 'tombstoned' OR (v.status = 'new' AND cv.state = 'ignored') THEN 'kept'
         ELSE 'other'
       END AS grp,
       COUNT(*),
       SUM(CASE WHEN v.status = 'downloaded' THEN v.filesize_bytes ELSE 0 END),
       SUM(COALESCE((SELECT octet_length(t.vtt) FROM video_transcripts t WHERE t.video_id = v.id), 0)),
       SUM(COALESCE(octet_length(v.summary), 0) + COALESCE(octet_length(v.chapters), 0)
           + COALESCE(octet_length(v.key_points), 0)
           + COALESCE((SELECT octet_length(d.body) FROM video_in_depth d WHERE d.video_id = v.id), 0)),
       SUM(COALESCE((SELECT SUM(octet_length(c.text)) FROM transcript_chunks c WHERE c.video_id = v.id), 0)),
       SUM((SELECT COUNT(*) FROM transcript_chunks c WHERE c.video_id = v.id)),
       SUM(COALESCE((SELECT octet_length(th.bytes) FROM video_thumbnails th WHERE th.video_id = v.id), 0))
FROM videos v
LEFT JOIN channel_videos cv ON cv.video_id = v.id
WHERE v.channel_id = ?
GROUP BY grp`

// ChannelStorage measures what peeq stores for one channel's videos.
func (s *Store) ChannelStorage(ctx context.Context, channelID string) (ChannelStorage, error) {
	dim, err := rag.NewStore(s.db).BuiltDim(ctx)
	if err != nil {
		return ChannelStorage{}, err
	}
	rows, err := s.db.QueryContext(ctx, channelStorageQuery, channelID)
	if err != nil {
		return ChannelStorage{}, fmt.Errorf("channel storage %s: %w", channelID, err)
	}
	defer func() { _ = rows.Close() }()
	var out ChannelStorage
	for rows.Next() {
		var grp string
		var g StorageGroup
		var chunkText, chunks int64
		if err := rows.Scan(&grp, &g.Videos, &g.FileBytes, &g.TranscriptBytes, &g.SummaryBytes,
			&chunkText, &chunks, &g.PosterBytes); err != nil {
			return ChannelStorage{}, fmt.Errorf("channel storage %s: %w", channelID, err)
		}
		g.SearchTextBytes = 2 * chunkText
		g.VectorBytes = chunks * int64(dim) * 4
		switch grp {
		case "on_disk":
			out.OnDisk = g
		case "kept":
			out.Kept = g
		default:
			out.Other = g
		}
	}
	if err := rows.Err(); err != nil {
		return ChannelStorage{}, fmt.Errorf("channel storage %s: %w", channelID, err)
	}
	return out, nil
}
