// Package settings persists the single-user settings singleton row (see
// migration 0001_init.sql). The pasted YouTube cookie is write-only: this
// package deliberately has no exported accessor that returns the raw cookie
// text, so it can never be serialized back out over the API (see Settings).
package settings

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/cookie"
)

// DBTX is the subset of *sql.DB used by the settings store.
type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ActivityRecorder records a cookie/access transition for the Activity feed.
// Narrow and nil-safe like the download/scan/summarize workers' own recorders;
// nil in tests, the shared *activity.Store in prod.
type ActivityRecorder interface {
	Record(activity.Event)
}

// Settings is the non-secret view of the settings singleton row.
// Intentionally has no field for cookie_text: the pasted cookie is
// write-only over the API, so it must never be a value this struct can hand
// back to a JSON encoder.
type Settings struct {
	CookieStatus            string `json:"cookie_status"`
	CookieUpdatedAt         string `json:"cookie_updated_at,omitempty"`
	FormatPreset            string `json:"format_preset"`
	FormatCustom            string `json:"format_custom"`
	LimitRate               string `json:"limit_rate"`
	ThrottleBaseSeconds     int    `json:"throttle_base_seconds"`
	RetentionDays           int    `json:"retention_days"`
	MinFreeGB               int    `json:"min_free_gb"`
	MinVideoDurationSeconds int    `json:"min_video_duration_seconds"`
	SubtitlesDefault        bool   `json:"subtitles_default"`
	YTDLPVersion            string `json:"ytdlp_version"`
	YoutubePaused           bool   `json:"youtube_paused"`
	YoutubePauseReason      string `json:"youtube_pause_reason"`
	YoutubePausedAt         string `json:"youtube_paused_at,omitempty"`
}

// Patch is a partial update to the non-secret settings fields. Nil fields
// are left unchanged.
type Patch struct {
	FormatPreset            *string
	FormatCustom            *string
	LimitRate               *string
	ThrottleBaseSeconds     *int
	RetentionDays           *int
	MinFreeGB               *int
	MinVideoDurationSeconds *int
	// SubtitlesDefault is the first bool in Patch. database/sql maps a nil
	// *bool to NULL (so COALESCE leaves the column alone) and a non-nil one
	// to 0/1, exactly like the *int fields above.
	SubtitlesDefault *bool
}

// Store persists the settings singleton row.
type Store struct {
	db DBTX
	// Activity, when set, records cookie/access state transitions for the
	// Activity feed. Set post-construction in main.go (like activity.Store's own
	// OnRecord); nil disables recording, so every caller and test is safe.
	Activity ActivityRecorder
}

// New returns a settings store backed by db.
func New(db DBTX) *Store {
	return &Store{db: db}
}

// Get returns the current non-secret settings. Never returns the cookie
// body.
func (s *Store) Get(ctx context.Context) (Settings, error) {
	var st Settings
	var cookieUpdatedAt sql.NullString
	var pausedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
SELECT cookie_status, cookie_updated_at, format_preset, format_custom, limit_rate,
       throttle_base_seconds, retention_days, min_free_gb, min_video_duration_seconds,
       subtitles_default, ytdlp_version,
       youtube_paused, youtube_pause_reason, youtube_paused_at
FROM settings
WHERE id = 1`,
	).Scan(
		&st.CookieStatus, &cookieUpdatedAt, &st.FormatPreset, &st.FormatCustom, &st.LimitRate,
		&st.ThrottleBaseSeconds, &st.RetentionDays, &st.MinFreeGB, &st.MinVideoDurationSeconds,
		&st.SubtitlesDefault, &st.YTDLPVersion,
		&st.YoutubePaused, &st.YoutubePauseReason, &pausedAt,
	)
	if err != nil {
		return Settings{}, fmt.Errorf("get settings: %w", err)
	}
	if cookieUpdatedAt.Valid {
		st.CookieUpdatedAt = cookieUpdatedAt.String
	}
	if pausedAt.Valid {
		st.YoutubePausedAt = pausedAt.String
	}
	return st, nil
}

// Update applies patch to the settings row. Nil fields in patch are left
// untouched.
func (s *Store) Update(ctx context.Context, patch Patch) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE settings
SET format_preset         = COALESCE(?, format_preset),
    format_custom         = COALESCE(?, format_custom),
    limit_rate            = COALESCE(?, limit_rate),
    throttle_base_seconds = COALESCE(?, throttle_base_seconds),
    retention_days        = COALESCE(?, retention_days),
    min_free_gb           = COALESCE(?, min_free_gb),
    min_video_duration_seconds = COALESCE(?, min_video_duration_seconds),
    subtitles_default     = COALESCE(?, subtitles_default)
WHERE id = 1`,
		patch.FormatPreset, patch.FormatCustom, patch.LimitRate,
		patch.ThrottleBaseSeconds, patch.RetentionDays, patch.MinFreeGB, patch.MinVideoDurationSeconds,
		patch.SubtitlesDefault,
	)
	if err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	return nil
}

