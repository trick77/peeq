package videos

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

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

// ClearSummary wipes a video's stored analysis — prose summary, chapters and
// key points — leaving summary_status alone so the caller decides the resulting
// state. It is the counterpart of SetSummary for two cases: re-analysis that
// found nothing to summarize, and a user-triggered re-summarize. The worker's
// pipeline is resumable and skips the summary step when summary <> ”, so
// without this a redo would silently keep the old text.
func (s *Store) ClearSummary(id string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET summary='', chapters='', key_points='', summary_error='' WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("clear video %s summary: %w", id, err)
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
//
// It also maintains category_manual, the flag that survives a bulk
// reclassification (see migration 0004). One rule covers both callers, because
// both are the human speaking: a Player pick sets a real category and marks
// the row manual; Re-summarize resets to 'uncategorized' and clears the flag,
// which is what hands the video back to the classifier. Anything else would
// leave a re-summarized row flagged and permanently uncategorized, since
// SetCategoryIfUnset refuses flagged rows.
func (s *Store) SetCategory(id, category string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET category = ?, category_manual = ? WHERE id = ?`,
		category, boolToInt(category != UncategorizedCategory), id)
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
// to overwrite the model, never the other way round. The category_manual guard
// says the same thing a second way — redundant while the picker offers no
// "clear" entry (so a flagged row is never 'uncategorized'), but it keeps the
// guarantee inside the statement that enforces it.
func (s *Store) SetCategoryIfUnset(id, category string) (bool, error) {
	res, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET category = ? WHERE id = ? AND category = ? AND category_manual = 0`,
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

// NextUnclassified returns one video that has a summary but is still
// 'uncategorized', newest first, or (nil, nil) when there is none. It backs the
// summarize worker's idle sweep, which repairs videos whose classify step never
// ran — chiefly those summarized before classification moved ahead of the
// fragile key-points call, where a key-points failure returned before the
// category was ever set.
//
// Having a summary is the ONLY requirement, and status deliberately is not one.
// Classification reads a title and a summary; it never touches the media file,
// so 'downloaded' was never the real precondition. Requiring it stranded every
// tombstoned video — media reclaimed, summary and row kept, still listed and
// still filtered by category in the Library — with whatever category it had
// when it was archived, unreachable by any later enum change. A no-transcript
// video has no summary and so is still excluded, which is the case that stays
// uncategorized by design.
//
// category_manual = 0 is the second condition, and it is here because
// SetCategoryIfUnset — the only writer this query feeds — refuses a flagged
// row. Selecting a row its own writer will reject is not a no-op: classifyOne
// treats applied=false as "the user picked one meanwhile" and deliberately
// does not park the video, on the premise that it no longer matches this
// query. Leave the flag out and that premise is false, so a flagged row still
// reading 'uncategorized' comes back every idle turn and burns a classify call
// each time, forever.
//
// Migration 0004's reset selects on these same two rules; the two must stay in
// step, and TestResetSetMatchesTheSweep pins them together.
//
// skip is the caller's in-process set of video ids whose classify call errored,
// excluded so one persistently failing video cannot starve the rest of the
// backlog.
func (s *Store) NextUnclassified(skip []string) (*Video, error) {
	q := "SELECT " + videoColumns + " " + videoFrom + `
		WHERE v.category = ? AND v.summary <> '' AND v.category_manual = 0`
	args := []any{UncategorizedCategory}
	if len(skip) > 0 {
		q += " AND v.id NOT IN (?" + strings.Repeat(",?", len(skip)-1) + ")"
		for _, id := range skip {
			args = append(args, id)
		}
	}
	q += " ORDER BY v.created_at DESC, v.id DESC LIMIT 1"

	v, err := scanVideo(s.db.QueryRowContext(context.Background(), q, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("next unclassified video: %w", err)
	}
	return &v, nil
}
