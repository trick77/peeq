// Package videos persists the videos table (migration 0001_init.sql): one
// row per tracked YouTube video, holding its metadata, download state, and
// the watched/favorite/tombstone lifecycle.
//
// Watched semantics (Task 11, decided product rules): a video becomes
// watched automatically when its resume position reaches >= 90% of the
// duration (SetResume), or manually (SetWatched(id, true)). Re-watching
// never resets watched_at once set — no "life extension" of the retention
// clock. Manual un-watch (SetWatched(id, false)) clears watched, watched_at,
// AND resume_position_seconds, rescuing the video from the retention sweep
// and making that rescue sticky (a stale near-end resume ping can't
// immediately re-mark it watched). Tombstone keeps
// the row (for watched history and a future summary/transcript) but clears
// media_path and marks status='tombstoned'; the caller is responsible for
// unlinking the actual media/thumbnail files from disk first.
package videos

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Video mirrors the columns of the videos table this package reads or
// writes. Fields left at their zero value on Upsert fall back to the
// column defaults on insert.
type Video struct {
	ID                    string
	URL                   string
	Title                 string
	ChannelID             string
	ChannelName           string
	DurationSeconds       int64
	PublishedAt           string
	Description           string
	ThumbnailPath         string
	MediaPath             string
	FilesizeBytes         int64
	FormatUsed            string
	RequestedFormat       string
	Availability          string
	Status                string
	ErrorMessage          string
	SponsorblockSegments  string
	Watched               bool
	WatchedAt             string
	ResumePositionSeconds float64
	Favorite              bool
	FavoritedAt           string
	CreatedAt             string
	DownloadedAt          string
	AudioLanguage         string
	SubtitlePath          string
	Summary               string
	Chapters              string
	KeyPoints             string
	SummaryStatus         string
	SummaryError          string
	EmbedModel            string
	EmbedDim              int
	Category              string
}

// watchedThreshold is the fraction of a video's duration that, once
// reached via SetResume, auto-marks it watched.
const watchedThreshold = 0.9

// DownloadedResult is the outcome of a successful download, mapped from
// ytdlp.Result by the worker. SponsorblockSegments is the JSON text stored
// verbatim in the sponsorblock_segments column.
type DownloadedResult struct {
	MediaPath            string
	ThumbnailPath        string
	FilesizeBytes        int64
	FormatUsed           string
	SponsorblockSegments string
	SubtitleRelPath      string
	AudioLanguage        string
	ChaptersJSON         string
	// PublishedAt is the release date (YYYY-MM-DD) yt-dlp reported in the
	// download's own info.json, or "" when it reported none.
	//
	// SetDownloaded is the only place a channel-driven download can pick one
	// up: scan.Scheduler.enqueueAuto seeds its videos row from a flat listing
	// that carries no release date, and nothing else would ever set one — the
	// library would sort those videos by download date forever. An empty value
	// leaves whatever is already stored, so a date the richer Metadata path
	// wrote is never clobbered.
	PublishedAt string
}

// Store persists video rows.
type Store struct {
	db *sql.DB
}

