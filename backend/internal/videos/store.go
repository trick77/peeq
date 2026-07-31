// Package videos persists the videos table (migration 0001_init.sql): one
// row per tracked YouTube video, holding its metadata, download state, and
// the watched/favorite/tombstone lifecycle.
//
// Watched semantics (Task 11, decided product rules): a video becomes
// watched automatically when its resume position reaches >= 90% of the
// duration (SetResume), or manually (SetWatched(id, true)). Re-watching
// never resets watched_at once set — no "life extension" of the retention
// clock — the one exception is RestartRetentionClock, which the re-download
// path calls precisely to grant one: a restored file needs its full
// retention_days back, or the next sweep reclaims what was just fetched.
// Manual un-watch (SetWatched(id, false)) clears watched, watched_at,
// AND resume_position_seconds, rescuing the video from the retention sweep
// and making that rescue sticky (a stale near-end resume ping can't
// immediately re-mark it watched). Manual mark-watched zeroes the resume
// position too — the button means "done", so there is nothing to resume —
// while the automatic >= 90% path keeps it, so a video watched to nearly
// the end can still be finished. Tombstone keeps
// the row (for watched history and a future summary/transcript) but clears
// media_path and marks status='tombstoned'; the caller is responsible for
// unlinking the actual media file from disk first (the thumbnail and the
// subtitle stay — see media.RemoveTombstonedVideoFiles).
//
// The store's methods are grouped across sibling files by the lifecycle stage
// they serve, all on the same *Store: this file holds the row shape, the
// shared read machinery and the two whole-library reads (Get, List);
// store_download.go the download lifecycle; store_probe.go the ffprobe
// backfill; store_sponsorblock.go the segment refresh; store_summary.go the
// summarize/classify pipeline; store_watch.go the watched/resume/retention
// rules described above.
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
	ID              string
	URL             string
	Title           string
	ChannelID       string
	ChannelName     string
	DurationSeconds int64
	PublishedAt     string
	Description     string
	// HasThumbnail is whether a poster is stored for this video (see
	// videoColumns). Migration 0024 took the thumbnail_path column that used to
	// stand in for this: a string claiming a file exists was something any
	// metadata write could blank out from under a perfectly good image, and did.
	HasThumbnail          bool
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
	// StateVersion is the row's watched-state generation counter (migration
	// 0010). It is read-only from this struct's point of view: only SetWatched
	// and SetResume's auto-watch path bump it, and both return the new value
	// rather than expecting a caller to set this field.
	StateVersion  int64
	Favorite      bool
	FavoritedAt   string
	CreatedAt     string
	DownloadedAt  string
	AudioLanguage string
	// HasTranscript is whether captions are stored for this video, the same
	// shape as HasThumbnail and for the same reason.
	HasTranscript bool
	Summary       string
	Chapters      string
	KeyPoints     string
	SummaryStatus string
	SummaryError  string
	EmbedModel    string
	EmbedDim      int
	// EmbedRev is the CONTENT recipe the stored chunks follow (which kinds of
	// chunk exist), as opposed to EmbedModel/EmbedDim which describe the model.
	// Below rag.ChunkRecipeRev means the index is stale and needs rebuilding.
	EmbedRev int
	Category string
	// MediaContainer, VideoCodec, VideoHeight and AudioCodec are what the
	// downloaded file actually is, filled in by mediaprobe. They carry
	// ffprobe's raw values ("mp4", "h264", 1080, "aac"); the UI does the
	// friendly naming. All are empty/zero until the file has been probed.
	MediaContainer string
	VideoCodec     string
	VideoHeight    int64
	AudioCodec     string
	// ProbedAt is when the probe was last ATTEMPTED, success or failure.
	// Empty means never attempted, which is what the backfill sweep selects.
	ProbedAt string
	// MediaType/LiveStatus/YTTags/YTCategories come straight from yt-dlp (see
	// ytdlp.Meta). YTTags/YTCategories are JSON arrays stored as TEXT, like
	// Chapters and KeyPoints, and are YouTube's own labels — not Category,
	// which is peeq's classification enum.
	MediaType    string
	LiveStatus   string
	YTTags       string
	YTCategories string
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
//
// published_at and description are refreshed but never CLEARED: several callers
// legitimately have no date or description to offer (scan's enqueueAuto seeds
// from a metadata-poor flat listing; the approve-from-inbox path passes
// id/url/title/duration only), and a plain `= excluded.published_at` let any of
// them blank out a good air date on a re-seen id. Filling a hole is fine;
// punching one is not.
//
// thumbnail_path used to be here, unguarded, and it cost real posters: every
// channel scan, every inbox caption-fetch and every add-by-URL blanked it on the
// rows it touched while the image sat untouched on disk. The poster is stored
// bytes now (0022) and the column is gone (0024), so there is no pointer left to
// get wrong — which is the durable version of that fix.
func (s *Store) Upsert(v Video) error {
	availability := v.Availability
	if availability == "" {
		availability = "unknown"
	}
	_, err := s.db.ExecContext(context.Background(), `
INSERT INTO videos (id, url, title, channel_id, channel_name, duration_seconds,
	published_at, description, availability, requested_format)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	url             = excluded.url,
	title           = excluded.title,
	channel_id      = excluded.channel_id,
	channel_name    = excluded.channel_name,
	duration_seconds = excluded.duration_seconds,
	published_at    = COALESCE(excluded.published_at, videos.published_at),
	description     = COALESCE(NULLIF(excluded.description, ''), videos.description),
	availability    = excluded.availability`,
		v.ID, v.URL, v.Title, v.ChannelID, v.ChannelName, nullInt(v.DurationSeconds),
		nullStr(v.PublishedAt), v.Description, availability, v.RequestedFormat,
	)
	if err != nil {
		return fmt.Errorf("upsert video %s: %w", v.ID, err)
	}
	return nil
}

