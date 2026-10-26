package videos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/trick77/peeq/internal/store"
)

// SetSubcategoriesIfCategory replaces a video's subcategories with keys (in
// order, first = primary) and records that the classifier answered, but only
// while the video's category is still category, and reports whether it wrote.
// The classify call runs on a row read before it; a category picked by hand in
// the meantime must not receive labels chosen under the old one. An empty keys
// is a real answer ("nothing fits") and is recorded so the sweep moves on.
func (s *Store) SetSubcategoriesIfCategory(ctx context.Context, id, category string, keys []string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("set video %s subcategories: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT category FROM videos WHERE id = ?`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && current != category) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("set video %s subcategories: read category: %w", id, err)
	}
	if err := dropSubcategoriesTx(ctx, tx, id); err != nil {
		return false, fmt.Errorf("set video %s subcategories: %w", id, err)
	}
	for i, k := range keys {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO video_subcategories (video_id, subcategory, rank) VALUES (?, ?, ?)`, id, k, i); err != nil {
			return false, fmt.Errorf("set video %s subcategories: insert %s: %w", id, k, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO video_subcategory_state (video_id, category) VALUES (?, ?)`, id, category); err != nil {
		return false, fmt.Errorf("set video %s subcategories: state: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("set video %s subcategories: commit: %w", id, err)
	}
	return true, nil
}

// dropSubcategoriesTx removes a video's subcategories and its classified
// marker, handing it back to the sweep. Every category write calls it: the
// labels belong to the category they were chosen under.
func dropSubcategoriesTx(ctx context.Context, x store.DBTX, id string) error {
	if _, err := x.ExecContext(ctx, `DELETE FROM video_subcategories WHERE video_id = ?`, id); err != nil {
		return err
	}
	_, err := x.ExecContext(ctx, `DELETE FROM video_subcategory_state WHERE video_id = ?`, id)
	return err
}

// SubcategoriesCtx returns a video's subcategory keys, primary first.
func (s *Store) SubcategoriesCtx(ctx context.Context, id string) ([]string, error) {
	m, err := s.SubcategoriesMany(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	return m[id], nil
}

// SubcategoriesMany returns the subcategory keys of each of ids that has any,
// primary first. Ids without labels are absent from the map.
func (s *Store) SubcategoriesMany(ctx context.Context, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	query := `SELECT video_id, subcategory FROM video_subcategories WHERE video_id IN (` + store.Placeholders(len(ids)) + `) ORDER BY video_id, rank` //nolint:gosec // placeholders only
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("video subcategories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, fmt.Errorf("video subcategories: scan: %w", err)
		}
		out[id] = append(out[id], key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("video subcategories: %w", err)
	}
	return out, nil
}

// NextUnsubcategorized returns one video the subcategory sweep owes an answer,
// or (nil, nil) when there is none: categorized, not an inbox read (those are
// never categorized either, see summarize/worker.go), and never answered for
// under its current category. skip is the sweep's in-process set of videos
// whose call failed, so one bad video cannot starve the rest.
//
// category and status come from idx_videos_counts, so the scan never reads a
// row's text columns; only the chosen video is loaded whole.
func (s *Store) NextUnsubcategorized(ctx context.Context, skip []string) (*Video, error) {
	q := `SELECT v.id FROM videos v INDEXED BY idx_videos_counts
WHERE v.category <> ? AND v.status <> ?
  AND NOT EXISTS (SELECT 1 FROM video_subcategory_state s WHERE s.video_id = v.id)`
	args := []any{UncategorizedCategory, StatusNew}
	if len(skip) > 0 {
		q += ` AND v.id NOT IN (` + store.Placeholders(len(skip)) + `)`
		for _, id := range skip {
			args = append(args, id)
		}
	}
	q += ` LIMIT 1`
	var id string
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("next unsubcategorized video: %w", err)
	}
	return s.GetCtx(ctx, id)
}

// ChannelTitles returns up to limit of a channel's video titles, newest
// release first: what the channel classifier reads besides the description.
// Inbox reads count — a channel's labels are what makes its unread videos
// findable at all.
func (s *Store) ChannelTitles(ctx context.Context, channelID string, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT title FROM videos
WHERE channel_id = ? AND title <> ''
ORDER BY COALESCE(published_at, date(created_at)) DESC, id LIMIT ?`, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("channel %s titles: %w", channelID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("channel %s titles: scan: %w", channelID, err)
		}
		out = append(out, strings.TrimSpace(t))
	}
	return out, rows.Err()
}