// New returns a videos store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// Upsert inserts a video row or, if it already exists, refreshes only its
// metadata columns. It deliberately does NOT touch download-owned columns
// (status, media_path, filesize_bytes, format_used, downloaded_at,
// sponsorblock_segments) or requested_format: re-running metadata for an
// already-downloaded video must not wipe its downloaded state, and a
// metadata-only re-sync must not clobber a per-channel format override set
// via SetRequestedFormat. requested_format IS included in the INSERT
// column list, so a fresh row still carries the value the caller passed
// (e.g. an initial channel scan seeding both metadata and the override in
// one Upsert); it is simply excluded from the ON CONFLICT UPDATE SET.
func (s *Store) Upsert(v Video) error {
	availability := v.Availability
	if availability == "" {
		availability = "unknown"
	}
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO videos (id, url, title, channel_id, channel_name, duration_seconds,
	published_at, description, thumbnail_path, availability, requested_format)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	url             = excluded.url,
	title           = excluded.title,
	channel_id      = excluded.channel_id,
	channel_name    = excluded.channel_name,
	duration_seconds = excluded.duration_seconds,
	published_at    = excluded.published_at,
	description     = excluded.description,
	thumbnail_path  = excluded.thumbnail_path,
	availability    = excluded.availability`,
		v.ID, v.URL, v.Title, v.ChannelID, v.ChannelName, nullInt(v.DurationSeconds),
		nullStr(v.PublishedAt), v.Description, v.ThumbnailPath, availability, v.RequestedFormat,
	)
	if err != nil {
		return fmt.Errorf("upsert video %s: %w", v.ID, err)
	}
	return nil
}

// videoColumns is the column list shared by Get and List, in the order
// scanRow expects.
const videoColumns = `id, url, title, channel_id, channel_name, duration_seconds, published_at,
	description, thumbnail_path, media_path, filesize_bytes, format_used, requested_format,
	availability, status, error_message, sponsorblock_segments,
	watched, watched_at, resume_position_seconds, favorite, favorited_at,
	created_at, downloaded_at,
	audio_language, subtitle_path, summary, chapters, key_points, summary_status, summary_error, embed_model, embed_dim, category`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanVideo scans one row in the videoColumns order into a Video.
func scanVideo(rs rowScanner) (Video, error) {
	var v Video
	var duration, filesize sql.NullInt64
	var publishedAt, watchedAt, favoritedAt, downloadedAt sql.NullString
	var watched, favorite int
	err := rs.Scan(
		&v.ID, &v.URL, &v.Title, &v.ChannelID, &v.ChannelName, &duration, &publishedAt,
		&v.Description, &v.ThumbnailPath, &v.MediaPath, &filesize, &v.FormatUsed, &v.RequestedFormat,
		&v.Availability, &v.Status, &v.ErrorMessage, &v.SponsorblockSegments,
		&watched, &watchedAt, &v.ResumePositionSeconds, &favorite, &favoritedAt,
		&v.CreatedAt, &downloadedAt,
		&v.AudioLanguage, &v.SubtitlePath, &v.Summary, &v.Chapters, &v.KeyPoints,
		&v.SummaryStatus, &v.SummaryError, &v.EmbedModel, &v.EmbedDim, &v.Category,
	)
	if err != nil {
		return Video{}, err
	}
	v.DurationSeconds = duration.Int64
	v.FilesizeBytes = filesize.Int64
	v.PublishedAt = publishedAt.String
	v.Watched = watched != 0
	v.WatchedAt = watchedAt.String
	v.Favorite = favorite != 0
	v.FavoritedAt = favoritedAt.String
	v.DownloadedAt = downloadedAt.String
	return v, nil
}

// Get returns the video row for id, or (nil, nil) if there is none.
func (s *Store) Get(id string) (*Video, error) {
	row := s.db.QueryRowContext(context.Background(),
		"SELECT "+videoColumns+" FROM videos WHERE id = ?", id,
	)
	v, err := scanVideo(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get video %s: %w", id, err)
	}
	return &v, nil
}

// ListOptions narrows videos.Store.List. Every field is optional; the zero
// value means "every video, newest first" — the pre-existing behavior.
type ListOptions struct {
	// Filter is the status dimension: unwatched|watched|favorites|downloading.
	// Anything else (including "" and "all") means no status constraint.
	Filter string
	// Category is the classification dimension, ANDed with Filter.
	Category string
	// Query matches case-insensitively against the title as a substring.
	Query string
	// Sort is newest|oldest|longest|title. Anything else means newest.
	Sort string
	// ChannelID scopes to one channel. ChannelName is the fallback for rows
	// written before channel ids were recorded, and is only consulted when
	// ChannelID is also set.
	ChannelID   string
	ChannelName string
}

// sortClauses maps the accepted Sort values to ORDER BY fragments. Sort is
// interpolated into SQL, so it must only ever come from this map — never
// from the caller's string.
//
// newest/oldest rank by RELEASE date (published_at), not by when peeq added
// the row: an old video downloaded this morning belongs where it was
// published, not at the top of the library. published_at is NULL when yt-dlp
// reports no upload_date (some live streams and premieres), so those rows
// fall back to created_at and stay interleaved rather than sinking to one
// end forever. date() normalizes the fallback — published_at is 'YYYY-MM-DD'
// while created_at is 'YYYY-MM-DD HH:MM:SS', and comparing the two shapes
// lexically would sort a same-day date-only value before the datetime one.
var sortClauses = map[string]string{
	"newest":  "COALESCE(published_at, date(created_at)) DESC, created_at DESC, id DESC",
	"oldest":  "COALESCE(published_at, date(created_at)) ASC, created_at ASC, id ASC",
	"longest": "COALESCE(duration_seconds, 0) DESC, id DESC",
	"title":   "title COLLATE NOCASE ASC, id ASC",
}

// escapeLike escapes the three characters LIKE treats specially so a user
// typing "100%" searches for a literal percent sign rather than matching
// every row. Pairs with the ESCAPE '\' clause in the query below.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// List returns videos matching opts, ordered by opts.Sort. The status,
// category, search, and channel dimensions are orthogonal: all that are set
// apply together.
//   - Filter: "unwatched" (downloaded and not watched), "watched", "favorites",
//     "downloading" (queued or downloading), or anything else/"" (no
//     constraint, tombstoned included)
//   - Category: empty/"all"/unknown ⇒ no category constraint
//   - Query: case-insensitive substring match against title
//   - Sort: newest|oldest|longest|title; anything else falls back to newest.
//     newest/oldest order by release date (published_at), falling back to
//     created_at for rows with no known release date
//   - ChannelID/ChannelName: scopes to one channel, matching channel_id or,
//     for rows written before channel ids were recorded, an exact
//     channel_name match on rows with an empty channel_id
func (s *Store) List(opts ListOptions) ([]Video, error) {
	conds := []string{}
	args := []any{}
	switch opts.Filter {
	case "unwatched":
		conds = append(conds, "status = 'downloaded' AND watched = 0")
	case "watched":
		conds = append(conds, "watched = 1")
	case "favorites":
		conds = append(conds, "favorite = 1")
	case "downloading":
		conds = append(conds, "status IN ('queued', 'downloading')")
	}
	if opts.Category != "" && opts.Category != "all" && ValidCategory(opts.Category) {
		conds = append(conds, "category = ?")
		args = append(args, opts.Category)
	}
	if q := strings.TrimSpace(opts.Query); q != "" {
		conds = append(conds, `title LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(q)+"%")
	}
	if opts.ChannelID != "" {
		if opts.ChannelName != "" {
			conds = append(conds, "(channel_id = ? OR (channel_id = '' AND channel_name = ?))")
			args = append(args, opts.ChannelID, opts.ChannelName)
		} else {
			conds = append(conds, "channel_id = ?")
			args = append(args, opts.ChannelID)
		}
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	order, ok := sortClauses[opts.Sort]
	if !ok {
		order = sortClauses["newest"]
	}

	rows, err := s.db.QueryContext(context.Background(),
		"SELECT "+videoColumns+" FROM videos "+where+" ORDER BY "+order,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("list videos (%+v): %w", opts, err)
	}
	defer rows.Close()

	out := []Video{}
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, fmt.Errorf("list videos (%+v): %w", opts, err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list videos (%+v): %w", opts, err)
	}
	return out, nil
}

