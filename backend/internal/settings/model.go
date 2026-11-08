package settings

// Settings is the non-secret view of the settings singleton row.
// Intentionally has no field for cookie_text: the pasted cookie is
// write-only over the API, so it must never be a value this struct can hand
// back to a JSON encoder.
//
// Table: settings
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
	DirectStreamEnabled     bool   `json:"direct_stream_enabled"`
	YtdlpIPFamily           string `json:"ytdlp_ip_family"`
	YTDLPVersion            string `json:"ytdlp_version"`
	YoutubePaused           bool   `json:"youtube_paused"`
	YoutubePauseReason      string `json:"youtube_pause_reason"`
	YoutubePausedAt         string `json:"youtube_paused_at,omitempty"`
}
