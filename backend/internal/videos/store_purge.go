package videos

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/trick77/peeq/internal/rag"
)

// KeptCounts is what turning a channel's keep_reads off takes away, and later
// what it did take: Deleted tombstones and Ignored kept inbox reads are purged
// outright; Inbox videos still awaiting a decision only lose their search
// index, since the New tab card still shows their summary.
type KeptCounts struct {
	Deleted int `json:"deleted"`
	Ignored int `json:"ignored"`
	Inbox   int `json:"inbox"`
}

type keptKind int

const (
	keptNone keptKind = iota
	keptDeleted
	keptIgnored
	keptInbox
)

type keptVideo struct {
	id   string
	kind keptKind
}

// keptVideosQuery lists the channel's tombstoned and never-downloaded videos
// with what the decision needs: the ledger state, the transcript source and
// whether the video is indexed. It stays on idx_videos_channel_kept (0037);
// see queryplan_test.go.
const keptVideosQuery = `
SELECT v.id, v.status, COALESCE(cv.state, ''),
       COALESCE((SELECT t.source FROM video_transcripts t WHERE t.video_id = v.id), ''),
       EXISTS (SELECT 1 FROM transcript_chunks c WHERE c.video_id = v.id)
FROM videos v INDEXED BY idx_videos_channel_kept
LEFT JOIN channel_videos cv ON cv.video_id = v.id
WHERE v.channel_id = ? AND v.status IN ('tombstoned', 'new')`

// keptVideos classifies the channel's videos for a switch-off purge.
//
// A 'new' row is only ever a kept read when its ledger row says so and its
// transcript came from a caption read, the same guard dropInboxRead uses: a
// cancelled download is also 'new' but sits at ledger 'queued', and anything
// that was ever downloaded carries a 'download' transcript.
func (s *Store) keptVideos(channelID string) ([]keptVideo, error) {
	rows, err := s.db.QueryContext(context.Background(), keptVideosQuery, channelID)
	if err != nil {
		return nil, fmt.Errorf("kept videos %s: %w", channelID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []keptVideo
	for rows.Next() {
		var id, status, state, source string
		var indexed bool
		if err := rows.Scan(&id, &status, &state, &source, &indexed); err != nil {
			return nil, fmt.Errorf("kept videos %s: %w", channelID, err)
		}
		kind := keptNone
		switch {
		case status == StatusTombstoned:
			kind = keptDeleted
		case source == TranscriptSourceDownload:
		case state == "ignored":
			kind = keptIgnored
		case state == "pending" && indexed:
			kind = keptInbox
		}
		if kind != keptNone {
			out = append(out, keptVideo{id: id, kind: kind})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("kept videos %s: %w", channelID, err)
	}
	return out, nil
}

// KeptImpact counts what PurgeKept would take, for the confirm dialog.
func (s *Store) KeptImpact(channelID string) (KeptCounts, error) {
	kept, err := s.keptVideos(channelID)
	if err != nil {
		return KeptCounts{}, err
	}
	var c KeptCounts
	for _, k := range kept {
		c.add(k.kind)
	}
	return c, nil
}

func (c *KeptCounts) add(k keptKind) {
	switch k {
	case keptDeleted:
		c.Deleted++
	case keptIgnored:
		c.Ignored++
	case keptInbox:
		c.Inbox++
	}
}

// PurgeKept carries out a switch-off: it purges the channel's tombstones and
// kept inbox reads and drops the index of its indexed inbox videos, returning
// what it did. stillOff is asked before each video; the switch can be turned
// back on while this runs, and what is left then stays.
//
// One transaction per video, never one for the lot: a channel can hold
// hundreds, and holding the write lock that long starves every other writer
// past busy_timeout.
func (s *Store) PurgeKept(channelID string, stillOff func() bool) (KeptCounts, error) {
	kept, err := s.keptVideos(channelID)
	if err != nil {
		return KeptCounts{}, err
	}
	var done KeptCounts
	for _, k := range kept {
		if !stillOff() {
			break
		}
		if k.kind == keptInbox {
			err = s.DropIndex(k.id)
		} else {
			err = s.Purge(k.id)
		}
		if err != nil {
			return done, err
		}
		done.add(k.kind)
	}
	return done, nil
}

// DropIndex removes a video's search index and marks it unindexed, so a later
// download re-embeds it: the summary worker only embeds below
// rag.ChunkRecipeRev, and a stale embed_rev would leave it unsearchable for
// good. Everything else on the row stays.
func (s *Store) DropIndex(id string) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("drop index %s: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	chunks, err := rag.DeleteVideoTx(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("drop index %s: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE videos SET embed_model = '', embed_dim = 0, embed_rev = 0 WHERE id = ?`, id); err != nil {
		return fmt.Errorf("drop index %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("drop index %s: commit: %w", id, err)
	}
	if chunks > 0 {
		rag.WarnVectorBloat(ctx, s.db, slog.Default(), "video_id", id)
	}
	return nil
}