// SetStatus sets a video's status and error_message. Used by the worker to
// mark a video 'downloading' when its job is claimed and 'error' (with a
// message) when the download fails terminally.
func (s *Store) SetStatus(id, status, errMsg string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET status = ?, error_message = ? WHERE id = ?`,
		status, errMsg, id,
	)
	if err != nil {
		return fmt.Errorf("set video %s status: %w", id, err)
	}
	return nil
}

// SetRequestedFormat overrides the yt-dlp format string used for this
// video's next download (empty = use the global preset). Set by the scan
// scheduler from a channel's format_override before enqueueing.
func (s *Store) SetRequestedFormat(id, format string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET requested_format = ? WHERE id = ?`, format, id)
	if err != nil {
		return fmt.Errorf("set requested_format %s: %w", id, err)
	}
	return nil
}

// SetDownloaded records a successful download: media path, filesize, the
// resolved format, the SponsorBlock segments JSON, status=downloaded, and
// the downloaded_at timestamp. error_message is cleared (a prior failed
// attempt's message must not linger on a now-successful video). It also fills
// in published_at — see DownloadedResult.PublishedAt for why that lands here.
func (s *Store) SetDownloaded(id string, res DownloadedResult) error {
	segments := res.SponsorblockSegments
	if segments == "" {
		segments = "[]"
	}
	_, err := s.db.ExecContext(context.Background(), `
UPDATE videos
SET media_path = ?, thumbnail_path = COALESCE(NULLIF(?, ''), thumbnail_path),
	filesize_bytes = ?, format_used = ?, sponsorblock_segments = ?,
	subtitle_path = ?, audio_language = ?,
	chapters = CASE WHEN ? != '' THEN ? ELSE chapters END,
	published_at = COALESCE(NULLIF(?, ''), published_at),
	status = 'downloaded', error_message = '', downloaded_at = datetime('now')
WHERE id = ?`,
		res.MediaPath, res.ThumbnailPath, res.FilesizeBytes, res.FormatUsed, segments,
		res.SubtitleRelPath, res.AudioLanguage,
		res.ChaptersJSON, res.ChaptersJSON,
		res.PublishedAt,
		id,
	)
	if err != nil {
		return fmt.Errorf("set video %s downloaded: %w", id, err)
	}
	return nil
}

