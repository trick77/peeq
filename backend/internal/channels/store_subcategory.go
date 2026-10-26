package channels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/trick77/peeq/internal/store"
)

// regrowBelow bounds re-classification: a channel answered from fewer videos
// than this is asked again once it has doubled. Past it, the labels rest on
// enough titles that growth adds nothing but calls.
const regrowBelow = 20

// SubcategorizeCandidate is a channel the subcategory sweep owes an answer,
// with what the classifier reads.
type SubcategorizeCandidate struct {
	ID          string
	Name        string
	Handle      string
	Description string
	Videos      int
}

// SetSubcategoriesIfAuto is the classifier's write: it replaces the channel's
// subcategories with keys (primary first) and records the video count they
// were chosen from, unless the user has set them by hand, and reports whether
// it wrote. An empty keys is a real answer and is recorded as one.
func (s *Store) SetSubcategoriesIfAuto(ctx context.Context, id string, keys []string, videos int) (bool, error) {
	return s.setSubcategories(ctx, id, keys, false, videos)
}

// SetSubcategoriesManual is the channel page's write: the user's labels, which
// the classifier never overwrites.
func (s *Store) SetSubcategoriesManual(ctx context.Context, id string, keys []string) error {
	_, err := s.setSubcategories(ctx, id, keys, true, 0)
	return err
}

func (s *Store) setSubcategories(ctx context.Context, id string, keys []string, manual bool, videos int) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("set channel %s subcategories: begin: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()
	if !manual {
		var isManual bool
		err := tx.QueryRowContext(ctx, `SELECT manual FROM channel_subcategory_state WHERE channel_id = ?`, id).Scan(&isManual)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("set channel %s subcategories: read state: %w", id, err)
		}
		if isManual {
			return false, nil
		}
	}
	if err := clearSubcategoriesTx(ctx, tx, id); err != nil {
		return false, fmt.Errorf("set channel %s subcategories: %w", id, err)
	}
	for i, k := range keys {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO channel_subcategories (channel_id, subcategory, rank) VALUES (?, ?, ?)`, id, k, i); err != nil {
			return false, fmt.Errorf("set channel %s subcategories: insert %s: %w", id, k, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO channel_subcategory_state (channel_id, manual, video_count) VALUES (?, ?, ?)`,
		id, manual, videos); err != nil {
		return false, fmt.Errorf("set channel %s subcategories: state: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("set channel %s subcategories: commit: %w", id, err)
	}
	return true, nil
}

// ClearSubcategories drops the channel's labels and its marker, manual or not,
// handing it back to the classifier.
func (s *Store) ClearSubcategories(ctx context.Context, id string) error {
	if err := clearSubcategoriesTx(ctx, s.db, id); err != nil {
		return fmt.Errorf("clear channel %s subcategories: %w", id, err)
	}
	return nil
}

func clearSubcategoriesTx(ctx context.Context, x store.DBTX, id string) error {
	if _, err := x.ExecContext(ctx, `DELETE FROM channel_subcategories WHERE channel_id = ?`, id); err != nil {
		return err
	}
	_, err := x.ExecContext(ctx, `DELETE FROM channel_subcategory_state WHERE channel_id = ?`, id)
	return err
}

// Subcategories returns the channel's keys, primary first, and whether the
// user set them by hand.
func (s *Store) Subcategories(ctx context.Context, id string) ([]string, bool, error) {
	var manual bool
	err := s.db.QueryRowContext(ctx, `SELECT manual FROM channel_subcategory_state WHERE channel_id = ?`, id).Scan(&manual)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("channel %s subcategories: %w", id, err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT subcategory FROM channel_subcategories WHERE channel_id = ? ORDER BY rank`, id)
	if err != nil {
		return nil, false, fmt.Errorf("channel %s subcategories: %w", id, err)
	}
	defer func() { _ = rows.Close() }()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, false, fmt.Errorf("channel %s subcategories: scan: %w", id, err)
		}
		keys = append(keys, k)
	}
	return keys, manual, rows.Err()
}

// AllSubcategories returns every labelled channel's keys, primary first.
func (s *Store) AllSubcategories(ctx context.Context) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT channel_id, subcategory FROM channel_subcategories ORDER BY channel_id, rank`)
	if err != nil {
		return nil, fmt.Errorf("channel subcategories: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][]string{}
	for rows.Next() {
		var id, k string
		if err := rows.Scan(&id, &k); err != nil {
			return nil, fmt.Errorf("channel subcategories: scan: %w", err)
		}
		out[id] = append(out[id], k)
	}
	return out, rows.Err()
}

// NextToSubcategorize returns the channel the sweep should classify next, the
// one with the most videos first, or (nil, nil) when none is owed: a channel
// List would show (added, or with a download) that was never answered for, or
// was answered automatically from fewer than regrowBelow videos and has since
// doubled. A manual answer is final. skip holds channels whose call failed in
// this process.
//
// The per-channel count is read from idx_videos_channel_categories, so no
// videos row is touched.
func (s *Store) NextToSubcategorize(ctx context.Context, skip []string) (*SubcategorizeCandidate, error) {
	q := `
WITH counts AS (
	SELECT channel_id, count(*) AS n FROM videos INDEXED BY idx_videos_channel_categories
	WHERE channel_id <> '' GROUP BY channel_id
)
SELECT c.id, c.name, c.handle, c.description, COALESCE(k.n, 0) AS n
FROM channels c
LEFT JOIN counts k ON k.channel_id = c.id
LEFT JOIN channel_subcategory_state st ON st.channel_id = c.id
WHERE (c.added_at IS NOT NULL OR ` + hasDownloadsPredicate + `)
  AND (st.channel_id IS NULL OR (st.manual = 0 AND st.video_count < ` + fmt.Sprint(regrowBelow) + `
       AND COALESCE(k.n, 0) > st.video_count AND COALESCE(k.n, 0) >= 2 * st.video_count))`
	var args []any
	if len(skip) > 0 {
		q += ` AND c.id NOT IN (` + store.Placeholders(len(skip)) + `)`
		for _, id := range skip {
			args = append(args, id)
		}
	}
	q += ` ORDER BY n DESC, c.id LIMIT 1`
	var c SubcategorizeCandidate
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&c.ID, &c.Name, &c.Handle, &c.Description, &c.Videos)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("next channel to subcategorize: %w", err)
	}
	return &c, nil
}