// SetCookie validates text as a Netscape cookie file before persisting
// anything. On success it stores the cookie body, forces cookie_status to
// "valid" (the status parameter is only used when text is empty, letting
// callers flip status — e.g. to "blocked" after a failed download — without
// touching the stored cookie), and stamps cookie_updated_at with the current
// time. On validation failure, nothing is written and the error is
// returned as-is so callers (the HTTP handler) can surface it to the user.
func (s *Store) SetCookie(ctx context.Context, text string, status string) error {
	if text != "" {
		if err := cookie.Validate(text); err != nil {
			return err
		}
		// Read the status this write is about to replace, so the access row below
		// records a genuine state change and not the mere fact of a re-paste.
		old := s.CookieStatus(ctx)
		status = "valid"
		_, err := s.db.ExecContext(ctx, `
UPDATE settings
SET cookie_text = ?, cookie_status = ?, cookie_updated_at = datetime('now')
WHERE id = 1`,
			text, status,
		)
		if err != nil {
			return fmt.Errorf("set cookie: %w", err)
		}
		s.recordAccessTransition(old, status)
		return nil
	}

	// Empty text: status-only update, cookie body and previous timestamp are
	// left untouched.
	old := s.CookieStatus(ctx)
	_, err := s.db.ExecContext(ctx, `
UPDATE settings
SET cookie_status = ?
WHERE id = 1`,
		status,
	)
	if err != nil {
		return fmt.Errorf("set cookie status: %w", err)
	}
	s.recordAccessTransition(old, status)
	return nil
}

// recordAccessTransition records an Activity row for a genuine cookie-status
// change (old != new). It is the "silence rule" chokepoint: the callers that
// flip a dead cookie to "blocked"/"stale" (scan scheduler, download worker) do
// so with an UNCONDITIONAL write on every failed pass, so without the old != new
// guard a persistently-blocked cookie would emit an identical warn row every
// poll. A no-op re-paste of a valid cookie (valid -> valid) likewise records
// nothing. Best-effort and nil-safe: it never fails, and thus never fails the
// SetCookie caller.
func (s *Store) recordAccessTransition(old, newStatus string) {
	if s.Activity == nil || old == newStatus {
		return
	}
	var e activity.Event
	switch newStatus {
	case "valid":
		e = activity.Event{Kind: activity.KindAccess, Outcome: activity.OutcomeOK,
			Summary: "YouTube access restored"}
	case CookieBlocked:
		e = activity.Event{Kind: activity.KindAccess, Outcome: activity.OutcomeWarn,
			Summary: "YouTube blocked the request"}
	case CookieStale:
		e = activity.Event{Kind: activity.KindAccess, Outcome: activity.OutcomeWarn,
			Summary: "cookie expired"}
	default:
		// Other transitions (e.g. -> "absent") are not access events worth a row.
		return
	}
	s.Activity.Record(e)
}