// SetSubtitle records the downloaded subtitle relpath and resolved audio
// language.
func (s *Store) SetSubtitle(id, relPath, audioLang string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET subtitle_path=?, audio_language=? WHERE id=?`, relPath, audioLang, id)
	if err != nil {
		return fmt.Errorf("set video %s subtitle: %w", id, err)
	}
	return nil
}

// SetSummaryStatus updates the summarization lifecycle state and error.
func (s *Store) SetSummaryStatus(id, status, errMsg string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET summary_status=?, summary_error=? WHERE id=?`, status, errMsg, id)
	if err != nil {
		return fmt.Errorf("set video %s summary status: %w", id, err)
	}
	return nil
}

// SetSummary persists the three artifacts and marks the summary done.
func (s *Store) SetSummary(id, summary, chaptersJSON, keyPointsJSON string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET summary=?, chapters=?, key_points=?, summary_status='done', summary_error='' WHERE id=?`,
		summary, chaptersJSON, keyPointsJSON, id)
	if err != nil {
		return fmt.Errorf("set video %s summary: %w", id, err)
	}
	return nil
}

// SetSummaryText persists just the prose summary, leaving chapters, key points
// and status untouched, so the resumable summarize worker can save it the
// moment it is produced — before the fragile key-points step — instead of
// discarding it if a later step fails. Clears any prior summary error.
func (s *Store) SetSummaryText(id, summary string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET summary=?, summary_error='' WHERE id=?`, summary, id)
	if err != nil {
		return fmt.Errorf("set video %s summary text: %w", id, err)
	}
	return nil
}

// SetKeyPoints persists the chapters and key points independently of the prose
// summary, so a failure in that step never discards an already-saved summary
// and a retry only re-runs the key-points call.
func (s *Store) SetKeyPoints(id, chaptersJSON, keyPointsJSON string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET chapters=?, key_points=? WHERE id=?`, chaptersJSON, keyPointsJSON, id)
	if err != nil {
		return fmt.Errorf("set video %s key points: %w", id, err)
	}
	return nil
}

// SetCategory persists a video's classification. The value must already be a
// valid enum id or 'uncategorized' (callers use videos.NormalizeCategory).
func (s *Store) SetCategory(id, category string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET category = ? WHERE id = ?`, category, id)
	if err != nil {
		return fmt.Errorf("set video %s category: %w", id, err)
	}
	return nil
}

