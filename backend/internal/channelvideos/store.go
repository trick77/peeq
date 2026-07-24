// Package channelvideos persists the per-channel scan ledger (the
// channel_videos table from migration 0001_init.sql). Every video a
// subscription scan observes gets exactly one row here, keyed by video_id:
// this is both the dedup set (has this video already been seen by a scan?)
// and the pending list (videos awaiting a keep/ignore decision).
package channelvideos

import (
	"context"
	"database/sql"
	"fmt"
)

// Entry mirrors one row of the channel_videos table. DecidedAt is an empty
// string when the underlying column is NULL (no decision made yet).
// DurationSeconds is 0 when unknown (NULL in the DB); callers filtering on
// duration should fail open on 0.
type Entry struct {
	VideoID         string
	ChannelID       string
	ChannelName     string
	Title           string
	DurationSeconds int
	URL             string
	ThumbnailURL    string
	State           string
	DiscoveredAt    string
	DecidedAt       string
	// PublishedAt is YYYY-MM-DD, or "" when not known. It is yt-dlp's
	// APPROXIMATE tab date, not the exact upload_date on videos.published_at,
	// and rows written before migration 0008 carry "" until a scan heals them.
	PublishedAt string
}

// Store persists the channel_videos scan ledger.
type Store struct {
	db *sql.DB
}

// New returns a channelvideos store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// selectColumns is the shared column list for every row read, in Entry field
// order, so scanRow can be reused by Get and ListPending.
const selectColumns = `video_id, channel_id, title, duration_seconds, url, thumbnail_url, state, discovered_at, decided_at, published_at`

// pendingColumns is selectColumns aliased to the channel_videos table (cv), for
// the ListPending JOIN where an unqualified column list would be ambiguous.
const pendingColumns = `cv.video_id, cv.channel_id, cv.title, cv.duration_seconds, cv.url, cv.thumbnail_url, cv.state, cv.discovered_at, cv.decided_at, cv.published_at`

// scanRow scans one channel_videos row (in selectColumns order) into an
// Entry, mapping NULL duration_seconds/decided_at to 0/"".
func scanRow(sc interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var duration sql.NullInt64
	var decidedAt, publishedAt sql.NullString
	if err := sc.Scan(
		&e.VideoID, &e.ChannelID, &e.Title, &duration, &e.URL, &e.ThumbnailURL,
		&e.State, &e.DiscoveredAt, &decidedAt, &publishedAt,
	); err != nil {
		return Entry{}, err
	}
	e.DurationSeconds = int(duration.Int64)
	e.DecidedAt = decidedAt.String
	e.PublishedAt = publishedAt.String
	return e, nil
}

// Exists reports whether videoID is already present in the ledger (the
// dedup check a scan uses before deciding whether a video is new).
func (s *Store) Exists(videoID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(context.Background(),
		`SELECT 1 FROM channel_videos WHERE video_id = ?`, videoID,
	).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check exists %s: %w", videoID, err)
	}
	return true, nil
}

// Insert adds a new ledger row for e. e.State must be set by the caller
// (e.g. "seen" for a video below the duration floor, "pending" for one
// awaiting a decision). DurationSeconds of 0 is stored as-is; the column
// allows NULL for genuinely unknown durations, but a caller-supplied 0 is
// indistinguishable from unknown and is treated the same way by consumers.
func (s *Store) Insert(e Entry) error {
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO channel_videos (video_id, channel_id, title, duration_seconds, url, thumbnail_url, state, published_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.VideoID, e.ChannelID, e.Title, e.DurationSeconds, e.URL, e.ThumbnailURL, e.State,
		nullIfEmpty(e.PublishedAt),
	)
	if err != nil {
		return fmt.Errorf("insert channel video %s: %w", e.VideoID, err)
	}
	return nil
}

