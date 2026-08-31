package videos

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SetAudioLanguage records the language a video's captions are in.
//
// It is what is left of SetSubtitle, which also wrote the subtitle_path column
// dropped in 0024. The language is not vestigial with it: the download worker
// asks yt-dlp for that language on the next fetch, and both video DTOs carry it
// to the player as the <track srcLang>.
func (s *Store) SetAudioLanguage(id, audioLang string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET audio_language=? WHERE id=?`, audioLang, id)
	if err != nil {
		return fmt.Errorf("set video %s audio language: %w", id, err)
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
	// Writing chapters invalidates the search index: chapter chunks are built
	// from this column, so whatever is stored was built without these. Zeroing
	// embed_rev in the SAME statement is what makes the invariant hold — a
	// separate call could be interrupted between the two, leaving an index that
	// claims to be current but predates the chapters it should contain.
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET chapters=?, key_points=?, embed_rev=0 WHERE id=?`, chaptersJSON, keyPointsJSON, id)
	if err != nil {
		return fmt.Errorf("set video %s key points: %w", id, err)
	}
	return nil
}

// ClearEmbedRev marks a video's search index stale, so the next summarize or
// re-embed pass rebuilds it. Used by Reprocess, which throws away the stored
// analysis the index was built from.
func (s *Store) ClearEmbedRev(id string) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET embed_rev=0 WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("clear video %s embed rev: %w", id, err)
	}
	return nil
}

// ChatUsage is what the chat model has spent on one video: the tokens, and what
// they cost in nanodollars (billionths of a dollar — integers, because a whole
// video costs a fraction of a cent and floats drift in exactly those digits).
//
// The two token lanes are not disjoint: CachedTokens is a SUBSET of
// PromptTokens, and the completion count already includes the model's reasoning
// tokens. Anything deriving a figure from these has to know that; the price was
// computed upstream in internal/llm, which does.
//
// Deliberately plain int64s rather than an llm.Usage. This package must not
// import internal/llm — it is the storage layer, and a dependency on the client
// that happens to produce the numbers would invert that.
type ChatUsage struct {
	PromptTokens     int64
	CachedTokens     int64
	CompletionTokens int64
	CostNanoUSD      int64
}

// Empty reports whether nothing has been accounted for this video, which is the
// state of every row analysed before migration 0028 as well as of any video the
// endpoint never reported usage for. Callers use it to omit a figure rather than
// display a confident zero.
func (u ChatUsage) Empty() bool {
	return u.PromptTokens == 0 && u.CachedTokens == 0 && u.CompletionTokens == 0 && u.CostNanoUSD == 0
}

// AddChatUsage folds one analysis run's chat spend into a video's running
// totals.
//
// ADDITIVE, and that is the whole design. The job queue retries a failed
// analysis up to max_attempts times and every attempt spends real tokens at the
// endpoint, so a column overwritten with the last run's snapshot would report a
// video that failed twice as costing only its third attempt. The same follows
// for a manual Re-summarize: it is money spent on this video, so it adds. What
// the columns answer is "what has this video cost", not "what would it cost to
// produce the analysis currently on screen".
func (s *Store) AddChatUsage(id string, u ChatUsage) error {
	_, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET
		   chat_prompt_tokens     = chat_prompt_tokens + ?,
		   chat_cached_tokens     = chat_cached_tokens + ?,
		   chat_completion_tokens = chat_completion_tokens + ?,
		   chat_cost_nano_usd     = chat_cost_nano_usd + ?
		 WHERE id = ?`,
		u.PromptTokens, u.CachedTokens, u.CompletionTokens, u.CostNanoUSD, id)
	if err != nil {
		return fmt.Errorf("add video %s chat usage: %w", id, err)
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
// StatusNew is the one status that IS excluded, and it is the exception that
// proves the paragraph above. An inbox video — captions read, summary written,
// still awaiting the decision to download — has a summary and is uncategorized,
// so it matches every other condition here. But its category was skipped on
// purpose: the summarize worker stops after the prose for exactly these rows,
// so that a video the user ignores never cost a classify call. This sweep
// would quietly undo that, spending one call per inbox video, and after any
// category-reset migration spending it again on every video ever declined.
//
// Migration 0004's reset selects on these same rules; the two must stay in
// step, and TestResetSetMatchesTheSweep pins them together.
//
// skip is the caller's in-process set of video ids whose classify call errored,
// excluded so one persistently failing video cannot starve the rest of the
// backlog.
func (s *Store) NextUnclassified(skip []string) (*Video, error) {
	q := "SELECT " + videoColumns + " " + videoFrom + `
		WHERE v.category = ? AND v.summary <> '' AND v.category_manual = 0
		  AND v.status <> '` + StatusNew + `'`
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
