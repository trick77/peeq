// Package settings persists the single-user settings singleton row (see
// migration 0001_init.sql). The pasted YouTube cookie is write-only: this
// package deliberately has no exported accessor that returns the raw cookie
// text, so it can never be serialized back out over the API (see Settings).
package settings

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/trick77/peeq/internal/cookie"
)

// DBTX is the subset of *sql.DB used by the settings store.
type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
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
	YTDLPVersion            string `json:"ytdlp_version"`
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
}

// Store persists the settings singleton row.
type Store struct {
	db DBTX
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
	err := s.db.QueryRowContext(ctx, `
SELECT cookie_status, cookie_updated_at, format_preset, format_custom, limit_rate,
       throttle_base_seconds, retention_days, min_free_gb, min_video_duration_seconds, ytdlp_version
FROM settings
WHERE id = 1`,
	).Scan(
		&st.CookieStatus, &cookieUpdatedAt, &st.FormatPreset, &st.FormatCustom, &st.LimitRate,
		&st.ThrottleBaseSeconds, &st.RetentionDays, &st.MinFreeGB, &st.MinVideoDurationSeconds, &st.YTDLPVersion,
	)
	if err != nil {
		return Settings{}, fmt.Errorf("get settings: %w", err)
	}
	if cookieUpdatedAt.Valid {
		st.CookieUpdatedAt = cookieUpdatedAt.String
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
    min_video_duration_seconds = COALESCE(?, min_video_duration_seconds)
WHERE id = 1`,
		patch.FormatPreset, patch.FormatCustom, patch.LimitRate,
		patch.ThrottleBaseSeconds, patch.RetentionDays, patch.MinFreeGB, patch.MinVideoDurationSeconds,
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
		return nil
	}

	// Empty text: status-only update, cookie body and previous timestamp are
	// left untouched.
	_, err := s.db.ExecContext(ctx, `
UPDATE settings
SET cookie_status = ?
WHERE id = 1`,
		status,
	)
	if err != nil {
		return fmt.Errorf("set cookie status: %w", err)
	}
	return nil
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
		return "", "absent"
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
		return "absent"
	}
	return status
}
