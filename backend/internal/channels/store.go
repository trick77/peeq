// Package channels persists a metadata cache of YouTube channels (the
// channels table) and their optional subscriptions (the subscriptions
// table) from migration 0001_init.sql. A channels row does NOT mean
// "tracked" — it exists for any channel peeq has ever looked at, including
// ones the user never explicitly added. Tracking is tracked_at IS NOT NULL,
// set via Track. A subscription row means "subscribed" (the scheduler
// periodically scans it for new videos); a channel can be tracked without
// being subscribed, but a subscription always implies a tracked channel
// (subscriptions.channel_id references channels.id).
package channels

import (
	"context"
	"database/sql"
	"fmt"
)

// Channel mirrors one row of the channels table. A Channel may exist purely
// as a metadata cache entry: TrackedAt is empty for a channel the user has
// visited but never tracked. AvatarPath and BannerPath are relative to the
// media dir (resolve them with media.SafeMediaPath before serving).
type Channel struct {
	ID          string
	Handle      string
	Name        string
	Description string
	AvatarPath  string
	BannerPath  string
	ResolvedAt  string
	TrackedAt   string
	AddedAt     string
}

// Subscription mirrors one row of the subscriptions table. BaselinedAt and
// LastScannedAt are empty strings when the underlying column is NULL
// (BaselinedAt is NULL until the subscription's first scan completes).
type Subscription struct {
	ChannelID      string
	Autodownload   bool
	FormatOverride string
	BaselinedAt    string
	LastScannedAt  string
	NextScanAt     string
	CreatedAt      string
}

// ListItem is a channel joined with its (optional) subscription state, plus
// counts used by the channels list UI.
type ListItem struct {
	Channel
	Subscribed      bool
	Autodownload    bool
	FormatOverride  string
	PendingCount    int
	DownloadedCount int
}

// Store persists channels and subscriptions.
type Store struct {
	db *sql.DB
}

// New returns a channels store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB exposes the underlying handle so tests can seed rows the channels Store
// itself has no writer for (videos, download_jobs, channel_videos) when
// exercising the delete cascade.
func (s *Store) DB() *sql.DB {
	return s.db
}

// VideoRef identifies one of a channel's downloaded videos and the on-disk
// files that belong to it. It is read BEFORE a cascade delete so the HTTP
// handler can unlink media/thumbnail files after the videos rows are gone.
type VideoRef struct {
	VideoID       string
	MediaPath     string
	ThumbnailPath string
	SubtitlePath  string
}

// VideoRefs returns a VideoRef for every videos row belonging to channelID.
// Callers read these before DeleteCascade so the media/thumbnail/subtitle
// paths (lost once the rows are deleted) are still available for unlinking
// the files.
func (s *Store) VideoRefs(channelID string) ([]VideoRef, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, media_path, thumbnail_path, subtitle_path FROM videos WHERE channel_id = ?`, channelID)
	if err != nil {
		return nil, fmt.Errorf("video refs: %w", err)
	}
	defer rows.Close()
	var out []VideoRef
	for rows.Next() {
		var r VideoRef
		if err := rows.Scan(&r.VideoID, &r.MediaPath, &r.ThumbnailPath, &r.SubtitlePath); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteCascade removes a channel and everything belonging to it in one
// transaction. vec_chunks (a vec0 virtual table) and fts_chunks (an fts5
// virtual table) cannot ride an FK cascade or trigger, so their rows for
// this channel's videos are purged explicitly FIRST, by rowid (==
// transcript_chunks.id) — before the videos delete cascades away the
// transcript_chunks rows that rowid comes from. videos itself has no
// foreign key to channels, so its rows are deleted explicitly by channel_id
// (this FK-cascades their download_jobs, transcript_chunks, and summary_jobs).
// Deleting the channel row then FK-cascades the subscription and
// channel_videos ledger rows. This intentionally removes ALL of the channel's
// videos, including favorited "Kept forever" ones — the explicit
// delete-channel action overrides the retention invariant (the UI guards it
// behind a confirm).
func (s *Store) DeleteCascade(channelID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// vec_chunks (vec0) and fts_chunks (fts5) can't ride an FK cascade, so
	// purge their rows for this channel's videos explicitly, by rowid,
	// BEFORE the videos delete cascades their transcript_chunks away (which
	// would strand the vec/fts rows forever).
	rows, err := tx.Query(`
SELECT tc.id FROM transcript_chunks tc
JOIN videos v ON v.id = tc.video_id
WHERE v.channel_id = ?`, channelID)
	if err != nil {
		return fmt.Errorf("select chunk rowids for channel: %w", err)
	}
	var chunkIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan chunk rowid: %w", err)
		}
		chunkIDs = append(chunkIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate chunk rowids for channel: %w", err)
	}
	for _, id := range chunkIDs {
		if _, err := tx.Exec(`DELETE FROM vec_chunks WHERE rowid = ?`, id); err != nil {
			return fmt.Errorf("delete vec_chunks row %d: %w", id, err)
		}
		if _, err := tx.Exec(`DELETE FROM fts_chunks WHERE rowid = ?`, id); err != nil {
			return fmt.Errorf("delete fts_chunks row %d: %w", id, err)
		}
	}

	if _, err := tx.Exec(`DELETE FROM videos WHERE channel_id = ?`, channelID); err != nil {
		return fmt.Errorf("delete videos for channel: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM channels WHERE id = ?`, channelID); err != nil {
		return fmt.Errorf("delete channel: %w", err)
	}
	return tx.Commit()
}

