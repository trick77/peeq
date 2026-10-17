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
// about: OnDisk is the downloaded videos, Kept is what survives a file and
// what turning the switch off purges (tombstones and ignored reads, by the
// same rule as keptVideos), Other is everything else (the New tab, queued or
// failed work).
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
//
// The where clause is channels.Stats' own, legacy name fallback included, so
// the files here add up to the header's figure. Reading status and file size
// from the row costs what that header already pays on every channel load.
//
// Kept is the switch-off purge's set and nothing else, so it equals what the
// confirm dialog names: keptVideos' rule (a once-downloaded ignore is not a
// kept read), and only rows carrying the channel's id — a legacy row matched
// by name is never purged, so it counts under Other. store_storage_test ties
// the two together.
func channelStorageQuery(where string) string {
	return `
SELECT CASE
         WHEN v.status = 'downloaded' THEN 'on_disk'
         WHEN v.channel_id = ? AND v.status = 'tombstoned' THEN 'kept'
         WHEN v.channel_id = ? AND v.status = 'new' AND cv.state = 'ignored'
              AND COALESCE(t.source, '') != 'download' THEN 'kept'
         ELSE 'other'
       END AS grp,
       COUNT(*),
       SUM(CASE WHEN v.status = 'downloaded' THEN COALESCE(v.filesize_bytes, 0) ELSE 0 END),
       SUM(COALESCE(octet_length(t.vtt), 0)),
       SUM(COALESCE(octet_length(v.summary), 0) + COALESCE(octet_length(v.chapters), 0)
           + COALESCE(octet_length(v.key_points), 0)
           + COALESCE((SELECT octet_length(d.body) FROM video_in_depth d WHERE d.video_id = v.id), 0)),
       SUM(COALESCE((SELECT SUM(octet_length(c.text)) FROM transcript_chunks c WHERE c.video_id = v.id), 0)),
       SUM((SELECT COUNT(*) FROM transcript_chunks c WHERE c.video_id = v.id)),
       SUM(COALESCE((SELECT octet_length(th.bytes) FROM video_thumbnails th WHERE th.video_id = v.id), 0))
FROM videos v
LEFT JOIN channel_videos cv ON cv.video_id = v.id
LEFT JOIN video_transcripts t ON t.video_id = v.id
WHERE ` + where + `
GROUP BY grp`
}

// ChannelStorage measures what peeq stores for one channel's videos.
// channelName is the fallback for videos written before channel ids were
// recorded, as for channels.Store.Stats; "" matches on channel_id alone.
func (s *Store) ChannelStorage(ctx context.Context, channelID, channelName string) (ChannelStorage, error) {
	where := "v.channel_id = ?"
	// The two CASE binds first, then the where clause's.
	args := []any{channelID, channelID, channelID}
	if channelName != "" {
		where = "(v.channel_id = ? OR (v.channel_id = '' AND v.channel_name = ?))"
		args = append(args, channelName)
	}
	rows, err := s.db.QueryContext(ctx, channelStorageQuery(where), args...)
	if err != nil {
		return ChannelStorage{}, fmt.Errorf("channel storage %s: %w", channelID, err)
	}
	defer func() { _ = rows.Close() }()
	var out ChannelStorage
	var chunksBy [3]int64
	for rows.Next() {
		var grp string
		var g StorageGroup
		var chunkText, chunks int64
		if err := rows.Scan(&grp, &g.Videos, &g.FileBytes, &g.TranscriptBytes, &g.SummaryBytes,
			&chunkText, &chunks, &g.PosterBytes); err != nil {
			return ChannelStorage{}, fmt.Errorf("channel storage %s: %w", channelID, err)
		}
		g.SearchTextBytes = 2 * chunkText
		switch grp {
		case "on_disk":
			out.OnDisk, chunksBy[0] = g, chunks
		case "kept":
			out.Kept, chunksBy[1] = g, chunks
		default:
			out.Other, chunksBy[2] = g, chunks
		}
	}
	if err := rows.Err(); err != nil {
		return ChannelStorage{}, fmt.Errorf("channel storage %s: %w", channelID, err)
	}
	// The vector width only matters once there is a chunk to multiply.
	if chunksBy[0]+chunksBy[1]+chunksBy[2] > 0 {
		dim, err := rag.NewStore(s.db).BuiltDim(ctx)
		if err != nil {
			return ChannelStorage{}, err
		}
		out.OnDisk.VectorBytes = chunksBy[0] * int64(dim) * 4
		out.Kept.VectorBytes = chunksBy[1] * int64(dim) * 4
		out.Other.VectorBytes = chunksBy[2] * int64(dim) * 4
	}
	return out, nil
}
