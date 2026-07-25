package ytdlp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ChannelEntry is one video from a flat channel listing. Flat entries are
// metadata-poor by design (no description/availability): only the fields
// below are reliably present, and ThumbnailURL is a REMOTE url, never a
// local path.
//
// PublishedAt is YYYY-MM-DD and APPROXIMATE (see approximateDateArgs); it is
// "" when the listing carried no timestamp. It must never be written to
// videos.published_at, which holds the exact upload_date from a real
// per-video metadata call made after download.
type ChannelEntry struct {
	ID              string
	Title           string
	URL             string
	DurationSeconds int
	ThumbnailURL    string
	LiveStatus      string
	PublishedAt     string
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
	Timestamp  int64   `json:"timestamp"`
	// ReleaseTimestamp is when a broadcast went live, and is the ONLY date a
	// /streams entry is guaranteed to carry: approximate_date derives timestamp
	// from the relative-time text on an upload card, and a stream card carries
	// "streamed 2 years ago" instead. Read as a fallback, never a replacement —
	// timestamp is the better answer whenever it is present.
	//
	// Without this fallback a stream VOD reaches the scanner with no date at
	// all, and every date-based decision there fails open. That is what turns a
	// missing field into a fifty-item inbox.
	ReleaseTimestamp int64 `json:"release_timestamp"`
	Thumbnails       []struct {
		URL string `json:"url"`
	} `json:"thumbnails"`
}

// approximateDateArgs opts the channel-tab extractor into reporting an upload
// date for flat entries. Without it, timestamp/upload_date/release_timestamp
// are all null in a --flat-playlist listing (verified against the /videos tab
// of a real channel); with it, timestamp is populated for ordinary uploads.
//
// It costs no extra request: the date is derived from the relative-time text
// ("2 weeks ago") already present in the tab response, which is also why it is
// APPROXIMATE — good to the day for recent uploads, coarser for old ones. That
// precision matches what the inbox card renders ("3 days ago"), and the exact
// date still arrives with the real metadata call once a video is downloaded.
var approximateDateArgs = []string{"--extractor-args", "youtubetab:approximate_date"}

// unixToDate converts yt-dlp's unix `timestamp` into the YYYY-MM-DD shape the
// rest of peeq stores dates in (matching normalizeUploadDate's output). UTC,
// so the same listing yields the same date regardless of host timezone.
// Returns "" for a missing or non-positive timestamp.
func unixToDate(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format("2006-01-02")
}

// firstTimestamp returns the first positive value, so a caller can express
// "prefer timestamp, fall back to release_timestamp" without repeating the
// >0 test that unixToDate already owns. Returns 0 when none is positive,
// which unixToDate renders as the honest "" (date unknown).
func firstTimestamp(candidates ...int64) int64 {
	for _, ts := range candidates {
		if ts > 0 {
			return ts
		}
	}
	return 0
}

type flatListing struct {
	ID          string         `json:"id"`
	Channel     string         `json:"channel"`
	Title       string         `json:"title"`
	Uploader    string         `json:"uploader"`
	UploaderID  string         `json:"uploader_id"`
	Ch          string         `json:"channel_id"`
	Description string         `json:"description"`
	Followers   int64          `json:"channel_follower_count"`
	Verified    bool           `json:"channel_is_verified"`
	Thumbnails  []channelThumb `json:"thumbnails"`
	Entries     []flatEntry    `json:"entries"`
}

// ChannelInfo is a channel's identity as resolved from a metadata-only
// yt-dlp call. AvatarURL and BannerURL are REMOTE urls; the caller decides
// whether to download them.
//
// Handle is the channel's own @handle as YouTube reports it, which is NOT
// the same source as the handle peeq stores when a user pastes a channel
// url — that one comes from the pasted text. This one is the only way a
// channel peeq never saw a url for (an import, or a channel discovered from
// a video) can get a handle at all.
//
// Subscribers is 0 when the field is absent, which reads as "not known"
// rather than "zero subscribers": YouTube omits it for channels that hide
// their count, and a hidden count must not be rendered as a real number.
type ChannelInfo struct {
	UCID        string
	Name        string
	Handle      string
	Description string
	Subscribers int64
	Verified    bool
	AvatarURL   string
	BannerURL   string
}

// channelThumb is one entry of the channel-level thumbnails array. Unlike a
// video's thumbnails, these carry an id naming the role ("avatar_uncropped",
// "banner_uncropped"), which is how the two are told apart — array order is
// not guaranteed and the array also holds cropped variants.
//
// The cropped variants are numbered rather than named, and Width/Height is
// how they are identified: see pickBanner.
type channelThumb struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// bannerMinRatio is how wide a thumbnail must be to count as a banner crop.
// YouTube's desktop banner is 2560x424 (~6:1) and its smaller variants keep
// that shape, while the avatar crops are square — 4:1 sits far from both.
const bannerMinRatio = 4

