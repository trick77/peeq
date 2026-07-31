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
	// UnavailableReason is why State is StateUnavailable — one of the
	// ytdlp.TerminalError reasons (members, age, geo, private, deleted). It is
	// "" in every other state; SetState clears it on the way out.
	UnavailableReason string
	// UnavailableAt is when the row was last confirmed unavailable, "" in every
	// other state. It rate-limits the per-video availability probe the scan
	// runs when the channel listing says nothing — see StateUnavailable. It is
	// not a re-offer timer: nothing moves this row without an answer.
	UnavailableAt string
	// SummaryStatus is the videos row's summary_status for this video, and
	// AutoSummary whether its channel is opted in to being read at all. Both
	// are populated ONLY by the ListPending queries; Get leaves them zero,
	// because a ledger row on its own knows nothing about either.
	//
	// The Inbox card needs both, and neither alone is enough. A video that has
	// not been reached yet has no videos row, so its status is "" — which is
	// also what a video on an opted-out channel looks like. The flag is what
	// separates "not read yet" from "never will be", and therefore whether the
	// card shows a progress marker or nothing at all.
	SummaryStatus string
	AutoSummary   bool
	// HasSubtitles is whether captions are already on disk for this video.
	//
	// It is not implied by SummaryStatus. 'no_transcript' covers two different
	// videos: one YouTube has no captions for at all, and one whose captions
	// turned out to be music and so produced no summary. The second has a
	// readable transcript and the first has nothing, and only this flag tells
	// the Inbox card which of the two it is holding.
	HasSubtitles bool
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
const selectColumns = `video_id, channel_id, title, duration_seconds, url, thumbnail_url, state, discovered_at, decided_at, published_at, unavailable_reason, unavailable_at`

// pendingColumns is selectColumns aliased to the channel_videos table (cv), for
// the ListPending JOIN where an unqualified column list would be ambiguous.
const pendingColumns = `cv.video_id, cv.channel_id, cv.title, cv.duration_seconds, cv.url, cv.thumbnail_url, cv.state, cv.discovered_at, cv.decided_at, cv.published_at, cv.unavailable_reason, cv.unavailable_at`

// scanRow scans one channel_videos row (in selectColumns order) into an
// Entry, mapping NULL duration_seconds/decided_at to 0/"".
func scanRow(sc interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var duration sql.NullInt64
	var decidedAt, publishedAt, unavailableAt sql.NullString
	if err := sc.Scan(
		&e.VideoID, &e.ChannelID, &e.Title, &duration, &e.URL, &e.ThumbnailURL,
		&e.State, &e.DiscoveredAt, &decidedAt, &publishedAt, &e.UnavailableReason,
		&unavailableAt,
	); err != nil {
		return Entry{}, err
	}
	e.DurationSeconds = int(duration.Int64)
	e.DecidedAt = decidedAt.String
	e.PublishedAt = publishedAt.String
	e.UnavailableAt = unavailableAt.String
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

// Insert adds a new ledger row for e. The unavailable_at stamp is derived in
// SQL from the state being written rather than taken from the caller, so a row
// born StateUnavailable (a scan that saw the gate badge before the video ever
// reached the inbox) starts its re-offer clock on exactly the same footing as
// one parked later by SetUnavailable. e.UnavailableAt is read-only.
//
// e.State must be set by the caller
// (e.g. "seen" for a video below the duration floor, "pending" for one
// awaiting a decision). DurationSeconds of 0 is stored as-is; the column
// allows NULL for genuinely unknown durations, but a caller-supplied 0 is
// indistinguishable from unknown and is treated the same way by consumers.
func (s *Store) Insert(e Entry) error {
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO channel_videos (video_id, channel_id, title, duration_seconds, url, thumbnail_url, state, published_at, unavailable_reason, unavailable_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CASE WHEN ? = 'unavailable' THEN datetime('now') END)`,
		e.VideoID, e.ChannelID, e.Title, e.DurationSeconds, e.URL, e.ThumbnailURL, e.State,
		nullIfEmpty(e.PublishedAt), e.UnavailableReason, e.State,
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
//
// It also clears unavailable_reason, which is only meaningful alongside
// StateUnavailable. Leaving a stale reason on a row that has since become
// queued would make the UI label a perfectly ordinary download "members only".
// Use SetUnavailable to move a row the other way; it is the only writer of
// that column.
func (s *Store) SetState(videoID, state string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE channel_videos SET state = ?, unavailable_reason = '', unavailable_at = NULL, decided_at = datetime('now') WHERE video_id = ?`,
		state, videoID,
	)
	if err != nil {
		return fmt.Errorf("set state %s: %w", videoID, err)
	}
	return nil
}

