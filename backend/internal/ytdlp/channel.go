package ytdlp

import (
	"context"
	"encoding/json"
	"fmt"
)

// ChannelEntry is one video from a flat channel listing. Flat entries are
// metadata-poor by design (no description/published_at/availability): only
// the fields below are reliably present, and ThumbnailURL is a REMOTE url,
// never a local path.
type ChannelEntry struct {
	ID              string
	Title           string
	URL             string
	DurationSeconds int
	ThumbnailURL    string
	LiveStatus      string
}

// flatEntry mirrors one yt-dlp flat-playlist entry. duration is a float
// (yt-dlp emits it as a number, and may omit it entirely in flat mode — in
// which case DurationSeconds is 0 and the caller's `<min` filter fails open).
type flatEntry struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Duration   float64 `json:"duration"`
	LiveStatus string  `json:"live_status"`
	Thumbnails []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

type flatListing struct {
	ID          string         `json:"id"`
	Channel     string         `json:"channel"`
	Title       string         `json:"title"`
	Uploader    string         `json:"uploader"`
	Ch          string         `json:"channel_id"`
	Description string         `json:"description"`
	Thumbnails  []channelThumb `json:"thumbnails"`
	Entries     []flatEntry    `json:"entries"`
}

// ChannelInfo is a channel's identity as resolved from a metadata-only
// yt-dlp call. AvatarURL and BannerURL are REMOTE urls; the caller decides
// whether to download them.
type ChannelInfo struct {
	UCID        string
	Name        string
	Description string
	AvatarURL   string
	BannerURL   string
}

// channelThumb is one entry of the channel-level thumbnails array. Unlike a
// video's thumbnails, these carry an id naming the role ("avatar_uncropped",
// "banner_uncropped"), which is how the two are told apart — array order is
// not guaranteed and the array also holds cropped variants.
type channelThumb struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// ChannelVideos returns up to n most-recent uploads from a channel's /videos
// tab via a single flat-playlist call. Querying only the /videos tab means
// shorts and livestreams (separate tabs) are excluded by construction. The
// call goes through the cookie gate + throttle like every other Runner call.
func (r *Runner) ChannelVideos(ctx context.Context, ucid string, n int) ([]ChannelEntry, error) {
	if err := r.pauseGate(); err != nil {
		return nil, err
	}

	cookieText, err := r.cookieGate()
	if err != nil {
		return nil, err
	}
	items := fmt.Sprintf(":%d:1", n)
	url := "https://www.youtube.com/channel/" + ucid + "/videos"
	out, err := r.exec(ctx, cookieText, "-J", "--flat-playlist", "--skip-download", "--playlist-items", items, url)
	if err != nil {
		return nil, err
	}
	var raw flatListing
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("ytdlp: parse channel listing json: %w", err)
	}
	entries := make([]ChannelEntry, 0, len(raw.Entries))
	for _, e := range raw.Entries {
		if e.ID == "" {
			continue
		}
		thumb := ""
		if len(e.Thumbnails) > 0 {
			thumb = e.Thumbnails[len(e.Thumbnails)-1].URL
		}
		entries = append(entries, ChannelEntry{
			ID:              e.ID,
			Title:           e.Title,
			URL:             "https://www.youtube.com/watch?v=" + e.ID,
			DurationSeconds: int(e.Duration),
			ThumbnailURL:    thumb,
			LiveStatus:      e.LiveStatus,
		})
	}
	return entries, nil
}

// parseChannelInfo extracts a ChannelInfo from a yt-dlp metadata-only
// channel response. Split out from ResolveChannel so it is testable without
// shelling out.
func parseChannelInfo(out []byte) (ChannelInfo, error) {
	var raw flatListing
	if err := json.Unmarshal(out, &raw); err != nil {
		return ChannelInfo{}, fmt.Errorf("ytdlp: parse channel json: %w", err)
	}
	info := ChannelInfo{
		UCID:        raw.Ch,
		Name:        raw.Channel,
		Description: raw.Description,
	}
	if info.UCID == "" {
		info.UCID = raw.ID
	}
	if info.Name == "" {
		info.Name = raw.Uploader
	}
	if info.Name == "" {
		info.Name = raw.Title
	}
	for _, th := range raw.Thumbnails {
		switch th.ID {
		case "avatar_uncropped":
			info.AvatarURL = th.URL
		case "banner_uncropped":
			info.BannerURL = th.URL
		}
	}
	if info.UCID == "" {
		return ChannelInfo{}, fmt.Errorf("ytdlp: could not resolve channel id")
	}
	return info, nil
}

// ResolveChannel resolves a channel URL (a @handle, /c/, /user/, or /channel/
// URL) to its full identity — UCID, display name, description, avatar, and
// banner — via a metadata-only flat call (--playlist-items 0 fetches no
// entries). Used at explicit channel-add time.
func (r *Runner) ResolveChannel(ctx context.Context, channelURL string) (ChannelInfo, error) {
	if perr := r.pauseGate(); perr != nil {
		return ChannelInfo{}, perr
	}

	cookieText, gerr := r.cookieGate()
	if gerr != nil {
		return ChannelInfo{}, gerr
	}
	out, xerr := r.exec(ctx, cookieText, "-J", "--flat-playlist", "--skip-download", "--playlist-items", "0", channelURL)
	if xerr != nil {
		return ChannelInfo{}, xerr
	}
	info, err := parseChannelInfo(out)
	if err != nil {
		return ChannelInfo{}, fmt.Errorf("%w (url %q)", err, channelURL)
	}
	return info, nil
}
