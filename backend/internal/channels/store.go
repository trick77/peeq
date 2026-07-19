// Package channels persists tracked YouTube channels (the channels table)
// and their optional subscriptions (the subscriptions table) from migration
// 0001_init.sql. A channel row means "tracked" (its identity is known,
// videos may reference it); a subscription row means "subscribed" (the
// scheduler periodically scans it for new videos). A channel can be tracked
// without being subscribed, but a subscription always implies a tracked
// channel (subscriptions.channel_id references channels.id).
package channels

import (
	"context"
	"database/sql"
	"fmt"
)

// Channel mirrors one row of the channels table.
type Channel struct {
	ID         string
	Handle     string
	Name       string
	AvatarPath string
	AddedAt    string
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
}

// VideoRefs returns a VideoRef for every videos row belonging to channelID.
// Callers read these before DeleteCascade so the media/thumbnail paths (lost
// once the rows are deleted) are still available for unlinking the files.
func (s *Store) VideoRefs(channelID string) ([]VideoRef, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT id, media_path, thumbnail_path FROM videos WHERE channel_id = ?`, channelID)
	if err != nil {
		return nil, fmt.Errorf("video refs: %w", err)
	}
	defer rows.Close()
	var out []VideoRef
	for rows.Next() {
		var r VideoRef
		if err := rows.Scan(&r.VideoID, &r.MediaPath, &r.ThumbnailPath); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteCascade removes a channel and everything belonging to it in one
// transaction. vec_chunks (a vec0 virtual table) cannot ride an FK cascade or
// trigger, so its rows for this channel's videos are purged explicitly FIRST,
// by rowid (== transcript_chunks.id) — before the videos delete cascades away
// the transcript_chunks rows that rowid comes from. videos itself has no
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

	// vec_chunks (vec0) can't ride an FK cascade, so purge its rows for this
	// channel's videos explicitly, by rowid, BEFORE the videos delete cascades
	// their transcript_chunks away (which would strand the vec rows forever).
	rows, err := tx.Query(`
SELECT tc.id FROM transcript_chunks tc
JOIN videos v ON v.id = tc.video_id
WHERE v.channel_id = ?`, channelID)
	if err != nil {
		return fmt.Errorf("select vec_chunks rowids for channel: %w", err)
	}
	var vecIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("scan vec_chunks rowid: %w", err)
		}
		vecIDs = append(vecIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate vec_chunks rowids for channel: %w", err)
	}
	for _, id := range vecIDs {
		if _, err := tx.Exec(`DELETE FROM vec_chunks WHERE rowid = ?`, id); err != nil {
			return fmt.Errorf("delete vec_chunks row %d: %w", id, err)
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

// Upsert tracks a channel: inserts it if new, or refreshes handle/name if it
// already exists. avatar_path and added_at are left untouched on conflict.
func (s *Store) Upsert(c Channel) error {
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO channels (id, handle, name) VALUES (?, ?, ?)
ON CONFLICT(id) DO UPDATE SET handle = excluded.handle, name = excluded.name`,
		c.ID, c.Handle, c.Name,
	)
	if err != nil {
		return fmt.Errorf("upsert channel %s: %w", c.ID, err)
	}
	return nil
}

// Get returns the channel with the given id, or (nil, nil) if it is not
// tracked.
func (s *Store) Get(id string) (*Channel, error) {
	row := s.db.QueryRowContext(context.Background(),
		`SELECT id, handle, name, avatar_path, added_at FROM channels WHERE id = ?`, id)
	var c Channel
	if err := row.Scan(&c.ID, &c.Handle, &c.Name, &c.AvatarPath, &c.AddedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get channel %s: %w", id, err)
	}
	return &c, nil
}

// List returns tracked channels joined with their subscription state,
// ordered by name (case-insensitive) then id. filter narrows the result:
// "all" (no filter), "subscribed" (has a subscription row), or "tracked"
// (no subscription row).
func (s *Store) List(filter string) ([]ListItem, error) {
	query := `
SELECT c.id, c.handle, c.name, c.avatar_path, c.added_at,
       s.channel_id IS NOT NULL AS subscribed,
       COALESCE(s.autodownload, 0), COALESCE(s.format_override, ''),
       (SELECT count(*) FROM channel_videos cv WHERE cv.channel_id = c.id AND cv.state = 'pending'),
       (SELECT count(*) FROM videos v WHERE v.channel_id = c.id AND v.status = 'downloaded')
FROM channels c LEFT JOIN subscriptions s ON s.channel_id = c.id`

	switch filter {
	case "subscribed":
		query += ` WHERE s.channel_id IS NOT NULL`
	case "tracked":
		query += ` WHERE s.channel_id IS NULL`
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
			&it.ID, &it.Handle, &it.Name, &it.AvatarPath, &it.AddedAt,
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
