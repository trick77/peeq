// Package channels persists a metadata cache of YouTube channels (the
// channels table) and their optional subscriptions (the subscriptions
// table) from migration 0001_init.sql. A channels row does NOT mean
// "added" — it exists for any channel peeq has ever looked at, including
// ones the user never explicitly added. Being added is added_at IS NOT NULL,
// set via MarkAdded. A subscription row means "subscribed" (the scheduler
// periodically scans it for new videos); a channel can be added without
// being subscribed, but a subscription always implies an added channel
// (subscriptions.channel_id references channels.id).
package channels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Channel mirrors one row of the channels table. A Channel may exist purely
// as a metadata cache entry: AddedAt is empty for a channel the user has
// visited but never added. AvatarPath and BannerPath are relative to the
// media dir (resolve them with media.SafeMediaPath before serving).
// Subscribers is 0 when YouTube did not report a count (it is hidden, or the
// channel has never been resolved) — callers must treat 0 as "unknown", not
// as a real zero. ResolveOk records whether the LAST resolve attempt actually
// succeeded; ResolvedAt is stamped either way, so ResolveOk is the only thing
// that distinguishes fresh metadata from a failed attempt that gave up.
type Channel struct {
	ID          string
	Handle      string
	Name        string
	Description string
	AvatarPath  string
	BannerPath  string
	Subscribers int64
	Verified    bool
	ResolvedAt  string
	ResolveOk   bool
	AddedAt     string
	FirstSeenAt string
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
	// ScanRequestedAt is set while a user-pressed "Scan now" is still waiting
	// for the scan loop to reach this channel, and "" for an ordinary automatic
	// scan. The scanner reads it to decide whether the pass owes the user a
	// receipt even when it found nothing new — see migration 0009.
	ScanRequestedAt string
	// NextMetaRefreshAt is when the weekly metadata refresh is next due, or ""
	// when this channel has no rotation slot at all — a subscription predating
	// migration 0005, or one never scheduled. The column is nullable and is read
	// as "" here, so callers must treat empty as "not scheduled" rather than as
	// an instant.
	NextMetaRefreshAt string
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
	// LastVideoAt is the discovered_at of the channel's most recently seen
	// video, or "" if none has ever been discovered (added-but-unscanned,
	// or genuinely brand new).
	LastVideoAt string
	// Dormant mirrors DormantChannels' predicate for this one channel: it is
	// subscribed, has seen at least one video, that video is older than
	// DormantAfter relative to now, and dormancy has not been dismissed
	// since. Always false for an added-but-unsubscribed channel.
	Dormant bool
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

// HasDownloads reports whether the library holds at least one downloaded
// video from channelID. It is the handler-side twin of hasDownloadsPredicate,
// which List embeds in SQL — the two must stay in step, since together they
// decide which channels are visible and which of those may be acted on.
//
// Deliberately NOT expressible as len(VideoRefs(id)) > 0: VideoRefs returns
// every video row whatever its status, so a channel whose only video is
// queued or failed would pass a check written that way.
func (s *Store) HasDownloads(channelID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM videos WHERE channel_id = ? AND status = 'downloaded')`,
		channelID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("has downloads %s: %w", channelID, err)
	}
	return n == 1, nil
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
// added_at: caching a channel's details must never add or un-add it.
// Empty fields do not overwrite stored values, so a partial refresh cannot
// blank out a name that was already known.
//
// Upsert deliberately does not touch subscriber_count, verified or
// resolve_ok. Its callers (the TubeArchivist import, adding a pasted url)
// know a channel's identity but nothing about its YouTube metadata, and
// writing the zero values they carry would silently clear a resolved
// channel's subscriber count and flip resolve_ok back to "never succeeded".
// Those three columns have exactly one writer each: SaveResolved on success
// and MarkResolveAttempted on failure.
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

// SaveResolved writes the result of a SUCCESSFUL metadata resolve: identity
// plus the YouTube-published facts, with resolve_ok set so the channel page
// can tell this apart from an attempt that failed.
//
// Identity fields keep Upsert's never-blank rule — a resolve that came back
// without a description must not erase the one already stored. Two fields
// break it on purpose:
//
//   - verified is written as-is. A channel that loses its checkmark has to be
//     able to say so, and a never-false column could never report that.
//   - subscriber_count keeps the never-blank rule, because 0 here means
//     "YouTube did not report it" (hidden counts are omitted, not zeroed),
//     and the last real count is better than nothing.
func (s *Store) SaveResolved(c Channel) error {
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO channels (id, handle, name, description, avatar_path, banner_path,
                      subscriber_count, verified, resolved_at, resolve_ok)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), 1)
ON CONFLICT(id) DO UPDATE SET
    handle           = COALESCE(NULLIF(excluded.handle, ''), channels.handle),
    name             = COALESCE(NULLIF(excluded.name, ''), channels.name),
    description      = COALESCE(NULLIF(excluded.description, ''), channels.description),
    avatar_path      = COALESCE(NULLIF(excluded.avatar_path, ''), channels.avatar_path),
    banner_path      = COALESCE(NULLIF(excluded.banner_path, ''), channels.banner_path),
    subscriber_count = COALESCE(NULLIF(excluded.subscriber_count, 0), channels.subscriber_count),
    verified         = excluded.verified,
    resolved_at      = COALESCE(excluded.resolved_at, channels.resolved_at),
    resolve_ok       = 1`,
		c.ID, c.Handle, c.Name, c.Description, c.AvatarPath, c.BannerPath,
		c.Subscribers, c.Verified, c.ResolvedAt,
	)
	if err != nil {
		return fmt.Errorf("save resolved channel %s: %w", c.ID, err)
	}
	return nil
}