// SetCategoryIfUnset writes category only while the row is still
// 'uncategorized', and reports whether it actually wrote. It is the
// classifier's write: both worker paths decide to classify from a row read
// BEFORE a slow LLM call, so by the time the answer arrives the user may have
// picked a category by hand on the Player. An unconditional UPDATE would
// silently overwrite that — the guard belongs in the statement, not in a
// re-read, because any re-read has the same race one layer up.
//
// A manual pick from the Player uses plain SetCategory: the human is allowed
// to overwrite the model, never the other way round.
func (s *Store) SetCategoryIfUnset(id, category string) (bool, error) {
	res, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET category = ? WHERE id = ? AND category = ?`,
		category, id, UncategorizedCategory)
	if err != nil {
		return false, fmt.Errorf("set video %s category if unset: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set video %s category if unset: %w", id, err)
	}
	return n > 0, nil
}

// NextUnclassified returns one downloaded video that has a summary but is
// still 'uncategorized', newest first, or (nil, nil) when there is none. It
// backs the summarize worker's idle sweep, which repairs videos whose classify
// step never ran — chiefly those summarized before classification moved ahead
// of the fragile key-points call, where a key-points failure returned before
// the category was ever set.
//
// skip is the caller's in-process set of video ids whose classify call errored,
// excluded so one persistently failing video cannot starve the rest of the
// backlog. A non-empty summary is required: with no summary there is nothing
// to classify from, which is the no-transcript case that stays uncategorized
// by design.
func (s *Store) NextUnclassified(skip []string) (*Video, error) {
	q := "SELECT " + videoColumns + ` FROM videos
		WHERE status = 'downloaded' AND category = ? AND summary <> ''`
	args := []any{UncategorizedCategory}
	if len(skip) > 0 {
		q += " AND id NOT IN (?" + strings.Repeat(",?", len(skip)-1) + ")"
		for _, id := range skip {
			args = append(args, id)
		}
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT 1"

	v, err := scanVideo(s.db.QueryRowContext(context.Background(), q, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("next unclassified video: %w", err)
	}
	return &v, nil
}

// SetFavorite sets a video's favorite flag, stamping (or clearing)
// favorited_at to match.
func (s *Store) SetFavorite(id string, fav bool) error {
	var err error
	if fav {
		_, err = s.db.ExecContext(context.Background(),
			`UPDATE videos SET favorite = 1, favorited_at = datetime('now') WHERE id = ?`, id)
	} else {
		_, err = s.db.ExecContext(context.Background(),
			`UPDATE videos SET favorite = 0, favorited_at = NULL WHERE id = ?`, id)
	}
	if err != nil {
		return fmt.Errorf("set video %s favorite: %w", id, err)
	}
	return nil
}

// SetWatched is the manual watched toggle. Setting true marks the video
// watched, stamping watched_at only if it isn't already set (no life
// extension on a manual re-confirmation); it leaves resume_position_seconds
// untouched. Setting false clears watched, watched_at, AND
// resume_position_seconds — this rescues the video from the retention
// sweep, per the decided un-watch rule. Zeroing the resume position makes
// the rescue sticky: without it, a player resume ping still sitting at or
// above the 90% threshold would immediately re-cross SetResume's
// auto-watched check and undo the un-watch.
func (s *Store) SetWatched(id string, watched bool) error {
	var err error
	if watched {
		_, err = s.db.ExecContext(context.Background(), `
UPDATE videos SET watched = 1, watched_at = COALESCE(watched_at, datetime('now'))
WHERE id = ?`, id)
	} else {
		_, err = s.db.ExecContext(context.Background(), `
UPDATE videos SET watched = 0, watched_at = NULL, resume_position_seconds = 0
WHERE id = ?`, id)
	}
	if err != nil {
		return fmt.Errorf("set video %s watched: %w", id, err)
	}
	return nil
}

// SetResume records the player's resume position and, per the decided
// watched rule, auto-marks the video watched once position reaches >= 90%
// of its duration (this also covers "reaches the end", since position can't
// exceed duration in practice). watched_at is stamped only the first time —
// a later call at or above the threshold (re-watching) never resets it.
// Duration 0/unknown never auto-marks watched (there is no ratio to check).
//
// A negative position is clamped to 0 rather than stored as-is: the HTTP
// handler already rejects negative positions with a 400, but the store
// clamps too as defense-in-depth against any other caller.
func (s *Store) SetResume(id string, position float64) error {
	if position < 0 {
		position = 0
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("set video %s resume: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	var duration sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT duration_seconds FROM videos WHERE id = ?`, id,
	).Scan(&duration); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("set video %s resume: not found", id)
		}
		return fmt.Errorf("set video %s resume: %w", id, err)
	}

	autoWatched := duration.Valid && duration.Int64 > 0 &&
		position >= watchedThreshold*float64(duration.Int64)

	if autoWatched {
		_, err = tx.ExecContext(ctx, `
UPDATE videos
SET resume_position_seconds = ?, watched = 1, watched_at = COALESCE(watched_at, datetime('now'))
WHERE id = ?`, position, id)
	} else {
		_, err = tx.ExecContext(ctx,
			`UPDATE videos SET resume_position_seconds = ? WHERE id = ?`, position, id)
	}
	if err != nil {
		return fmt.Errorf("set video %s resume: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("set video %s resume: %w", id, err)
	}
	return nil
}