// Upsert caches a channel's identity, inserting it if new or refreshing the
// resolved metadata if it already exists. It deliberately does NOT touch
// tracked_at: caching a channel's details must never track or untrack it.
// Empty fields do not overwrite stored values, so a partial refresh cannot
// blank out a name that was already known.
func (s *Store) Upsert(c Channel) error {
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO channels (id, handle, name, description, avatar_path, banner_path, resolved_at)
VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''))
ON CONFLICT(id) DO UPDATE SET
    handle      = COALESCE(NULLIF(excluded.handle, ''), channels.handle),
    name        = COALESCE(NULLIF(excluded.name, ''), channels.name),
    description = COALESCE(NULLIF(excluded.description, ''), channels.description),
    avatar_path = COALESCE(NULLIF(excluded.avatar_path, ''), channels.avatar_path),
    banner_path = COALESCE(NULLIF(excluded.banner_path, ''), channels.banner_path),
    resolved_at = COALESCE(excluded.resolved_at, channels.resolved_at)`,
		c.ID, c.Handle, c.Name, c.Description, c.AvatarPath, c.BannerPath, c.ResolvedAt,
	)
	if err != nil {
		return fmt.Errorf("upsert channel %s: %w", c.ID, err)
	}
	return nil
}

// Track marks a cached channel as explicitly tracked by the user. It is
// idempotent: re-tracking an already-tracked channel keeps the original
// timestamp rather than resetting "tracked since".
func (s *Store) Track(channelID, trackedAt string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE channels SET tracked_at = COALESCE(tracked_at, ?) WHERE id = ?`,
		trackedAt, channelID,
	)
	if err != nil {
		return fmt.Errorf("track channel %s: %w", channelID, err)
	}
	return nil
}

// MarkResolveAttempted records that a metadata fetch was tried, whether or
// not it succeeded. Without this a permanently unresolvable channel would be
// re-fetched from YouTube on every single page visit.
func (s *Store) MarkResolveAttempted(channelID, at string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE channels SET resolved_at = ? WHERE id = ?`, at, channelID)
	if err != nil {
		return fmt.Errorf("mark resolve attempted %s: %w", channelID, err)
	}
	return nil
}

// Get returns the channel with the given id, or (nil, nil) if no such
// channel is cached. Unlike List, Get does NOT filter on tracked_at — it
// also finds cache-only rows, since the channel page reads metadata for
// channels the user has never tracked.
func (s *Store) Get(id string) (*Channel, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT id, handle, name, description, avatar_path, banner_path,
       COALESCE(resolved_at, ''), COALESCE(tracked_at, ''), added_at
FROM channels WHERE id = ?`, id)
	var c Channel
	if err := row.Scan(&c.ID, &c.Handle, &c.Name, &c.Description,
		&c.AvatarPath, &c.BannerPath, &c.ResolvedAt, &c.TrackedAt, &c.AddedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get channel %s: %w", id, err)
	}
	return &c, nil
}