// MarkAdded marks a cached channel as explicitly added by the user. It is
// idempotent: re-adding an already-added channel keeps the original
// timestamp rather than resetting "added since".
func (s *Store) MarkAdded(channelID, addedAt string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE channels SET added_at = COALESCE(added_at, ?) WHERE id = ?`,
		addedAt, channelID,
	)
	if err != nil {
		return fmt.Errorf("mark channel added %s: %w", channelID, err)
	}
	return nil
}

// MarkResolveAttempted records that a metadata fetch was tried and FAILED.
// Stamping resolved_at is what stops a permanently unresolvable channel being
// re-fetched from YouTube on every single page visit; clearing resolve_ok is
// what lets the channel page say why it has no artwork instead of showing a
// blank header with a confident "Refreshed <date>" beside it.
//
// Whatever metadata is already stored is left alone: a channel that resolved
// last week and failed today keeps last week's name and avatar, and only the
// freshness claim changes.
func (s *Store) MarkResolveAttempted(channelID, at string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE channels SET resolved_at = ?, resolve_ok = 0 WHERE id = ?`, at, channelID)
	if err != nil {
		return fmt.Errorf("mark resolve attempted %s: %w", channelID, err)
	}
	return nil
}

// Get returns the channel with the given id, or (nil, nil) if no such
// channel is cached. Unlike List, Get does NOT filter on added_at — it
// also finds cache-only rows, since the channel page reads metadata for
// channels the user has never added.
func (s *Store) Get(id string) (*Channel, error) {
	row := s.db.QueryRowContext(context.Background(),
		`SELECT `+channelColumns+` FROM channels c WHERE c.id = ?`, id)
	c, err := scanChannel(row)
	if err != nil {
		return nil, fmt.Errorf("get channel %s: %w", id, err)
	}
	return c, nil
}

// channelColumns is the channels row as every reader of a whole Channel wants
// it: aliased "c" so it drops into a join unchanged, and with the nullable
// columns already COALESCEd to the empty strings the Channel struct uses.
// Shared so a reader that joins channels (the metadata claims) cannot drift
// from Get's column order, which scanChannel depends on.
const channelColumns = `c.id, c.handle, c.name, c.description, c.avatar_path, c.banner_path,
       c.subscriber_count, c.verified,
       COALESCE(c.resolved_at, ''), c.resolve_ok, COALESCE(c.added_at, ''), c.first_seen_at`

// rowScanner is *sql.Row and *sql.Rows both.
type rowScanner interface{ Scan(dest ...any) error }