// SetResumeRaw sets a video's resume position WITHOUT the >=90% auto-watch that
// SetResume applies. The TubeArchivist import uses it so a partially-watched
// "continue" video imported at, say, 92% keeps its position and stays in the
// Continue Watching queue rather than being flipped to watched (which would
// drop it out of exactly the queue the migration exists to preserve). It errors
// on a missing row, so callers must Upsert the video first.
func (s *Store) SetResumeRaw(id string, position float64) error {
	if position < 0 {
		position = 0
	}
	res, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET resume_position_seconds = ? WHERE id = ?`, position, id)
	if err != nil {
		return fmt.Errorf("set video %s resume (raw): %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set video %s resume (raw): %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("set video %s resume (raw): not found", id)
	}
	return nil
}

// SweepCandidates returns videos eligible for the retention sweeper
// (Task 12): watched, not favorited, not already tombstoned, and last
// watched strictly before cutoff (an absolute point in time, formatted
// "2006-01-02 15:04:05" UTC to match the format datetime('now') stores in
// watched_at — the caller computes cutoff from settings.RetentionDays and
// its own clock, so the sweeper stays testable without depending on
// SQLite's notion of "now"). Oldest-watched first, so the sweeper's log
// order reads chronologically.
func (s *Store) SweepCandidates(cutoffUTC string) ([]Video, error) {
	rows, err := s.db.QueryContext(context.Background(),
		"SELECT "+videoColumns+` FROM videos
WHERE watched = 1 AND favorite = 0 AND status != 'tombstoned' AND watched_at < ?
ORDER BY watched_at ASC`, cutoffUTC,
	)
	if err != nil {
		return nil, fmt.Errorf("sweep candidates: %w", err)
	}
	defer rows.Close()

	out := []Video{}
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, fmt.Errorf("sweep candidates: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sweep candidates: %w", err)
	}
	return out, nil
}

// Tombstone marks a video deleted-but-remembered: media_path and
// subtitle_path are cleared and status becomes 'tombstoned', but the row
// (and its watched history) is kept — a future badge can offer
// re-download. Tombstone only updates the database; the caller must unlink
// the actual media/thumbnail/subtitle files first (it needs
// config.MediaDir and path-safety checks the store doesn't have).
func (s *Store) Tombstone(id string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET media_path = '', subtitle_path = '', status = 'tombstoned' WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("tombstone video %s: %w", id, err)
	}
	return nil
}

// nullInt maps 0 to a NULL (the schema leaves duration/filesize nullable
// for "unknown"), any other value to itself.
func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// nullStr maps "" to NULL, any other value to itself.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
