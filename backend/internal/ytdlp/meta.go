package ytdlp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Meta is the subset of yt-dlp's -J metadata JSON peeq needs.
type Meta struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	ChannelID       string `json:"channel_id"`
	Channel         string `json:"channel"`
	DurationSeconds int    `json:"duration_seconds"`
	Thumbnail       string `json:"thumbnail"`
	Description     string `json:"description"`
	// PublishedAt is the AIR date, normalized to YYYY-MM-DD. See airDate for
	// which of yt-dlp's four date fields it comes from and in what order.
	PublishedAt  string `json:"published_at"`
	Availability string `json:"availability"`
	// Language is yt-dlp's reported audio/video language, if any. Not yet
	// consumed at Add time (see handleDownloadsPost) — the post-download
	// Result.AudioLanguage from Download's own *.info.json is the source
	// of truth stored on the video record.
	Language string `json:"language"`
	// MediaType/LiveStatus tell an ordinary upload from a Short from a
	// livestream ("video"/"short"/"livestream", and "was_live"/"is_live"/
	// "not_live"). Either can be "" — yt-dlp omits media_type on some
	// extractors.
	MediaType  string `json:"media_type"`
	LiveStatus string `json:"live_status"`
	// Tags/Categories are YouTube's OWN labels, not peeq's category enum
	// (internal/videos/category.go). Kept distinct everywhere they are stored.
	Tags       []string `json:"tags"`
	Categories []string `json:"categories"`
}

// ytdlpJSON mirrors the fields of yt-dlp's own -J JSON schema that Meta is
// built from. Field names/shapes intentionally match yt-dlp's output, not
// Meta's, since yt-dlp is the source of truth for this shape.
//
// -J returns the COMPLETE info dict, so every field here is free: adding one
// costs no extra request. The four date fields feed airDate; see it for why
// upload_date alone was not enough.
type ytdlpJSON struct {
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	ChannelID        string   `json:"channel_id"`
	Channel          string   `json:"channel"`
	Duration         float64  `json:"duration"`
	Thumbnail        string   `json:"thumbnail"`
	Description      string   `json:"description"`
	UploadDate       string   `json:"upload_date"`
	ReleaseDate      string   `json:"release_date"`
	ReleaseTimestamp int64    `json:"release_timestamp"`
	Timestamp        int64    `json:"timestamp"`
	Availability     string   `json:"availability"`
	Language         string   `json:"language"`
	MediaType        string   `json:"media_type"`
	LiveStatus       string   `json:"live_status"`
	Tags             []string `json:"tags"`
	Categories       []string `json:"categories"`
}

// Metadata fetches metadata for a single video URL by running
// `<bin> -J --skip-download --no-playlist <watchURL>`. Every call first
// passes through the cookie gate (see cookieGate): if no cookie is
// configured, this returns ErrNoCookie and the binary is never invoked.
func (r *Runner) Metadata(ctx context.Context, rawURL string) (*Meta, error) {
	if err := r.pauseGate(); err != nil {
		return nil, err
	}

	cookieText, err := r.cookieGate()
	if err != nil {
		return nil, err
	}

	watchURL, _, _, err := Canonicalize(rawURL)
	if err != nil {
		return nil, fmt.Errorf("ytdlp: canonicalize url: %w", err)
	}

	out, err := r.exec(ctx, cookieText, "-J", "--skip-download", "--no-playlist", watchURL)
	if err != nil {
		return nil, err
	}

	var raw ytdlpJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("ytdlp: parse metadata json: %w", err)
	}

	return &Meta{
		ID:              raw.ID,
		Title:           raw.Title,
		ChannelID:       raw.ChannelID,
		Channel:         raw.Channel,
		DurationSeconds: int(raw.Duration),
		Thumbnail:       raw.Thumbnail,
		Description:     raw.Description,
		PublishedAt: airDate(rawDates{
			ReleaseTimestamp: raw.ReleaseTimestamp,
			Timestamp:        raw.Timestamp,
			ReleaseDate:      raw.ReleaseDate,
			UploadDate:       raw.UploadDate,
		}),
		Availability: raw.Availability,
		Language:     raw.Language,
		MediaType:    raw.MediaType,
		LiveStatus:   raw.LiveStatus,
		Tags:         raw.Tags,
		Categories:   raw.Categories,
	}, nil
}

// normalizeUploadDate converts one of yt-dlp's YYYYMMDD date strings
// (upload_date, release_date) into YYYY-MM-DD. Returns "" for anything that is
// not a real calendar date — the length check alone used to let "abcdefgh"
// through as "abcd-ef-gh".
func normalizeUploadDate(raw string) string {
	t, err := time.Parse("20060102", raw)
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02")
}

// rawDates carries the four date fields yt-dlp reports, so airDate is called
// with NAMED fields rather than a positional argument list.
//
// That is deliberate. The precedence below is release_timestamp, release_date,
// timestamp, upload_date — which is neither the order a positional signature
// would group them in (the two int64s adjacent) nor an order anyone recalls
// from memory. Swapping two same-typed arguments would still compile and would
// go wrong only for premieres: the one case this whole mechanism exists to get
// right, and the one least likely to be noticed.
type rawDates struct {
	ReleaseTimestamp int64
	Timestamp        int64
	ReleaseDate      string
	UploadDate       string
}

// airDate resolves when a video actually AIRED, in descending order of
// trustworthiness. Returns "" only when all four fields are absent.
//
// Release beats upload: for a premiere or a scheduled livestream, upload_date
// is when the file was staged — often days earlier, occasionally years — while
// release_* is when it went live to viewers. Sorting a premiere by its upload
// date puts it in the wrong place in the library, and premieres are exactly
// the videos that used to arrive with no usable date at all.
//
// The unix fields carry a time of day, but the result is deliberately truncated
// to YYYY-MM-DD: published_at is compared as TEXT, and a column mixing
// '2026-03-01' with '2026-03-01 09:00:00' sorts the date-only value first on a
// same-day tie. One shape, or ordering quietly breaks.
func airDate(d rawDates) string {
	if s := unixToDate(d.ReleaseTimestamp); s != "" {
		return s
	}
	if s := normalizeUploadDate(d.ReleaseDate); s != "" {
		return s
	}
	if s := unixToDate(d.Timestamp); s != "" {
		return s
	}
	return normalizeUploadDate(d.UploadDate)
}