// videoColumns is the column list shared by every whole-Video reader (Get,
// List, NextUnclassified, SweepCandidates), in the order scanVideo expects.
// Columns are qualified "v." because these queries LEFT JOIN the channels
// table (aliased "ch") to resolve the display channel name — see below.
//
// channel_name is coalesced, not read raw: a video discovered through a
// channel scan or subscription never gets its own videos.channel_name written
// (only manual-URL paste and TA-import do), so the raw column is empty for
// those rows and the UI would fall back to showing the bare UCxxxx id. The
// LEFT JOIN pulls the resolved name from the channels metadata cache instead,
// falling through to the id only when the channel itself is genuinely
// unresolved (resolve_ok = 0, name still blank). NULLIF guards both an empty
// videos.channel_name and an empty channels.name so neither shadows the next
// fallback. This is a read-side fix: it repairs existing rows with no
// migration. The write-side gap (populating videos.channel_name at scan/
// pending upsert) is a separate follow-up for consumers that read the column
// directly, e.g. search/export.
const videoColumns = `v.id, v.url, v.title, v.channel_id,
	COALESCE(NULLIF(v.channel_name, ''), NULLIF(ch.name, ''), v.channel_id) AS channel_name,
	v.duration_seconds, v.published_at,
	v.description,
	EXISTS (SELECT 1 FROM video_thumbnails t WHERE t.video_id = v.id) AS has_thumbnail,
	v.media_path, v.filesize_bytes, v.format_used, v.requested_format,
	v.availability, v.status, v.error_message, v.sponsorblock_segments,
	v.watched, v.watched_at, v.resume_position_seconds, v.state_version, v.favorite, v.favorited_at,
	v.created_at, v.downloaded_at,
	v.audio_language,
	EXISTS (SELECT 1 FROM video_transcripts t WHERE t.video_id = v.id) AS has_transcript, v.summary, v.chapters, v.key_points, v.summary_status, v.summary_error, v.embed_model, v.embed_dim, v.embed_rev, v.category,
	v.media_container, v.video_codec, v.video_height, v.audio_codec, v.probed_at,
	v.media_type, v.live_status, v.yt_tags, v.yt_categories`

