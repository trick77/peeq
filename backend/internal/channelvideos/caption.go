package channelvideos

import (
	"context"
	"database/sql"
	"fmt"
)

// CaptionExhausted is the attempt count that means "never fetch captions for
// this row". Migration 0018 stamps it on every row that existed when the
// feature shipped, and MarkCaptionSettled stamps it on every row that has been
// answered — by captions arriving, or by the ladder running out.
//
// It is deliberately far above CaptionMaxAttempts rather than equal to it, so
// that raising the ladder later does not resurrect rows that were already
// settled under the old one.
const CaptionExhausted = 99

// CaptionMaxAttempts bounds the retry ladder: one fetch at discovery plus four
// retries. See captionfetch.Backoff for the delays and why they exist.
const CaptionMaxAttempts = 5

// CaptionCandidate is a ledger row that is due a caption fetch. It is a
// narrower shape than Entry on purpose: adding the caption columns to Entry
// would change scanRow and every reader of it, for the benefit of exactly one
// caller.
type CaptionCandidate struct {
	VideoID         string
	ChannelID       string
	Title           string
	URL             string
	DurationSeconds int
	PublishedAt     string
	ThumbnailURL    string
	// Attempts is the count BEFORE this fetch. The worker needs it to know
	// whether the attempt it is about to make is the last one, and therefore
	// whether a miss should settle the row as having no transcript.
	Attempts int
}

// NextCaptionCandidate returns the oldest pending ledger row that is due a
// caption fetch, or nil when there is nothing to do.
//
// "Due" is four conditions: the row is still awaiting a decision, its channel
// has not opted out, the ladder has not run out, and the next-attempt stamp
// has passed. A NULL stamp means "never attempted", which is due immediately —
// that is the fresh-discovery case and the one that matters most, since a
// summary is only useful before the user has already scrolled past the card.
//
// Oldest-first rather than newest-first: a backlog should drain in the order it
// accumulated, and the newest video is the one most likely to have no captions
// yet anyway.
//
// This is a plain SELECT, not a claim. The caption fetcher is a single
// goroutine (like every other poller in peeq), and the attempt is burned by
// RecordCaptionAttempt before the fetch runs, so there is no window in which
// two ticks pick up the same row.
func (s *Store) NextCaptionCandidate() (*CaptionCandidate, error) {
	var c CaptionCandidate
	var duration sql.NullInt64
	var publishedAt sql.NullString
	err := s.db.QueryRowContext(context.Background(), `
SELECT cv.video_id, cv.channel_id, cv.title, cv.url, cv.duration_seconds,
       cv.published_at, cv.thumbnail_url, cv.caption_attempts
  FROM channel_videos cv
  JOIN channels c ON c.id = cv.channel_id
 WHERE cv.state = 'pending'
   AND c.auto_summary = 1
   AND cv.caption_attempts < ?
   AND (cv.next_caption_attempt_at IS NULL OR cv.next_caption_attempt_at <= datetime('now'))
 ORDER BY cv.discovered_at ASC, cv.video_id ASC
 LIMIT 1`, CaptionMaxAttempts,
	).Scan(&c.VideoID, &c.ChannelID, &c.Title, &c.URL, &duration,
		&publishedAt, &c.ThumbnailURL, &c.Attempts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("next caption candidate: %w", err)
	}
	c.DurationSeconds = int(duration.Int64)
	c.PublishedAt = publishedAt.String
	return &c, nil
}

// RecordCaptionAttempt burns one rung of the ladder for videoID, scheduling the
// next attempt delaySeconds from now.
//
// Called BEFORE the fetch, not after. A fetch that panics, is killed mid-flight
// or hangs until the process restarts must not leave the row due again with its
// count unchanged — that is an infinite retry loop against YouTube, which is
// the one failure mode this whole ladder exists to avoid.
func (s *Store) RecordCaptionAttempt(videoID string, delaySeconds int) error {
	_, err := s.db.ExecContext(context.Background(), `
UPDATE channel_videos
   SET caption_attempts        = caption_attempts + 1,
       next_caption_attempt_at = datetime('now', ?)
 WHERE video_id = ?`, fmt.Sprintf("+%d seconds", delaySeconds), videoID)
	if err != nil {
		return fmt.Errorf("record caption attempt %s: %w", videoID, err)
	}
	return nil
}

// ReturnCaptionAttempt gives back the rung RecordCaptionAttempt burned, and
// makes the row due again immediately.
//
// This is for the case where peeq declined to talk to YouTube at all — no
// cookie, the kill-switch, the auto-pause breaker. The video did not fail; it
// was never tried. Spending the ladder on a gated period would quietly exhaust
// every video discovered while a cookie was expired, and they would all settle
// as having no transcript the moment the cookie was fixed.
//
// The floor at zero matters: a return that ran without a matching burn (a
// double-handled error, a future caller) must not push the count negative and
// hand a row an extra rung.
func (s *Store) ReturnCaptionAttempt(videoID string) error {
	_, err := s.db.ExecContext(context.Background(), `
UPDATE channel_videos
   SET caption_attempts        = MAX(caption_attempts - 1, 0),
       next_caption_attempt_at = NULL
 WHERE video_id = ?`, videoID)
	if err != nil {
		return fmt.Errorf("return caption attempt %s: %w", videoID, err)
	}
	return nil
}

// MarkCaptionSettled stops any further caption fetching for videoID, whether
// because captions arrived or because the ladder ran out.
//
// Note what it does NOT do: it leaves state alone. A row whose captions have
// been fetched and summarized is still 'pending' — still in the Inbox, still
// awaiting the decision the summary exists to inform. Only downloading or
// ignoring moves it out.
func (s *Store) MarkCaptionSettled(videoID string) error {
	_, err := s.db.ExecContext(context.Background(), `
UPDATE channel_videos
   SET caption_attempts        = ?,
       next_caption_attempt_at = NULL
 WHERE video_id = ?`, CaptionExhausted, videoID)
	if err != nil {
		return fmt.Errorf("settle captions %s: %w", videoID, err)
	}
	return nil
}
