package videos

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/trick77/peeq/internal/store"
)

// BrowseMaxLimit bounds one Browse page, and with it step two's id list.
const BrowseMaxLimit = 50

// BrowseSorts are the orders Browse accepts, the default first.
var BrowseSorts = []string{"newest", "oldest", "added_newest", "title"}

// browseSortClauses are Browse's ORDER BY fragments, each ending on the id so
// paging never repeats or skips a row. newest, oldest and title are the
// Library's (sortClauses). added_newest is not: inbox reads have no
// downloaded_at, so it falls back to when peeq recorded the row (same
// 'YYYY-MM-DD HH:MM:SS' shape, both datetime('now')).
var browseSortClauses = map[string]string{
	"newest":       sortClauses["newest"],
	"oldest":       sortClauses["oldest"],
	"added_newest": "COALESCE(v.downloaded_at, v.created_at) DESC, v.id DESC",
	"title":        sortClauses["title"],
}

// BrowseOptions narrows Browse. Every field is optional.
type BrowseOptions struct {
	// ChannelIDs scopes to channels; ChannelNames is the legacy arm for rows
	// recorded without a channel id (exact name, channel_id = '' only), as in
	// rag.Filter.
	ChannelIDs   []string
	ChannelNames []string
	// Title is a case-insensitive title substring (the Library search box's).
	Title string
	// After and Before are inclusive 'YYYY-MM-DD' release-date bounds.
	After, Before string
	// Sort is one of BrowseSorts; anything else means newest.
	Sort string
	// Limit is clamped to 1..BrowseMaxLimit; Offset below 0 counts as 0.
	Limit, Offset int
}

// browseStatements is Browse's step one, built once per call so the plan
// test holds the exact statements to the index.
type browseStatements struct {
	page      string
	pageArgs  []any
	count     string
	countArgs []any
}

// browseFrom pins the videos side to idx_videos_cards, which holds every
// column step one reads, filters and sorts on (see listFrom for why it is
// pinned). The channels join is by primary key, for the display name.
const browseFrom = `FROM videos v INDEXED BY idx_videos_cards LEFT JOIN channels ch ON ch.id = v.channel_id`