// videoFrom is the FROM clause every whole-Video read shares: videos aliased
// "v", LEFT JOINed to the channels metadata cache so videoColumns can resolve
// the display channel name. LEFT (not INNER) so a video whose channel row was
// never created still returns — it simply falls through to the id.
const videoFrom = `FROM videos v LEFT JOIN channels ch ON ch.id = v.channel_id`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanVideo scans one row in the videoColumns order into a Video.
func scanVideo(rs rowScanner) (Video, error) {
	var v Video
	var duration, filesize sql.NullInt64
	var publishedAt, watchedAt, favoritedAt, downloadedAt, probedAt sql.NullString
	var watched, favorite, hasThumbnail, hasTranscript int
	err := rs.Scan(
		&v.ID, &v.URL, &v.Title, &v.ChannelID, &v.ChannelName, &duration, &publishedAt,
		&v.Description, &hasThumbnail, &v.MediaPath, &filesize, &v.FormatUsed, &v.RequestedFormat,
		&v.Availability, &v.Status, &v.ErrorMessage, &v.SponsorblockSegments,
		&watched, &watchedAt, &v.ResumePositionSeconds, &v.StateVersion, &favorite, &favoritedAt,
		&v.CreatedAt, &downloadedAt,
		&v.AudioLanguage, &hasTranscript, &v.Summary, &v.Chapters, &v.KeyPoints,
		&v.SummaryStatus, &v.SummaryError, &v.EmbedModel, &v.EmbedDim, &v.EmbedRev, &v.Category,
		&v.MediaContainer, &v.VideoCodec, &v.VideoHeight, &v.AudioCodec, &probedAt,
		&v.MediaType, &v.LiveStatus, &v.YTTags, &v.YTCategories,
	)
	if err != nil {
		return Video{}, err
	}
	v.DurationSeconds = duration.Int64
	v.HasThumbnail = hasThumbnail != 0
	v.HasTranscript = hasTranscript != 0
	v.FilesizeBytes = filesize.Int64
	v.PublishedAt = publishedAt.String
	v.Watched = watched != 0
	v.WatchedAt = watchedAt.String
	v.Favorite = favorite != 0
	v.FavoritedAt = favoritedAt.String
	v.DownloadedAt = downloadedAt.String
	v.ProbedAt = probedAt.String
	return v, nil
}