// pickBanner chooses the banner image to store from a channel's thumbnails.
//
// It deliberately prefers the widest NUMBERED crop over "banner_uncropped".
// The uncropped asset is the 16:9 original a creator uploads, most of which
// YouTube never shows: it crops to a ~6:1 strip, and the artwork is composed
// for that strip. Rendering the 16:9 version in peeq's header — a wide, short
// box — means cover-cropping to the middle of the image, which zooms into
// whatever happens to be at its centre instead of showing the composition.
// The numbered crops ARE that strip, so picking the largest of them gives the
// framing the channel intended.
//
// Falls back to banner_uncropped when no crop qualifies, so a channel whose
// thumbnails carry no dimensions still gets a banner.
func pickBanner(thumbs []channelThumb, uncropped string) string {
	best, bestWidth := "", 0
	for _, th := range thumbs {
		if th.URL == "" || th.Height <= 0 || th.Width <= bestWidth {
			continue
		}
		if th.Width/th.Height < bannerMinRatio {
			continue
		}
		best, bestWidth = th.URL, th.Width
	}
	if best != "" {
		return best
	}
	return uncropped
}

// ChannelVideos returns up to n most-recent uploads from a channel's /videos
// tab via a single flat-playlist call. The /videos tab carries ordinary uploads
// only — shorts and livestreams have their own tabs — so shorts stay excluded
// by construction, and stream VODs come from ChannelStreams instead.
//
// The mirror of ChannelStreams' warning applies here too: a channel whose
// output is entirely livestreams has no /videos tab, and yt-dlp fails this call
// outright (IsMissingTab recognises it). Callers must treat that as "no
// uploads", not as a broken scan.
func (r *Runner) ChannelVideos(ctx context.Context, ucid string, n int) ([]ChannelEntry, error) {
	return r.channelTab(ctx, ucid, "videos", n)
}

// ChannelStreams returns up to n most-recent entries from a channel's /streams
// tab — the livestream counterpart of ChannelVideos, same call shape, same flat
// listing. The tab mixes finished VODs with currently-live and scheduled items;
// LiveStatus is what tells them apart, and deciding which are worth recording
// is the caller's job (see scan.isUnfinishedStream).
//
// A channel that has never streamed has NO /streams tab, and yt-dlp fails the
// call outright (IsMissingTab recognises it). Callers must treat such a failure
// as "no streams", not as a broken scan.
func (r *Runner) ChannelStreams(ctx context.Context, ucid string, n int) ([]ChannelEntry, error) {
	return r.channelTab(ctx, ucid, "streams", n)
}

// channelTab is the shared body behind ChannelVideos/ChannelStreams: one flat
// listing of the newest n entries of a channel tab. It goes through the cookie
// gate + throttle like every other Runner call, so each tab costs its own
// throttle slot.
func (r *Runner) channelTab(ctx context.Context, ucid, tab string, n int) ([]ChannelEntry, error) {
	if err := r.pauseGate(); err != nil {
		return nil, err
	}

	cookieText, err := r.cookieGate()
	if err != nil {
		return nil, err
	}
	items := fmt.Sprintf(":%d:1", n)
	url := "https://www.youtube.com/channel/" + ucid + "/" + tab
	args := []string{"-J", "--flat-playlist", "--skip-download", "--playlist-items", items}
	args = append(args, approximateDateArgs...)
	out, err := r.exec(ctx, cookieText, append(args, url)...)
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
			PublishedAt:     unixToDate(firstTimestamp(e.Timestamp, e.ReleaseTimestamp)),
		})
	}
	return entries, nil
}

// channelHandle normalises yt-dlp's uploader_id into a peeq handle. yt-dlp
// reports it as "@tested" for a modern channel, but older channels have a
// legacy uploader_id that is a bare name or the UCID itself — neither is a
// handle, and rendering one as "@UCiDJ..." would produce a link to nothing.
// Only a value that already carries the @ is accepted; everything else
// yields "" and leaves whatever handle is already stored alone.
func channelHandle(uploaderID string) string {
	if len(uploaderID) < 2 || uploaderID[0] != '@' {
		return ""
	}
	return uploaderID
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
		Handle:      channelHandle(raw.UploaderID),
		Description: raw.Description,
		Subscribers: raw.Followers,
		Verified:    raw.Verified,
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
	uncroppedBanner := ""
	for _, th := range raw.Thumbnails {
		switch th.ID {
		case "avatar_uncropped":
			info.AvatarURL = th.URL
		case "banner_uncropped":
			uncroppedBanner = th.URL
		}
	}
	info.BannerURL = pickBanner(raw.Thumbnails, uncroppedBanner)
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