// List returns tracked channels joined with their subscription state,
// ordered by name (case-insensitive) then id. filter narrows the result:
// "all" (no filter), "subscribed" (has a subscription row), "tracked"
// (no subscription row), or "autodownload" (subscribed with autodownload
// on — a strict subset of "subscribed", since autodownload lives on the
// subscription row).
func (s *Store) List(filter string) ([]ListItem, error) {
	query := `
SELECT c.id, c.handle, c.name, c.description, c.avatar_path, c.banner_path,
       COALESCE(c.resolved_at, ''), COALESCE(c.tracked_at, ''), c.added_at,
       s.channel_id IS NOT NULL AS subscribed,
       COALESCE(s.autodownload, 0), COALESCE(s.format_override, ''),
       (SELECT count(*) FROM channel_videos cv WHERE cv.channel_id = c.id AND cv.state = 'pending'),
       (SELECT count(*) FROM videos v WHERE v.channel_id = c.id AND v.status = 'downloaded')
FROM channels c LEFT JOIN subscriptions s ON s.channel_id = c.id
WHERE c.tracked_at IS NOT NULL`

	switch filter {
	case "subscribed":
		query += ` AND s.channel_id IS NOT NULL`
	case "tracked":
		query += ` AND s.channel_id IS NULL`
	case "autodownload":
		// s.autodownload is NULL for tracked-but-unsubscribed channels, and
		// `NULL = 1` is not true in SQLite, so those drop out without an
		// extra IS NOT NULL guard.
		query += ` AND s.autodownload = 1`
	case "all", "":
		// no extra clause
	default:
		return nil, fmt.Errorf("list channels: unknown filter %q", filter)
	}
	query += ` ORDER BY c.name COLLATE NOCASE, c.id`

	rows, err := s.db.QueryContext(context.Background(), query)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()

	var out []ListItem
	for rows.Next() {
		var it ListItem
		if err := rows.Scan(
			&it.ID, &it.Handle, &it.Name, &it.Description, &it.AvatarPath, &it.BannerPath,
			&it.ResolvedAt, &it.TrackedAt, &it.AddedAt,
			&it.Subscribed, &it.Autodownload, &it.FormatOverride,
			&it.PendingCount, &it.DownloadedCount,
		); err != nil {
			return nil, fmt.Errorf("scan channel list item: %w", err)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate channel list: %w", err)
	}
	return out, nil
}

// Subscribe subscribes channelID, scheduling its first scan at nextScanAt.
// It is idempotent: if the channel is already subscribed, this is a no-op
// that leaves the existing subscription's config and baseline untouched.
func (s *Store) Subscribe(channelID, nextScanAt string) error {
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO subscriptions (channel_id, next_scan_at) VALUES (?, ?)
ON CONFLICT(channel_id) DO NOTHING`,
		channelID, nextScanAt,
	)
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", channelID, err)
	}
	return nil
}

// Unsubscribe removes channelID's subscription, leaving the channel tracked.
// Returns whether a subscription actually existed.
func (s *Store) Unsubscribe(channelID string) (bool, error) {
	res, err := s.db.ExecContext(context.Background(),
		`DELETE FROM subscriptions WHERE channel_id = ?`, channelID)
	if err != nil {
		return false, fmt.Errorf("unsubscribe %s: %w", channelID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("unsubscribe %s: rows affected: %w", channelID, err)
	}
	return n > 0, nil
}

// UpdateConfig sets a subscription's autodownload flag and format override.
// Returns whether a subscription row actually existed (and was updated) —
// callers use this to distinguish a real config update from a silent no-op
// on a channel that is tracked but not subscribed.
func (s *Store) UpdateConfig(channelID string, autodownload bool, formatOverride string) (bool, error) {
	res, err := s.db.ExecContext(context.Background(),
		`UPDATE subscriptions SET autodownload = ?, format_override = ? WHERE channel_id = ?`,
		autodownload, formatOverride, channelID,
	)
	if err != nil {
		return false, fmt.Errorf("update config %s: %w", channelID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("update config %s: rows affected: %w", channelID, err)
	}
	return n > 0, nil
}

// ClaimDue returns the subscription with the oldest next_scan_at <= now, or
// (nil, nil) if none is due. The scheduler runs on a single goroutine, so a
// plain SELECT is sufficient — no atomic claim (state flip) is needed.
func (s *Store) ClaimDue(now string) (*Subscription, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT channel_id, autodownload, format_override, baselined_at, last_scanned_at, next_scan_at, created_at
FROM subscriptions
WHERE next_scan_at <= ?
ORDER BY next_scan_at ASC
LIMIT 1`, now)

	var sub Subscription
	var baselinedAt, lastScannedAt sql.NullString
	err := row.Scan(
		&sub.ChannelID, &sub.Autodownload, &sub.FormatOverride,
		&baselinedAt, &lastScannedAt, &sub.NextScanAt, &sub.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim due subscription: %w", err)
	}
	sub.BaselinedAt = baselinedAt.String
	sub.LastScannedAt = lastScannedAt.String
	return &sub, nil
}