// SetUnavailable parks a row in StateUnavailable with the ytdlp reason that
// put it there. This is the one transition that is not a user decision, so it
// deliberately does NOT stamp decided_at: the column means "when the user
// decided", and a gate peeq discovered on its own is not a decision. Keeping
// it NULL also preserves the original decided_at when a queued item lands
// here, so a later return to 'pending' does not look like it was decided
// twice.
//
// unavailable_at is always restamped, including when the row was already
// unavailable, because every caller reaches here holding FRESH evidence the
// gate still stands — a badge in the listing, a probe that answered, or a
// download that hit the wall. Restamping is what spaces the next (costly)
// per-video probe a full window out; see Entry.UnavailableAt.
//
// The one thing that must NOT reach here is a check that failed to produce an
// answer. Restamping on those would let a run of network trouble silently
// push the next real re-check out by a window each time, which is the
// slow-motion version of burying the video.
func (s *Store) SetUnavailable(videoID, reason string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE channel_videos SET state = ?, unavailable_reason = ?, unavailable_at = datetime('now') WHERE video_id = ?`,
		StateUnavailable, reason, videoID,
	)
	if err != nil {
		return fmt.Errorf("set unavailable %s: %w", videoID, err)
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
		`SELECT `+pendingColumns+`, COALESCE(c.name, '') AS channel_name,
       COALESCE(v.summary_status, ''), COALESCE(c.auto_summary, 0),
       COALESCE(v.subtitle_path, '') <> ''
FROM channel_videos cv
LEFT JOIN channels c ON c.id = cv.channel_id
LEFT JOIN videos v ON v.id = cv.video_id
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
		`SELECT `+pendingColumns+`, COALESCE(c.name, '') AS channel_name,
       COALESCE(v.summary_status, ''), COALESCE(c.auto_summary, 0),
       COALESCE(v.subtitle_path, '') <> ''
FROM channel_videos cv
LEFT JOIN channels c ON c.id = cv.channel_id
LEFT JOIN videos v ON v.id = cv.video_id
WHERE cv.state = 'pending' AND cv.channel_id = ?
ORDER BY COALESCE(cv.published_at, date(cv.discovered_at)) DESC, cv.discovered_at DESC, cv.video_id DESC`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list pending for channel %s: %w", channelID, err)
	}
	defer rows.Close()
	return scanPendingEntries(rows)
}

// ListUnavailableForChannel returns every row for a channel parked as
// unavailable, oldest re-check first so a bounded per-pass probe budget is
// spent on the videos that have waited longest rather than on the same few
// every time.
//
// This exists because the scan's re-check otherwise only ever sees videos the
// channel LISTING returned, and the listing is capped (defaultListSize). A
// video parked in error — a gate misread, or a stale yt-dlp reporting a
// working video as unavailable — becomes permanently unrecoverable the moment
// it falls out of that window, since parking also discards its videos row.
// Reading the parked rows directly is what unties recovery from recency.
//
// No channels JOIN: the caller probes by URL and never displays these.
func (s *Store) ListUnavailableForChannel(channelID string) ([]Entry, error) {
	rows, err := s.db.QueryContext(context.Background(),
		`SELECT `+selectColumns+`
FROM channel_videos
WHERE state = ? AND channel_id = ?
ORDER BY COALESCE(unavailable_at, discovered_at) ASC, video_id ASC`,
		StateUnavailable, channelID)
	if err != nil {
		return nil, fmt.Errorf("list unavailable for channel %s: %w", channelID, err)
	}
	defer rows.Close()

	var out []Entry
	for rows.Next() {
		e, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scan unavailable channel video: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unavailable channel videos: %w", err)
	}
	return out, nil
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
// field order, minus ChannelName) followed by the joined channel_name, the
// video's summary_status, its channel's auto_summary flag and whether captions
// are on disk.
func scanPendingRow(sc interface{ Scan(...any) error }) (Entry, error) {
	var e Entry
	var duration sql.NullInt64
	var decidedAt, publishedAt, unavailableAt sql.NullString
	if err := sc.Scan(
		&e.VideoID, &e.ChannelID, &e.Title, &duration, &e.URL, &e.ThumbnailURL,
		&e.State, &e.DiscoveredAt, &decidedAt, &publishedAt, &e.UnavailableReason,
		&unavailableAt, &e.ChannelName, &e.SummaryStatus, &e.AutoSummary,
		&e.HasSubtitles,
	); err != nil {
		return Entry{}, err
	}
	e.DurationSeconds = int(duration.Int64)
	e.DecidedAt = decidedAt.String
	e.PublishedAt = publishedAt.String
	e.UnavailableAt = unavailableAt.String
	return e, nil
}