// nullIfEmpty maps "" to a SQL NULL, keeping "not known" distinct from a
// stored empty string for the nullable published_at column.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SetPublishedAt fills in a row's approximate publish date, but ONLY while it
// is still NULL. Rows written before migration 0008 — every item sitting in
// the inbox when this shipped — have no date, and a scan skips them entirely
// as already-seen, so without this they would stay dateless forever. The
// WHERE published_at IS NULL guard is what makes it safe to call on every
// scan pass: it heals the backlog once and then costs a no-op update, and it
// can never overwrite a date with a later, coarser approximation of itself.
//
// An empty date is a no-op rather than an error: a listing that carried no
// timestamp leaves the row NULL and eligible for the next pass.
func (s *Store) SetPublishedAt(videoID, publishedAt string) error {
	if publishedAt == "" {
		return nil
	}
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE channel_videos SET published_at = ? WHERE video_id = ? AND published_at IS NULL`,
		publishedAt, videoID,
	)
	if err != nil {
		return fmt.Errorf("set published_at %s: %w", videoID, err)
	}
	return nil
}

// SetState updates a ledger row's state and stamps decided_at with the
// current time (this is how a video transitions out of "pending": ignored,
// queued, or back to seen).
func (s *Store) SetState(videoID, state string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE channel_videos SET state = ?, decided_at = datetime('now') WHERE video_id = ?`,
		state, videoID,
	)
	if err != nil {
		return fmt.Errorf("set state %s: %w", videoID, err)
	}
	return nil
}

// Get returns the ledger entry for videoID, or (nil, nil) if it is absent.
func (s *Store) Get(videoID string) (*Entry, error) {
	row := s.db.QueryRowContext(context.Background(),
		`SELECT `+selectColumns+` FROM channel_videos WHERE video_id = ?`, videoID)
	e, err := scanRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get channel video %s: %w", videoID, err)
	}
	return &e, nil
}

// ListPending returns every entry in state 'pending', newest discovered
// first (ties broken by video_id descending for determinism). It LEFT JOINs
// channels so each entry carries the human-readable channel name (empty when
// the channel row is somehow absent) — the Pending UI shows the name rather
// than the raw UCID. The join keeps this a single query (no N+1).
func (s *Store) ListPending() ([]Entry, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT `+pendingColumns+`, COALESCE(c.name, '') AS channel_name
FROM channel_videos cv
LEFT JOIN channels c ON c.id = cv.channel_id
WHERE cv.state = 'pending'
ORDER BY COALESCE(cv.published_at, date(cv.discovered_at)) DESC, cv.discovered_at DESC, cv.video_id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list pending channel videos: %w", err)
	}
	defer rows.Close()
	return scanPendingEntries(rows)
}

// ListPendingForChannel is ListPending scoped to one channel. The
// idx_channel_videos_channel index already supports this predicate.
func (s *Store) ListPendingForChannel(channelID string) ([]Entry, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT `+pendingColumns+`, COALESCE(c.name, '') AS channel_name
FROM channel_videos cv
LEFT JOIN channels c ON c.id = cv.channel_id
WHERE cv.state = 'pending' AND cv.channel_id = ?
ORDER BY COALESCE(cv.published_at, date(cv.discovered_at)) DESC, cv.discovered_at DESC, cv.video_id DESC`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list pending for channel %s: %w", channelID, err)
	}
	defer rows.Close()
	return scanPendingEntries(rows)
}

// scanPendingEntries reads every remaining row from a ListPending-shaped
// query (pendingColumns + joined channel_name) into a slice. Shared by
// ListPending and ListPendingForChannel so the two lists can never diverge.
func scanPendingEntries(rows *sql.Rows) ([]Entry, error) {
	var out []Entry
	for rows.Next() {
		e, err := scanPendingRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan channel video: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending channel videos: %w", err)
	}
	return out, nil
}

// scanPendingRow scans one ListPending row: the selectColumns set (in Entry
// field order, minus ChannelName) followed by the joined channel_name.
func scanPendingRow(sc interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var duration sql.NullInt64
	var decidedAt, publishedAt sql.NullString
	if err := sc.Scan(
		&e.VideoID, &e.ChannelID, &e.Title, &duration, &e.URL, &e.ThumbnailURL,
		&e.State, &e.DiscoveredAt, &decidedAt, &publishedAt, &e.ChannelName,
	); err != nil {
		return Entry{}, err
	}
	e.DurationSeconds = int(duration.Int64)
	e.DecidedAt = decidedAt.String
	e.PublishedAt = publishedAt.String
	return e, nil
}
