package videos

import (
	"context"
	"database/sql"
	"fmt"
)

// Transcript sources. A caption fetched to decide whether a video is worth
// downloading gets a deliberately truncated analysis — summary only, no
// category, no key points, no embeddings — while a downloaded video gets the
// whole pipeline. Before migration 0023 the two were told apart by looking for
// the ".summaries/" prefix in subtitle_path; with the file gone the provenance
// is recorded rather than inferred from a path string.
const (
	TranscriptSourceDownload = "download"
	TranscriptSourceCaption  = "caption"
)

// MaxTranscriptBytes bounds one stored transcript. Raw auto-caption WebVTT runs
// roughly 3-4 KB per minute, so this is about a 30-hour video — far past
// anything real, which is the point: it exists so a pathological or hostile
// caption track cannot bloat the database, not to reject ordinary videos.
// Nothing capped the .vtt file at all before.
const MaxTranscriptBytes = 8 << 20 // 8 MiB

// Transcript is one stored WebVTT track.
type Transcript struct {
	// Source is TranscriptSourceDownload or TranscriptSourceCaption.
	Source string
	// VTT is the raw WebVTT text, stored verbatim. The <track> element, the
	// browser-side parser and the user-facing .vtt download all want the bytes
	// exactly as yt-dlp wrote them, so this is never normalized or re-rendered.
	VTT       string
	UpdatedAt string
}

// SetTranscript stores (or replaces) a video's transcript.
//
// The conflict clause OVERWRITES source rather than keeping the old value: a
// video first read from the inbox and later downloaded shares one video_id, and
// leaving source='caption' on it would strand a fully downloaded video on the
// truncated inbox pipeline forever.
func (s *Store) SetTranscript(id, source, vtt string) error {
	if id == "" {
		return fmt.Errorf("set transcript: empty video id")
	}
	if source != TranscriptSourceDownload && source != TranscriptSourceCaption {
		return fmt.Errorf("set transcript %s: unknown source %q", id, source)
	}
	if vtt == "" {
		return fmt.Errorf("set transcript %s: empty transcript", id)
	}
	if len(vtt) > MaxTranscriptBytes {
		return fmt.Errorf("set transcript %s: %d bytes exceeds cap %d", id, len(vtt), MaxTranscriptBytes)
	}
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO video_transcripts (video_id, source, vtt, updated_at)
VALUES (?, ?, ?, datetime('now'))
ON CONFLICT(video_id) DO UPDATE SET
	source     = excluded.source,
	vtt        = excluded.vtt,
	updated_at = excluded.updated_at`, id, source, vtt)
	if err != nil {
		return fmt.Errorf("set transcript %s: %w", id, err)
	}
	return nil
}

// GetTranscript returns the stored transcript for id, or (nil, nil) when the
// video has none — the only "missing" case there is: the text is either in the
// row or it does not exist.
func (s *Store) GetTranscript(id string) (*Transcript, error) {
	var t Transcript
	err := s.db.QueryRowContext(context.Background(),
		`SELECT source, vtt, updated_at FROM video_transcripts WHERE video_id = ?`, id,
	).Scan(&t.Source, &t.VTT, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get transcript %s: %w", id, err)
	}
	return &t, nil
}

// TranscriptSource returns just the provenance of a video's transcript, for the
// callers that only need to know whether this is an inbox read. Empty string
// means the video has no transcript.
func (s *Store) TranscriptSource(id string) (string, error) {
	var source string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT source FROM video_transcripts WHERE video_id = ?`, id,
	).Scan(&source)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get transcript source %s: %w", id, err)
	}
	return source, nil
}

// DeleteTranscript drops a video's stored transcript. The ON DELETE CASCADE
// handles a video row going away, so this is for dropping the text alone.
func (s *Store) DeleteTranscript(id string) error {
	if _, err := s.db.ExecContext(context.Background(),
		`DELETE FROM video_transcripts WHERE video_id = ?`, id,
	); err != nil {
		return fmt.Errorf("delete transcript %s: %w", id, err)
	}
	return nil
}

// TranscriptImportCandidate is a video with no stored transcript yet, and what
// the import worker needs to go looking for its .vtt on disk.
type TranscriptImportCandidate struct {
	ID           string
	ChannelID    string
	SubtitlePath string
}

// TranscriptlessVideos returns up to limit videos that have no row in
// video_transcripts, newest first.
//
// Not filtered by subtitle_path: a blanked pointer does not mean a missing
// file, and those rows are exactly what the import worker exists to rescue.
func (s *Store) TranscriptlessVideos(limit int) ([]TranscriptImportCandidate, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(context.Background(), `
SELECT v.id, v.channel_id, v.subtitle_path
FROM videos v
WHERE NOT EXISTS (SELECT 1 FROM video_transcripts t WHERE t.video_id = v.id)
ORDER BY COALESCE(v.downloaded_at, v.created_at) DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list transcriptless videos: %w", err)
	}
	defer rows.Close()

	var out []TranscriptImportCandidate
	for rows.Next() {
		var c TranscriptImportCandidate
		if err := rows.Scan(&c.ID, &c.ChannelID, &c.SubtitlePath); err != nil {
			return nil, fmt.Errorf("scan transcriptless video: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list transcriptless videos: %w", err)
	}
	return out, nil
}