// scanChannel reads one channelColumns row. A missing row is (nil, nil), not
// an error: "no such channel" is an ordinary answer everywhere this is used.
func scanChannel(row rowScanner) (*Channel, error) {
	var c Channel
	if err := row.Scan(&c.ID, &c.Handle, &c.Name, &c.Description,
		&c.AvatarPath, &c.BannerPath, &c.Subscribers, &c.Verified,
		&c.ResolvedAt, &c.ResolveOk, &c.AddedAt, &c.FirstSeenAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

// hasDownloadsPredicate is true for a channel with at least one downloaded
// video in the library. It is the second way a channel earns a place in the
// list: a video added by URL never adds its channel (see the download
// handler), so without this the channel behind a one-off download would be
// invisible under every filter.
//
// Shared by List, the delete guard and the subscribe guard so all three agree
// on what "peeq holds videos from this channel" means. Note it is deliberately
// narrower than VideoRefs, which returns every video row regardless of status
// — a channel whose only video is queued or failed has downloaded nothing.
const hasDownloadsPredicate = `EXISTS (SELECT 1 FROM videos v
        WHERE v.channel_id = c.id AND v.status = 'downloaded')`

// List returns the channels worth showing joined with their subscription
// state, ordered by name (case-insensitive) then id.
//
// A channel qualifies two ways: the user added it (added_at IS NOT NULL), or
// the library holds a downloaded video from it. The second kind is never
// scanned and was never added — it is here so a one-off video download does
// not leave its channel unreachable.
//
// filter narrows the result:
//
//   - "all"           — everything above
//   - "subscribed"    — has a subscription row
//   - "notsubscribed" — added, but no subscription row
//   - "downloaded"    — never added, present only via a downloaded video
//   - "autodownload"  — subscribed with autodownload on (a strict subset of
//     "subscribed", since autodownload lives on the subscription row)
//
// The last_video_at/dormant columns come from a "lv" CTE (one row per
// channel_id, its MAX(discovered_at)) joined in alongside subscriptions,
// rather than a correlated subquery repeated per output column — SQLite has
// no way to name and reuse a scalar subquery's result within the same
// SELECT list, and duplicating it three times over would both bloat the
// query and risk the copies drifting apart. dormant reuses DormantAfter (the
// same modifier DormantChannels applies) via a bound parameter, so the two
// have exactly one definition of "how long is too long" between them.
func (s *Store) List(filter string) ([]ListItem, error) {
	query := `
WITH lv AS (
  SELECT channel_id, MAX(discovered_at) AS last_video_at
  FROM channel_videos
  GROUP BY channel_id
)
SELECT c.id, c.handle, c.name, c.description, c.avatar_path, c.banner_path,
       COALESCE(c.resolved_at, ''), COALESCE(c.added_at, ''), c.first_seen_at,
       s.channel_id IS NOT NULL AS subscribed,
       COALESCE(s.autodownload, 0), COALESCE(s.format_override, ''),
       (SELECT count(*) FROM channel_videos cv WHERE cv.channel_id = c.id AND cv.state = 'pending'),
       (SELECT count(*) FROM videos v WHERE v.channel_id = c.id AND v.status = 'downloaded'),
       COALESCE(lv.last_video_at, ''),
       s.channel_id IS NOT NULL
         AND lv.last_video_at IS NOT NULL
         AND lv.last_video_at < datetime('now', ?)
         AND (s.dormant_dismissed_at IS NULL OR lv.last_video_at > s.dormant_dismissed_at)
FROM channels c
LEFT JOIN subscriptions s ON s.channel_id = c.id
LEFT JOIN lv ON lv.channel_id = c.id
WHERE (c.added_at IS NOT NULL OR ` + hasDownloadsPredicate + `)`

	switch filter {
	case "subscribed":
		query += ` AND s.channel_id IS NOT NULL`
	case "notsubscribed":
		// The added_at check is load-bearing: without it the download-only
		// rows, which have no subscription either, would land in this pill
		// too — and they are exactly what the "downloaded" pill is for.
		query += ` AND c.added_at IS NOT NULL AND s.channel_id IS NULL`
	case "downloaded":
		// The EXISTS is repeated rather than inherited from the base clause:
		// a cache-only row written by merely visiting a channel page also has
		// added_at IS NULL, and this pill must exclude it on its own terms.
		query += ` AND c.added_at IS NULL AND ` + hasDownloadsPredicate
	case "autodownload":
		// s.autodownload is NULL for added-but-unsubscribed channels, and
		// `NULL = 1` is not true in SQLite, so those drop out without an
		// extra IS NOT NULL guard.
		query += ` AND s.autodownload = 1`
	case "all", "":
		// no extra clause
	default:
		return nil, fmt.Errorf("list channels: unknown filter %q", filter)
	}
	// Sort by what the row actually shows. A channel whose metadata has never
	// resolved has an empty name, and the UI falls back to its handle and then
	// its id — ordering on the raw name would sort every one of those to the
	// very top under a label the list never displays.
	query += ` ORDER BY COALESCE(NULLIF(c.name, ''), NULLIF(c.handle, ''), c.id) COLLATE NOCASE, c.id`

	rows, err := s.db.QueryContext(context.Background(), query, DormantAfter)
	if err != nil {
		return nil, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()

	var out []ListItem
	for rows.Next() {
		var it ListItem
		if err := rows.Scan(
			&it.ID, &it.Handle, &it.Name, &it.Description, &it.AvatarPath, &it.BannerPath,
			&it.ResolvedAt, &it.AddedAt, &it.FirstSeenAt,
			&it.Subscribed, &it.Autodownload, &it.FormatOverride,
			&it.PendingCount, &it.DownloadedCount,
			&it.LastVideoAt, &it.Dormant,
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
//
// The first metadata refresh is scheduled here too, in SQL rather than as a
// parameter: no caller has an opinion about it, and none of them (nor the
// taimport writer interface) should have to grow an argument for a schedule
// they don't care about. It is jittered across the next 7 days with the SAME
// expression migration 0005 uses to spread existing rows, and for the same
// reason: a bulk import (taimport subscribes hundreds of channels in one loop)
// would otherwise stamp them all with an identical due time and reconverge them
// into the very weekly stampede 0005 exists to break up. 10080 = minutes in 7
// days; (random() & 0x7fffffff) masks the sign bit rather than abs()'ing, since
// abs(min-int64) overflows and errors in SQLite.
func (s *Store) Subscribe(channelID, nextScanAt string) error {
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO subscriptions (channel_id, next_scan_at, next_meta_refresh_at)
VALUES (?, ?, datetime('now', '+' || ((random() & 0x7fffffff) % 10080) || ' minutes'))
ON CONFLICT(channel_id) DO NOTHING`,
		channelID, nextScanAt,
	)
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", channelID, err)
	}
	return nil
}

// Unsubscribe removes channelID's subscription, leaving the channel added.
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

// UpdateConfig applies a partial update to a subscription's autodownload
// flag and/or format override in a single atomic statement: a nil argument
// leaves the corresponding column unchanged. It returns the resulting
// (post-update) values via RETURNING, so there is no separate read step for
// a concurrent write to race against. ok is false (with zero values and a
// nil error) when the channel is not subscribed.
//
// The single COALESCE ... RETURNING statement exists so a partial update
// cannot race a concurrent unsubscribe/resubscribe or another partial
// update: the merge happens inside the statement itself, not against a
// value read beforehand. This is a structural property of there being no
// read to race, not something demonstrated by the test suite.
func (s *Store) UpdateConfig(channelID string, autodownload *bool, formatOverride *string) (resultAutodownload bool, resultFormatOverride string, ok bool, err error) {
	row := s.db.QueryRowContext(context.Background(), `
UPDATE subscriptions
   SET autodownload    = COALESCE(?, autodownload),
       format_override = COALESCE(?, format_override)
 WHERE channel_id = ?
RETURNING autodownload, format_override`,
		autodownload, formatOverride, channelID,
	)
	if err := row.Scan(&resultAutodownload, &resultFormatOverride); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, "", false, nil
		}
		return false, "", false, fmt.Errorf("update config %s: %w", channelID, err)
	}
	return resultAutodownload, resultFormatOverride, true, nil
}

// ClaimDue returns the subscription with the oldest next_scan_at <= now, or
// (nil, nil) if none is due. The scheduler runs on a single goroutine, so a
// plain SELECT is sufficient — no atomic claim (state flip) is needed.
func (s *Store) ClaimDue(now string) (*Subscription, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT channel_id, autodownload, format_override, baselined_at, last_scanned_at, next_scan_at, created_at, scan_requested_at
FROM subscriptions
WHERE next_scan_at <= ?
ORDER BY next_scan_at ASC
LIMIT 1`, now)

	var sub Subscription
	var baselinedAt, lastScannedAt, scanRequestedAt sql.NullString
	err := row.Scan(
		&sub.ChannelID, &sub.Autodownload, &sub.FormatOverride,
		&baselinedAt, &lastScannedAt, &sub.NextScanAt, &sub.CreatedAt, &scanRequestedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim due subscription: %w", err)
	}
	sub.BaselinedAt = baselinedAt.String
	sub.LastScannedAt = lastScannedAt.String
	sub.ScanRequestedAt = scanRequestedAt.String
	return &sub, nil
}

// DueChannel is one upcoming scheduled channel task for the Activity page's
// future projection: a channel id + display name (frozen for display) + the
// instant it is due.
type DueChannel struct {
	ChannelID string
	Name      string
	At        string
}

// ScanDueSoon returns up to limit subscriptions ordered by next_scan_at ascending
// (soonest first), joined with the channel name, for the Activity agenda's future
// half. Unlike ClaimDue this is "what is next", not "what is claimable": there is
// deliberately no `<= now` cutoff (an item due in an hour still belongs on the
// agenda) and no scan-quiet predicate — that gates the metadata worker, not what
// the user is shown.
func (s *Store) ScanDueSoon(limit int) ([]DueChannel, error) {
	rows, err := s.db.QueryContext(context.Background(), `
SELECT s.channel_id, COALESCE(c.name, ''), s.next_scan_at
FROM subscriptions s
JOIN channels c ON c.id = s.channel_id
ORDER BY s.next_scan_at ASC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("scan due soon: %w", err)
	}
	return scanDueChannels(rows)
}

// MetaDueSoon is ScanDueSoon for the weekly metadata refresh. next_meta_refresh_at
// is nullable (a subscription predating 0005, or one never scheduled), so NULLs
// are excluded rather than sorted as the earliest.
func (s *Store) MetaDueSoon(limit int) ([]DueChannel, error) {
	rows, err := s.db.QueryContext(context.Background(), `
SELECT s.channel_id, COALESCE(c.name, ''), s.next_meta_refresh_at
FROM subscriptions s
JOIN channels c ON c.id = s.channel_id
WHERE s.next_meta_refresh_at IS NOT NULL
ORDER BY s.next_meta_refresh_at ASC
LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("meta due soon: %w", err)
	}
	return scanDueChannels(rows)
}

func scanDueChannels(rows *sql.Rows) ([]DueChannel, error) {
	defer rows.Close()
	var out []DueChannel
	for rows.Next() {
		var d DueChannel
		if err := rows.Scan(&d.ChannelID, &d.Name, &d.At); err != nil {
			return nil, fmt.Errorf("scan due channel: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetSubscription returns the subscription row for channelID, or (nil, nil)
// when the channel is not subscribed. ClaimDue is due-based and cannot answer
// "what is this one channel's schedule", which the channel page needs.
func (s *Store) GetSubscription(channelID string) (*Subscription, error) {
	row := s.db.QueryRowContext(context.Background(), `
SELECT channel_id, autodownload, format_override, baselined_at, last_scanned_at, next_scan_at, created_at, scan_requested_at, next_meta_refresh_at
FROM subscriptions WHERE channel_id = ?`, channelID)

	var sub Subscription
	var baselinedAt, lastScannedAt, scanRequestedAt, nextMetaRefreshAt sql.NullString
	err := row.Scan(&sub.ChannelID, &sub.Autodownload, &sub.FormatOverride,
		&baselinedAt, &lastScannedAt, &sub.NextScanAt, &sub.CreatedAt, &scanRequestedAt, &nextMetaRefreshAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription %s: %w", channelID, err)
	}
	sub.BaselinedAt = baselinedAt.String
	sub.LastScannedAt = lastScannedAt.String
	sub.ScanRequestedAt = scanRequestedAt.String
	sub.NextMetaRefreshAt = nextMetaRefreshAt.String
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
// page: the name is all peeq knows about a not-added channel, and existence
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
// observedRequest is the scan_requested_at value the caller saw when it claimed
// this subscription ("" for an ordinary automatic pass). Both the schedule and
// the marker are only written when the column STILL holds that value — a
// compare-and-set, not a blind overwrite.
//
// That guard exists for a specific race: a user can press "Scan now" while a
// scan of the same channel is already running. Clearing unconditionally would
// consume their request (no receipt is owed, since this pass never saw it) AND
// push next_scan_at a day out, silently swallowing the click — reproducing the
// very "the button does nothing" bug this all exists to fix. Leaving both
// columns alone instead means the loop re-claims the channel on its next poll
// and the request is honoured properly.
func (s *Store) MarkScanned(channelID string, baseline bool, lastScannedAt, nextScanAt, observedRequest string) error {
	var err error
	if baseline {
		_, err = s.db.ExecContext(context.Background(), `
UPDATE subscriptions
SET last_scanned_at = ?,
    baselined_at = COALESCE(baselined_at, ?),
    next_scan_at = CASE WHEN COALESCE(scan_requested_at, '') = ? THEN ? ELSE next_scan_at END,
    scan_requested_at = CASE WHEN COALESCE(scan_requested_at, '') = ? THEN NULL ELSE scan_requested_at END
WHERE channel_id = ?`,
			lastScannedAt, lastScannedAt, observedRequest, nextScanAt, observedRequest, channelID,
		)
	} else {
		_, err = s.db.ExecContext(context.Background(), `
UPDATE subscriptions
SET last_scanned_at = ?,
    next_scan_at = CASE WHEN COALESCE(scan_requested_at, '') = ? THEN ? ELSE next_scan_at END,
    scan_requested_at = CASE WHEN COALESCE(scan_requested_at, '') = ? THEN NULL ELSE scan_requested_at END
WHERE channel_id = ?`,
			lastScannedAt, observedRequest, nextScanAt, observedRequest, channelID,
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

// RequestScan is "Scan now": it pulls next_scan_at into the past so the scan
// loop claims this channel on its next poll, and marks the channel as having
// someone waiting on the result. It is deliberately not Backoff — that name (and
// its "push the schedule out" contract) says the opposite of what this does, even
// though both write the same column.
//
// scan_requested_at keeps the FIRST request's instant via COALESCE: pressing the
// button twice before the loop arrives is one wait, not two, and the earlier
// instant is the one the user has actually been waiting since.
func (s *Store) RequestScan(channelID, now string) error {
	_, err := s.db.ExecContext(context.Background(), `
UPDATE subscriptions
SET next_scan_at = ?, scan_requested_at = COALESCE(scan_requested_at, ?)
WHERE channel_id = ?`,
		now, now, channelID,
	)
	if err != nil {
		return fmt.Errorf("request scan %s: %w", channelID, err)
	}
	return nil
}

// ClearScanRequest drops the "someone is waiting" marker without touching the
// schedule. The failure path needs this: a scan that errored never reaches
// MarkScanned, and leaving the marker set would make some later automatic pass
// report itself as the answer to a request the user already saw fail.
//
// observedRequest is compared the same way MarkScanned compares it, and for the
// same reason: a request that arrived DURING the pass was never answered by it,
// so this must not consume it.
func (s *Store) ClearScanRequest(channelID, observedRequest string) error {
	_, err := s.db.ExecContext(context.Background(), `
UPDATE subscriptions SET scan_requested_at = NULL
WHERE channel_id = ? AND COALESCE(scan_requested_at, '') = ?`,
		channelID, observedRequest,
	)
	if err != nil {
		return fmt.Errorf("clear scan request %s: %w", channelID, err)
	}
	return nil
}
