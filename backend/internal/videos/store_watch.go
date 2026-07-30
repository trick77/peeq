package videos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// watchedThreshold is the fraction of a video's duration that, once
// reached via SetResume, auto-marks it watched.
const watchedThreshold = 0.9

// ErrStaleVersion is returned by SetResume when the caller's echoed
// state_version no longer matches the row: the video's watched state changed
// somewhere else (another tab, another device) and this caller has not seen it,
// so its position is stale. The HTTP layer maps this to 409 so the losing
// client can refetch instead of clobbering. See migration 0010 and issue #97.
var ErrStaleVersion = errors.New("video state changed since last read")

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
// extension on a manual re-confirmation), and zeroes
// resume_position_seconds: pressing the button means "I'm done with this",
// so there is no meaningful point to resume from and reopening starts at
// 0:00. Note the deliberate asymmetry with the auto-watched path in
// SetResume, which keeps the position — someone who genuinely played past
// 90% may still want the last few minutes. Setting false clears watched,
// watched_at, AND resume_position_seconds — this rescues the video from the
// retention sweep, per the decided un-watch rule. Zeroing the resume
// position makes the rescue sticky: without it, a player resume ping still
// sitting at or above the 90% threshold would immediately re-cross
// SetResume's auto-watched check and undo the un-watch.
//
// Either direction bumps state_version and returns the new value (migration
// 0010). The bump lives here because this is where the two halves of playback
// state stop agreeing: the zeroed position is only correct for a client that
// knows the toggle happened, so every other client's echoed version has to stop
// being valid at exactly this moment. The caller returns the new version to the
// client that pressed the button, so it can carry on pinging without 409ing
// against its own toggle.
func (s *Store) SetWatched(id string, watched bool) (int64, error) {
	var q string
	if watched {
		q = `
UPDATE videos SET watched = 1, watched_at = COALESCE(watched_at, datetime('now')),
	resume_position_seconds = 0, state_version = state_version + 1
WHERE id = ?
RETURNING state_version`
	} else {
		q = `
UPDATE videos SET watched = 0, watched_at = NULL, resume_position_seconds = 0,
	state_version = state_version + 1
WHERE id = ?
RETURNING state_version`
	}
	var version int64
	if err := s.db.QueryRowContext(context.Background(), q, id).Scan(&version); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("set video %s watched: not found", id)
		}
		return 0, fmt.Errorf("set video %s watched: %w", id, err)
	}
	return version, nil
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
//
// expectVersion is the state_version the caller last read, echoed back for the
// optimistic-concurrency check that fixes issue #97. When it no longer matches
// the row, nothing is written and ErrStaleVersion is returned: the caller has
// not seen a watched toggle that happened elsewhere, so its position is a stale
// value that would otherwise be written on top of a deliberately zeroed one. A
// nil expectVersion skips the check entirely, which keeps every non-Player
// caller — and any older cached SPA bundle — working exactly as before.
//
// Two asymmetries worth stating, both deliberate:
//
//   - The check is "!=", not "<". Behind and ahead are both "not the version I
//     validated against"; an ahead-version echo can only come from a client that
//     read a row this one has never seen.
//   - Only a genuine unwatched -> watched transition bumps state_version. A
//     plain position write is not a watched-state transition, and bumping on
//     every resume ping would invalidate every other client's echo instantly
//     (see migration 0010). That includes the pings AFTER the threshold has
//     already been crossed: every one of them is "auto-watched" by the ratio
//     test, so bumping on the ratio rather than on the transition would turn the
//     whole last 10% of a video into exactly the 409 storm 0010 rules out. Hence
//     the `watched = 0` guard on the bump.
//
// Returns the row's state_version after the write and whether the video is
// watched, so the caller can hand both back to the client that just pinged —
// without that, a client whose own ping crossed the 90% threshold would echo a
// version the auto-watch had just bumped and 409 against itself.
func (s *Store) SetResume(id string, position float64, expectVersion *int64) (int64, bool, error) {
	if position < 0 {
		position = 0
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, fmt.Errorf("set video %s resume: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	var duration sql.NullInt64
	var version int64
	var watched int
	if err := tx.QueryRowContext(ctx,
		`SELECT duration_seconds, state_version, watched FROM videos WHERE id = ?`, id,
	).Scan(&duration, &version, &watched); err != nil {
		if err == sql.ErrNoRows {
			return 0, false, fmt.Errorf("set video %s resume: not found", id)
		}
		return 0, false, fmt.Errorf("set video %s resume: %w", id, err)
	}

	if expectVersion != nil && *expectVersion != version {
		return version, watched != 0, ErrStaleVersion
	}

	autoWatched := duration.Valid && duration.Int64 > 0 &&
		position >= watchedThreshold*float64(duration.Int64)

	if autoWatched {
		err = tx.QueryRowContext(ctx, `
UPDATE videos
SET resume_position_seconds = ?, watched = 1, watched_at = COALESCE(watched_at, datetime('now')),
	state_version = state_version + (CASE WHEN watched = 0 THEN 1 ELSE 0 END)
WHERE id = ?
RETURNING state_version`, position, id).Scan(&version)
		watched = 1
	} else {
		_, err = tx.ExecContext(ctx,
			`UPDATE videos SET resume_position_seconds = ? WHERE id = ?`, position, id)
	}
	if err != nil {
		return 0, false, fmt.Errorf("set video %s resume: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("set video %s resume: %w", id, err)
	}
	return version, watched != 0, nil
}

// SetResumeRaw sets a video's resume position WITHOUT the >=90% auto-watch that
// SetResume applies, and without the state_version concurrency check. It was
// written for the TubeArchivist import, so a partially-watched "continue" video
// imported at, say, 92% kept its position and stayed in the Continue Watching
// queue rather than being flipped to watched (which would have dropped it out of
// exactly the queue the migration existed to preserve). That importer was
// deleted in PR #125, so this has no production caller left: it survives as the
// discriminator in store_test.go proving SetResume's auto-watch is SetResume's
// doing and not the bare UPDATE's. It errors on a missing row, so callers must
// Upsert the video first.
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

// RestartRetentionClock stamps watched_at to now on a watched video, leaving
// watched and resume_position_seconds alone. It exists for the re-download
// path: a restored video needs its full retention_days again, or the next
// hourly sweep would reclaim the file it just fetched.
//
// The alternative — marking the video unwatched, which is what re-download used
// to do — bought the same rescue by rewriting history. Watched-ness and whether
// the file is here are different facts: a video you watched and deleted is still
// a video you watched, and a video that was NEVER watched can be tombstoned too
// (the manual Delete does not ask), so there was nothing to un-watch in that
// case anyway.
//
// No state_version bump: watched_at is not part of the playback state clients
// echo back through SetResume, so nobody's version needs invalidating.
// Restricted to watched rows, so an unwatched video does not acquire a
// watched_at it never earned.
func (s *Store) RestartRetentionClock(id string) error {
	if _, err := s.db.ExecContext(context.Background(),
		`UPDATE videos SET watched_at = datetime('now') WHERE id = ? AND watched = 1`, id); err != nil {
		return fmt.Errorf("restart retention clock for video %s: %w", id, err)
	}
	return nil
}

// SweepCandidates returns videos eligible for the retention sweeper
// (Task 12): downloaded, watched, not favorited, and last watched strictly
// before cutoff (an absolute point in time, formatted
// "2006-01-02 15:04:05" UTC to match the format datetime('now') stores in
// watched_at — the caller computes cutoff from settings.RetentionDays and
// its own clock, so the sweeper stays testable without depending on
// SQLite's notion of "now"). Oldest-watched first, so the sweeper's log
// order reads chronologically.
//
// status = 'downloaded' rather than status != 'tombstoned': there is only ever
// something to reclaim from a video that HAS a file. The looser form also
// matched a watched row that was queued, downloading or errored, so a
// re-download of a long-ago-watched video could be flipped straight back to
// 'tombstoned' while its job was still in flight — see handleRedownloadVideo,
// which restarts the retention clock and relies on this to make the restore
// stick.
func (s *Store) SweepCandidates(cutoffUTC string) ([]Video, error) {
	rows, err := s.db.QueryContext(context.Background(),
		"SELECT "+videoColumns+" "+videoFrom+`
WHERE v.watched = 1 AND v.favorite = 0 AND v.status = 'downloaded' AND v.watched_at < ?
ORDER BY v.watched_at ASC`, cutoffUTC,
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