// CookieCredentials returns the raw cookie text and status, for wiring the
// yt-dlp Runner's CookieProvider. Unlike Get/Settings (which back the JSON
// settings API and deliberately never carry the cookie body), this is for
// internal server-side use only and must never be exposed over HTTP.
// Returns ("", "absent") if the read fails, so a Runner using this as its
// CookieProvider fails safe (refuses to run) rather than panics.
func (s *Store) CookieCredentials(ctx context.Context) (text string, status string) {
	err := s.db.QueryRowContext(ctx, `SELECT cookie_text, cookie_status FROM settings WHERE id = 1`).Scan(&text, &status)
	if err != nil {
		return "", CookieAbsent
	}
	return text, status
}

// CookieStatus returns the current cookie_status without loading the rest
// of the settings row. Returns "absent" if the read fails, so callers that
// only gate on "is there a usable cookie" fail safe rather than panic.
func (s *Store) CookieStatus(ctx context.Context) string {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT cookie_status FROM settings WHERE id = 1`).Scan(&status)
	if err != nil {
		return CookieAbsent
	}
	return status
}

// SetYoutubePaused sets or clears the global YouTube kill-switch. reason is
// the auto-pause explanation (” for a manual pause). When pausing, the
// timestamp is stamped; when resuming, all three columns are cleared.
func (s *Store) SetYoutubePaused(ctx context.Context, paused bool, reason string) error {
	if !paused {
		_, err := s.db.ExecContext(ctx, `
UPDATE settings SET youtube_paused = 0, youtube_pause_reason = '', youtube_paused_at = NULL WHERE id = 1`)
		if err != nil {
			return fmt.Errorf("resume youtube: %w", err)
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
UPDATE settings SET youtube_paused = 1, youtube_pause_reason = ?, youtube_paused_at = datetime('now') WHERE id = 1`, reason)
	if err != nil {
		return fmt.Errorf("pause youtube: %w", err)
	}
	return nil
}

// YoutubePaused reports the kill-switch state for the Runner pause-gate and
// the worker/scan poll-gates. Fails safe to not-paused on read error (a DB
// blip must never silently freeze all downloads forever).
func (s *Store) YoutubePaused(ctx context.Context) (bool, string) {
	var paused bool
	var reason string
	err := s.db.QueryRowContext(ctx,
		`SELECT youtube_paused, youtube_pause_reason FROM settings WHERE id = 1`).Scan(&paused, &reason)
	if err != nil {
		return false, ""
	}
	return paused, reason
}

// SetAPITokenHash stores the token hash and stamps api_token_created_at,
// returning the stamp. Returning it from the same statement (rather than
// re-reading it) means a create can never half-succeed: previously a failed
// follow-up read left the new hash live while the caller got an error and
// never saw the plaintext, locking the user out of their own token.
func (s *Store) SetAPITokenHash(ctx context.Context, hash string) (string, error) {
	var createdAt string
	err := s.db.QueryRowContext(ctx, `
UPDATE settings
SET api_token_hash = ?, api_token_created_at = datetime('now')
WHERE id = 1
RETURNING api_token_created_at`, hash).Scan(&createdAt)
	if err != nil {
		return "", fmt.Errorf("set api token hash: %w", err)
	}
	return createdAt, nil
}

// APITokenHash returns the stored token hash for the RequireToken
// middleware. Returns "" if unset or on read error, so an unconfigured or
// unreadable peeq fails safe: an empty hash never authenticates anything.
func (s *Store) APITokenHash(ctx context.Context) string {
	var hash string
	err := s.db.QueryRowContext(ctx, `SELECT api_token_hash FROM settings WHERE id = 1`).Scan(&hash)
	if err != nil {
		return ""
	}
	return hash
}

// APITokenInfo reports whether a token exists and when it was created,
// without exposing the hash. This backs GET /api/settings/token, which must
// never return anything secret.
func (s *Store) APITokenInfo(ctx context.Context) (bool, string, error) {
	var hash string
	var createdAt sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT api_token_hash, api_token_created_at FROM settings WHERE id = 1`,
	).Scan(&hash, &createdAt)
	if err != nil {
		return false, "", fmt.Errorf("get api token info: %w", err)
	}
	if !createdAt.Valid {
		return hash != "", "", nil
	}
	return hash != "", createdAt.String, nil
}
