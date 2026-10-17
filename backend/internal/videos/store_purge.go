package videos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/trick77/peeq/internal/rag"
)

// Purge is a delete on a channel whose keep_reads is off: the row and
// everything hanging off it go — transcript, summary, poster, in-depth text,
// share link, and the search index, which unlike the rest cannot ride the
// cascade. Tombstone is the keep_reads-on counterpart.
//
// What keeps the video gone is its channel_videos ledger row: a scan skips any
// video the ledger knows, except one parked as 'unavailable', which it revives
// when no downloaded videos row says otherwise — and after a purge there is no
// row at all; a 'pending' one would sit in the New tab again. So the ledger
// row is settled at 'queued' (approved, done with),
// and written if the video never had one (added by URL or the extension),
// dated by when peeq first had the video rather than now: discovered_at feeds
// the channel's latest-video date and the dormancy check, and a delete is not
// a discovery. The
// inbox poster hangs off that ledger row rather than the videos row, so the
// cascade misses it and it is deleted here.
//
// The media file is the caller's, as for Tombstone: row first, then
// media.RemoveTombstonedVideoFiles with the path read beforehand.
func (s *Store) Purge(id string) error {
	chunks, err := s.purge(id)
	if err != nil {
		return err
	}
	if chunks > 0 {
		// The purged vectors keep their storage (rag.CompactVectors).
		rag.WarnVectorBloat(context.Background(), s.db, slog.Default(), "video_id", id)
	}
	return nil
}

// purge is Purge without the bloat check, so a batch measures once at the end
// rather than counting the vector shadow tables per video. It reports how many
// chunks went.
func (s *Store) purge(id string) (int, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("purge video %s: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	// Before the delete: it reads the row's channel, title and url. The
	// channels join is the ledger's foreign key; a video whose channel row
	// does not exist is never purged in the first place (its keep_reads reads
	// as on).
	if _, err := tx.ExecContext(ctx, `
INSERT INTO channel_videos (video_id, channel_id, title, url, state, discovered_at, decided_at)
SELECT v.id, v.channel_id, v.title, v.url, 'queued', v.created_at, datetime('now')
FROM videos v JOIN channels c ON c.id = v.channel_id
WHERE v.id = ?
ON CONFLICT(video_id) DO UPDATE SET
    state = 'queued', decided_at = datetime('now'),
    unavailable_reason = '', unavailable_at = NULL
WHERE channel_videos.state IN ('unavailable', 'pending')`, id); err != nil {
		return 0, fmt.Errorf("purge video %s: settle ledger: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_thumbnails WHERE video_id = ?`, id); err != nil {
		return 0, fmt.Errorf("purge video %s: poster: %w", id, err)
	}
	chunks, err := rag.DeleteVideoTx(ctx, tx, id)
	if err != nil {
		return 0, fmt.Errorf("purge video %s: chunks: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM videos WHERE id = ?`, id); err != nil {
		return 0, fmt.Errorf("purge video %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("purge video %s: commit: %w", id, err)
	}
	return chunks, nil
}

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
// see queryplan_test.go. Not forced with INDEXED BY: that index is partial,
// usable only while the status term here matches its WHERE exactly, and a
// forced index that stops matching fails the query at runtime instead of
// just planning worse — the plan test is the guard.
const keptVideosQuery = `
SELECT v.id, v.status, COALESCE(cv.state, ''),
       COALESCE((SELECT t.source FROM video_transcripts t WHERE t.video_id = v.id), ''),
       EXISTS (SELECT 1 FROM transcript_chunks c WHERE c.video_id = v.id)
FROM videos v
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
	chunks := 0
	// Measured once for the batch, not per video: the check counts the vector
	// shadow tables.
	defer func() {
		if chunks > 0 {
			rag.WarnVectorBloat(context.Background(), s.db, slog.Default(), "channel_id", channelID)
		}
	}()
	for _, k := range kept {
		if !stillOff() {
			break
		}
		var n int
		if k.kind == keptInbox {
			n, err = s.dropIndex(k.id)
		} else {
			n, err = s.purge(k.id)
		}
		chunks += n
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
	chunks, err := s.dropIndex(id)
	if err != nil {
		return err
	}
	if chunks > 0 {
		rag.WarnVectorBloat(context.Background(), s.db, slog.Default(), "video_id", id)
	}
	return nil
}

// dropIndex is DropIndex without the bloat check (see purge).
func (s *Store) dropIndex(id string) (int, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("drop index %s: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	chunks, err := rag.DeleteVideoTx(ctx, tx, id)
	if err != nil {
		return 0, fmt.Errorf("drop index %s: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE videos SET embed_model = '', embed_dim = 0, embed_rev = 0 WHERE id = ?`, id); err != nil {
		return 0, fmt.Errorf("drop index %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("drop index %s: commit: %w", id, err)
	}
	return chunks, nil
}

// ChannelKeepReads reads only the video's channel keep_reads, as it is now —
// for a caller that holds a stale row and must not pay for re-reading all of
// it (videoColumns walks the big text columns). A video with no channel row
// reads as on, as in videoColumns; a missing video reads as off.
func (s *Store) ChannelKeepReads(id string) (bool, error) {
	var keep bool
	err := s.db.QueryRowContext(context.Background(), `
SELECT COALESCE(ch.keep_reads, 1) FROM videos v
LEFT JOIN channels ch ON ch.id = v.channel_id
WHERE v.id = ?`, id).Scan(&keep)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("channel keep_reads %s: %w", id, err)
	}
	return keep, nil
}