// Get returns the video row for id, or (nil, nil) if there is none.
func (s *Store) Get(id string) (*Video, error) {
	row := s.db.QueryRowContext(context.Background(),
		"SELECT "+videoColumns+" "+videoFrom+" WHERE v.id = ?", id,
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
// newest/oldest are the DEFAULT ordering and are restored here byte-for-byte
// to what they were before #139. That change repointed them at downloaded_at;
// the resulting grid was wrong in use, so the known-good clause is the one that
// stands. Do not "fix" it back without watching a real library under it first —
// this has now been changed twice on reasoning and reverted once on evidence.
//
// What it does: rank by release date, falling back to the row's own insertion
// date when yt-dlp reported none (some live streams and premieres), so a
// dateless row stays interleaved instead of sinking to one end forever. date()
// normalizes that fallback — published_at is 'YYYY-MM-DD' while created_at is
// 'YYYY-MM-DD HH:MM:SS', and comparing the shapes lexically would sort a
// same-day date-only value before the datetime one. The created_at tiebreak
// then orders same-day videos by when peeq recorded them, newest first.
//
// added_newest/added_oldest rank by downloaded_at — when peeq actually fetched
// the file — and are offered in the dropdown rather than as the default. NULL
// until a download succeeds, so an 'error' row (which the Library still lists,
// see notInFlight, so it can be retried) has no added date. `x IS NULL`
// evaluates to 0/1, so putting it first ASCENDING keeps dated rows ahead in
// BOTH directions; a plain `col DESC` would float SQLite's NULLs to the top.
// A re-download restamps downloaded_at, so "added" means last fetched.
var sortClauses = map[string]string{
	"newest":       "COALESCE(v.published_at, date(v.created_at)) DESC, v.created_at DESC, v.id DESC",
	"oldest":       "COALESCE(v.published_at, date(v.created_at)) ASC, v.created_at ASC, v.id ASC",
	"added_newest": "v.downloaded_at IS NULL, v.downloaded_at DESC, v.id DESC",
	"added_oldest": "v.downloaded_at IS NULL, v.downloaded_at ASC, v.id ASC",
	"longest":      "COALESCE(v.duration_seconds, 0) DESC, v.id DESC",
	"title":        "v.title COLLATE NOCASE ASC, v.id ASC",
}

// escapeLike escapes the three characters LIKE treats specially so a user
// typing "100%" searches for a literal percent sign rather than matching
// every row. Pairs with the ESCAPE '\' clause in the query below.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// notInFlight excludes the three states that mean "peeq is still working on
// this": freshly recorded, queued, and actively downloading. EVERY filter below
// applies it, which is what makes the Library mean one thing — videos that are
// here — rather than doubling as a view of the download queue. That queue has
// its own page now.
//
// Note what it does NOT exclude. An 'error' row (a failed download) and a
// 'tombstoned' row (media reclaimed by the retention sweeper, watched history
// kept) both stay: the Library grid is the only place either can be recovered
// from, since VideoCard's re-download button is rendered nowhere else. The rule
// is "not in the pipeline", not "playable".
const notInFlight = "v.status NOT IN ('new', 'queued', 'downloading')"

// List returns videos matching opts, ordered by opts.Sort. The status,
// category, search, and channel dimensions are orthogonal: all that are set
// apply together.
//   - Filter: "unwatched" (downloaded and not watched), "watched", "favorites",
//     or anything else/"" (no further constraint). Every one of them also
//     applies notInFlight — see that constant for why, and for why error and
//     tombstoned rows deliberately survive it.
//
// "downloading" was a filter value until the Library became ready-only. It is
// gone rather than kept as a no-op alias: the UI type dropped it too, so a
// caller still passing it is a bug, and it now lands in the default branch
// where it returns the same thing "all" does.
//   - Category: empty/"all"/unknown ⇒ no category constraint
//   - Query: case-insensitive substring match against title
//   - Sort: newest|oldest|added_newest|added_oldest|longest|title; anything
//     else falls back to newest. newest/oldest are the default and order by
//     release date (published_at), falling back to created_at for rows with no
//     known release date. added_newest/added_oldest order by when peeq fetched
//     the file (downloaded_at), with never-downloaded rows last. See
//     sortClauses.
//   - ChannelID/ChannelName: scopes to one channel, matching channel_id or,
//     for rows written before channel ids were recorded, an exact
//     channel_name match on rows with an empty channel_id
func (s *Store) List(opts ListOptions) ([]Video, error) {
	conds := []string{notInFlight}
	args := []any{}
	switch opts.Filter {
	case "unwatched":
		// Narrower than notInFlight on purpose: "unwatched" answers "what can I
		// press play on", so a failed or swept row is excluded here even though
		// it is still reachable through "all". The resume_position_seconds = 0
		// gate makes "unwatched" mean *never opened* — a partially-watched row
		// lives under "in_progress" instead, so the two never overlap.
		conds = append(conds, "v.status = 'downloaded' AND v.watched = 0 AND v.resume_position_seconds = 0")
	case "in_progress":
		// Started but not finished: the same play-eligible gate as "unwatched",
		// split off by a non-zero resume position.
		conds = append(conds, "v.status = 'downloaded' AND v.watched = 0 AND v.resume_position_seconds > 0")
	case "watched":
		conds = append(conds, "v.watched = 1")
	case "favorites":
		conds = append(conds, "v.favorite = 1")
	}
	if opts.Category != "" && opts.Category != "all" && ValidCategory(opts.Category) {
		conds = append(conds, "v.category = ?")
		args = append(args, opts.Category)
	}
	if q := strings.TrimSpace(opts.Query); q != "" {
		conds = append(conds, `v.title LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(q)+"%")
	}
	if opts.ChannelID != "" {
		if opts.ChannelName != "" {
			conds = append(conds, "(v.channel_id = ? OR (v.channel_id = '' AND v.channel_name = ?))")
			args = append(args, opts.ChannelID, opts.ChannelName)
		} else {
			conds = append(conds, "v.channel_id = ?")
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
		"SELECT "+videoColumns+" "+videoFrom+" "+where+" ORDER BY "+order,
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

// nullInt maps 0 to a NULL (the schema leaves duration/filesize nullable
// for "unknown"), any other value to itself.
func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// boolToInt maps a Go bool to the 0/1 SQLite stores for it.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullStr maps "" to NULL, any other value to itself.
func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
