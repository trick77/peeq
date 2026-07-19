package ytdlp

import (
	"context"
	"encoding/json"
	"fmt"
)

// Meta is the subset of yt-dlp's -J metadata JSON peeq needs.
type Meta struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	ChannelID       string `json:"channel_id"`
	Channel         string `json:"channel"`
	DurationSeconds int    `json:"duration_seconds"`
	Thumbnail       string `json:"thumbnail"`
	// PublishedAt is normalized to YYYY-MM-DD (yt-dlp reports upload_date
	// as YYYYMMDD).
	PublishedAt  string `json:"published_at"`
	Availability string `json:"availability"`
}

// ytdlpJSON mirrors the fields of yt-dlp's own -J JSON schema that Meta is
// built from. Field names/shapes intentionally match yt-dlp's output, not
// Meta's, since yt-dlp is the source of truth for this shape.
type ytdlpJSON struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	ChannelID    string  `json:"channel_id"`
	Channel      string  `json:"channel"`
	Duration     float64 `json:"duration"`
	Thumbnail    string  `json:"thumbnail"`
	UploadDate   string  `json:"upload_date"`
	Availability string  `json:"availability"`
}

// Metadata fetches metadata for a single video URL by running
// `<bin> -J --skip-download --no-playlist <watchURL>`. Every call first
// passes through the cookie gate (see cookieGate): if no cookie is
// configured, this returns ErrNoCookie and the binary is never invoked.
func (r *Runner) Metadata(ctx context.Context, rawURL string) (*Meta, error) {
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
		PublishedAt:     normalizeUploadDate(raw.UploadDate),
		Availability:    raw.Availability,
	}, nil
}

// normalizeUploadDate converts yt-dlp's YYYYMMDD upload_date into
// YYYY-MM-DD. Returns "" for anything that isn't exactly 8 digits.
func normalizeUploadDate(raw string) string {
	if len(raw) != 8 {
		return ""
	}
	return raw[0:4] + "-" + raw[4:6] + "-" + raw[6:8]
}