// GetSubscription returns the subscription row for channelID, or (nil, nil)
// when the channel is not subscribed. ClaimDue is due-based and cannot answer
// "what is this one channel's schedule", which the channel page needs.
func (s *Store) GetSubscription(channelID string) (*Subscription, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT channel_id, autodownload, format_override, baselined_at, last_scanned_at, next_scan_at, created_at
FROM subscriptions WHERE channel_id = ?`, channelID)

	var sub Subscription
	var baselinedAt, lastScannedAt sql.NullString
	err := row.Scan(&sub.ChannelID, &sub.Autodownload, &sub.FormatOverride,
		&baselinedAt, &lastScannedAt, &sub.NextScanAt, &sub.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription %s: %w", channelID, err)
	}
	sub.BaselinedAt = baselinedAt.String
	sub.LastScannedAt = lastScannedAt.String
	return &sub, nil
}

// Stats are the channel page's four header numbers. They count only
// downloaded videos: a queued, errored, or tombstoned row is not on disk, so
// counting it would overstate what the user actually has.
type Stats struct {
	ArchivedCount     int
	RuntimeSeconds    int64
	DiskBytes         int64
	NewestPublishedAt string
}

// Stats computes the header numbers for one channel. channelName is the
// fallback for videos written before channel ids were recorded; pass "" to
// match on channel_id alone.
func (s *Store) Stats(channelID, channelName string) (Stats, error) {
	where := "channel_id = ?"
	args := []any{channelID}
	if channelName != "" {
		where = "(channel_id = ? OR (channel_id = '' AND channel_name = ?))"
		args = []any{channelID, channelName}
	}
	row := s.db.QueryRowContext(context.Background(), `
SELECT count(*),
       COALESCE(sum(duration_seconds), 0),
       COALESCE(sum(filesize_bytes), 0),
       COALESCE(max(published_at), '')
FROM videos WHERE status = 'downloaded' AND `+where, args...)

	var st Stats
	if err := row.Scan(&st.ArchivedCount, &st.RuntimeSeconds, &st.DiskBytes, &st.NewestPublishedAt); err != nil {
		return Stats{}, fmt.Errorf("channel stats %s: %w", channelID, err)
	}
	return st, nil
}

// NameFromVideos returns the channel name recorded on this channel's videos
// and whether the channel has any videos at all. Both matter to the channel
// page: the name is all peeq knows about an untracked channel, and existence
// is what separates "a channel with nothing downloaded yet" from "an id that
// names nothing". Existence is deliberately NOT filtered by status — a video
// still downloading is still a video.
func (s *Store) NameFromVideos(channelID string) (name string, found bool, err error) {
	row := s.db.QueryRowContext(context.Background(),
		`SELECT COALESCE(max(channel_name), ''), count(*) FROM videos WHERE channel_id = ?`,
		channelID)
	var count int
	if err := row.Scan(&name, &count); err != nil {
		return "", false, fmt.Errorf("channel name from videos %s: %w", channelID, err)
	}
	return name, count > 0, nil
}

// MarkScanned records the result of a scan: last_scanned_at and next_scan_at
// are always updated. When baseline is true, baselined_at is stamped with
// lastScannedAt via COALESCE — a first scan sets it, and later scans (which
// also pass baseline=true, e.g. on baseline retries) leave the original
// value untouched. When baseline is false, baselined_at is left alone
// entirely (this scan does not represent a completed baseline).
func (s *Store) MarkScanned(channelID string, baseline bool, lastScannedAt, nextScanAt string) error {
	var err error
	if baseline {
		_, err = s.db.ExecContext(context.Background(), `
UPDATE subscriptions
SET last_scanned_at = ?, next_scan_at = ?, baselined_at = COALESCE(baselined_at, ?)
WHERE channel_id = ?`,
			lastScannedAt, nextScanAt, lastScannedAt, channelID,
		)
	} else {
		_, err = s.db.ExecContext(context.Background(), `
UPDATE subscriptions SET last_scanned_at = ?, next_scan_at = ? WHERE channel_id = ?`,
			lastScannedAt, nextScanAt, channelID,
		)
	}
	if err != nil {
		return fmt.Errorf("mark scanned %s: %w", channelID, err)
	}
	return nil
}

// Backoff pushes a subscription's next_scan_at out (e.g. after a scan
// error), leaving baselined_at and last_scanned_at untouched.
func (s *Store) Backoff(channelID, nextScanAt string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE subscriptions SET next_scan_at = ? WHERE channel_id = ?`,
		nextScanAt, channelID,
	)
	if err != nil {
		return fmt.Errorf("backoff %s: %w", channelID, err)
	}
	return nil
}