// browseQueries builds the page and the count. Unlike List, no status is
// excluded: the queue and the inbox reads are what a browse must show.
func browseQueries(opts BrowseOptions) browseStatements {
	var conds []string
	var args []any
	if len(opts.ChannelIDs) > 0 || len(opts.ChannelNames) > 0 {
		var arms []string
		if len(opts.ChannelIDs) > 0 {
			arms = append(arms, "v.channel_id IN ("+store.Placeholders(len(opts.ChannelIDs))+")")
			for _, id := range opts.ChannelIDs {
				args = append(args, id)
			}
		}
		if len(opts.ChannelNames) > 0 {
			arms = append(arms, "(v.channel_id = '' AND v.channel_name IN ("+store.Placeholders(len(opts.ChannelNames))+"))")
			for _, n := range opts.ChannelNames {
				args = append(args, n)
			}
		}
		conds = append(conds, "("+strings.Join(arms, " OR ")+")")
	}
	if c, arg, ok := queryCond(opts.Title); ok {
		conds = append(conds, c)
		args = append(args, arg)
	}
	// rag.Filter's release date: a dateless row counts by the day it was
	// recorded, so list_videos and search agree on a date range.
	const released = "COALESCE(v.published_at, date(v.created_at))"
	if opts.After != "" {
		conds = append(conds, released+" >= ?")
		args = append(args, opts.After)
	}
	if opts.Before != "" {
		conds = append(conds, released+" <= ?")
		args = append(args, opts.Before)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	order, ok := browseSortClauses[opts.Sort]
	if !ok {
		order = browseSortClauses["newest"]
	}
	limit := min(max(opts.Limit, 1), BrowseMaxLimit)
	offset := max(opts.Offset, 0)
	return browseStatements{
		page: `SELECT v.id, v.title,
	COALESCE(NULLIF(v.channel_name, ''), NULLIF(ch.name, ''), v.channel_id) AS channel_name,
	v.duration_seconds, v.published_at, v.status ` + browseFrom + where + " ORDER BY " + order + " LIMIT ? OFFSET ?",
		pageArgs:  append(append([]any{}, args...), limit, offset),
		count:     "SELECT COUNT(*) FROM videos v INDEXED BY idx_videos_cards" + where,
		countArgs: args,
	}
}

// browseFlagsQuery is step two over n ids: each video by primary key, its
// transcript by primary key, any chunk by idx_transcript_chunks_video.
// summary_status sits behind the analysis text in the row, which is why it
// is read for one page only, never in step one.
func browseFlagsQuery(n int) string {
	return `SELECT v.id, v.summary_status,
	EXISTS (SELECT 1 FROM video_transcripts t WHERE t.video_id = v.id),
	EXISTS (SELECT 1 FROM transcript_chunks c WHERE c.video_id = v.id)
FROM videos v WHERE v.id IN (` + store.Placeholders(n) + `)`
}

// Browse lists every video of the library, whatever its status, one page at
// a time with the total for the same filter. Step one (page and count) never
// opens a video row: idx_videos_cards, or for an unfiltered count SQLite's
// count shortcut onto the narrowest covering index. Step two reads what
// decides searchability for the page's ids only.
func (s *Store) Browse(ctx context.Context, opts BrowseOptions) (BrowsePage, error) {
	q := browseQueries(opts)
	var out BrowsePage
	// The title is the caller's text: kept out of the error, which reaches logs.
	if err := s.db.QueryRowContext(ctx, q.count, q.countArgs...).Scan(&out.Total); err != nil {
		return BrowsePage{}, fmt.Errorf("browse videos: count: %w", err)
	}
	videos, err := s.browsePage(ctx, q)
	if err != nil {
		return BrowsePage{}, err
	}
	if err := s.browseFlags(ctx, videos); err != nil {
		return BrowsePage{}, err
	}
	out.Videos = videos
	return out, nil
}

func (s *Store) browsePage(ctx context.Context, q browseStatements) ([]BrowseVideo, error) {
	rows, err := s.db.QueryContext(ctx, q.page, q.pageArgs...)
	if err != nil {
		return nil, fmt.Errorf("browse videos: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []BrowseVideo{}
	for rows.Next() {
		var v BrowseVideo
		var duration sql.NullInt64
		var published sql.NullString
		if err := rows.Scan(&v.ID, &v.Title, &v.ChannelName, &duration, &published, &v.Status); err != nil {
			return nil, fmt.Errorf("browse videos: scan: %w", err)
		}
		v.DurationSeconds, v.PublishedAt = duration.Int64, published.String
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("browse videos: %w", err)
	}
	return out, nil
}

// browseFlags fills SummaryStatus, HasTranscript and Indexed in place.
func (s *Store) browseFlags(ctx context.Context, page []BrowseVideo) error {
	if len(page) == 0 {
		return nil
	}
	ids := make([]any, len(page))
	at := make(map[string]int, len(page))
	for i, v := range page {
		ids[i] = v.ID
		at[v.ID] = i
	}
	rows, err := s.db.QueryContext(ctx, browseFlagsQuery(len(ids)), ids...)
	if err != nil {
		return fmt.Errorf("browse videos: flags: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, summary string
		var transcript, indexed bool
		if err := rows.Scan(&id, &summary, &transcript, &indexed); err != nil {
			return fmt.Errorf("browse videos: flags: scan: %w", err)
		}
		if i, ok := at[id]; ok {
			page[i].SummaryStatus, page[i].HasTranscript, page[i].Indexed = summary, transcript, indexed
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("browse videos: flags: %w", err)
	}
	return nil
}
