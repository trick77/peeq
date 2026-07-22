// Package taimport reads an existing TubeArchivist library over TubeArchivist's
// REST API so it can be migrated into peeq. It is a one-shot migration helper,
// not a sync: it has no delta tracking and is expected to be run by hand from
// the CLI while the peeq server is stopped.
package taimport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Channel is the subset of a TubeArchivist channel document this migration
// needs. TubeArchivist stores considerably more (description, subscriber
// count, tags, thumbnail/banner/TV-art URLs, per-channel download overrides);
// none of it has a home in peeq's channels table, so none of it is decoded.
//
// Note there is no handle: TubeArchivist does not store the @handle at all.
// peeq's channels.handle is therefore left empty by this import, which is
// already a normal state (it is parsed best-effort from a pasted URL in
// httpapi/channels_handlers.go and is omitempty in the API).
type Channel struct {
	ID         string
	Name       string
	Active     bool // false once the channel is gone from YouTube
	Subscribed bool
}

// channelDoc is the wire shape of one entry in TubeArchivist's /api/channel/
// response.
type channelDoc struct {
	ID         string `json:"channel_id"`
	Name       string `json:"channel_name"`
	Active     bool   `json:"channel_active"`
	Subscribed bool   `json:"channel_subscribed"`
}

// pageEnvelope is TubeArchivist's list-response wrapper. It also carries a
// "paginate" block, deliberately not decoded: its page size comes from the
// TubeArchivist user's own config and its last-page arithmetic degrades once
// Elasticsearch's 10k result ceiling is hit. Walking until a page comes back
// empty is both simpler and more robust.
type pageEnvelope struct {
	Data []channelDoc `json:"data"`
}

// Client talks to a TubeArchivist instance's REST API.
type Client struct {
	baseURL string
	token   string
	hc      *http.Client
}

// NewClient returns a Client for the TubeArchivist instance at baseURL (e.g.
// "http://tubearchivist:8000"), authenticating with token. Pass nil for hc to
// get a client with a sane timeout.
func NewClient(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		hc:      hc,
	}
}

// do issues an authenticated GET and decodes the JSON body into out. It
// reports (found=false) for HTTP 404, which TubeArchivist returns for an empty
// result set rather than an empty list.
func (c *Client) do(ctx context.Context, path string, query url.Values, out any) (found bool, err error) {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, fmt.Errorf("taimport: build request %s: %w", path, err)
	}
	// TubeArchivist runs Django REST Framework, whose TokenAuthentication
	// expects the "Token" keyword. "Bearer" is silently unauthenticated.
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("taimport: GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// Empty result set, not a failure.
		return false, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return false, fmt.Errorf("taimport: GET %s: %s — check the TubeArchivist API token", path, resp.Status)
	case resp.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return false, fmt.Errorf("taimport: GET %s: %s: %s", path, resp.Status, strings.TrimSpace(string(body)))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return false, fmt.Errorf("taimport: decode %s: %w", path, err)
	}
	return true, nil
}

// ChannelPage fetches one page of subscribed channels. more reports whether
// the page had any entries, i.e. whether it is worth asking for the next one.
//
// The filter=subscribed query is load-bearing, not a convenience:
// TubeArchivist indexes a channel document for every video ever downloaded,
// including one-off downloads from channels that were never followed.
// Importing unfiltered would create peeq subscriptions for all of them.
func (c *Client) ChannelPage(ctx context.Context, page int) (chans []Channel, more bool, err error) {
	q := url.Values{}
	q.Set("filter", "subscribed")
	q.Set("page", strconv.Itoa(page))

	var env pageEnvelope
	found, err := c.do(ctx, "/api/channel/", q, &env)
	if err != nil {
		return nil, false, err
	}
	if !found || len(env.Data) == 0 {
		return nil, false, nil
	}

	out := make([]Channel, 0, len(env.Data))
	for _, d := range env.Data {
		out = append(out, Channel{
			ID:         d.ID,
			Name:       d.Name,
			Active:     d.Active,
			Subscribed: d.Subscribed,
		})
	}
	return out, true, nil
}

// maxChannelPages bounds the pagination walk. TubeArchivist's default page
// size is 25, so this allows 50k subscribed channels — far beyond any real
// library, while still guaranteeing the loop terminates if a misbehaving
// instance or proxy never returns an empty page.
const maxChannelPages = 2000

// AllChannels pages through every subscribed channel, in the order
// TubeArchivist returns them.
func (c *Client) AllChannels(ctx context.Context) ([]Channel, error) {
	var all []Channel
	for page := 1; page <= maxChannelPages; page++ {
		batch, more, err := c.ChannelPage(ctx, page)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if !more {
			return all, nil
		}
	}
	return nil, fmt.Errorf("taimport: channel listing exceeded %d pages; refusing to keep paging", maxChannelPages)
}

// Video is the subset of a TubeArchivist video document the migration needs.
// Paths are rebuilt from ChannelID+ID (see PathMapper), never from the API's
// media_url, so no URL field is decoded.
type Video struct {
	ID              string
	ChannelID       string
	ChannelName     string
	Title           string
	Published       string  // normalized YYYY-MM-DD
	DurationSeconds int     // player.duration
	Position        float64 // player.position — resume seconds; 0 when not partially watched
	VidType         string  // videos | shorts | streams
	SubtitleLangs   []string
}

// flexDate decodes TubeArchivist's "published" field. Its REST layer normally
// returns a normalized "YYYY-MM-DD" string, but it can appear as an epoch
// integer on some paths; either form yields a YYYY-MM-DD string so a one-shot
// migration never aborts on a type it did not expect.
type flexDate string

func (d *flexDate) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	switch {
	case s == "" || s == "null":
		*d = ""
		return nil
	case s[0] == '"':
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		*d = flexDate(str)
		return nil
	default:
		var epoch int64
		if err := json.Unmarshal(b, &epoch); err != nil {
			return err
		}
		*d = flexDate(time.Unix(epoch, 0).UTC().Format("2006-01-02"))
		return nil
	}
}

// videoDoc is the wire shape of one entry in /api/video/. Only the fields the
// migration writes are decoded; channel id/name are nested, and the resume
// position, duration and watched state live under player.
type videoDoc struct {
	YoutubeID string   `json:"youtube_id"`
	Title     string   `json:"title"`
	Published flexDate `json:"published"`
	VidType   string   `json:"vid_type"`
	Channel   struct {
		ID   string `json:"channel_id"`
		Name string `json:"channel_name"`
	} `json:"channel"`
	Player struct {
		Duration int     `json:"duration"`
		Position float64 `json:"position"`
	} `json:"player"`
	Subtitles []struct {
		Lang string `json:"lang"`
	} `json:"subtitles"`
}

func (d videoDoc) toVideo() Video {
	langs := make([]string, 0, len(d.Subtitles))
	for _, s := range d.Subtitles {
		if s.Lang != "" {
			langs = append(langs, s.Lang)
		}
	}
	return Video{
		ID:              d.YoutubeID,
		ChannelID:       d.Channel.ID,
		ChannelName:     d.Channel.Name,
		Title:           d.Title,
		Published:       string(d.Published),
		DurationSeconds: d.Player.Duration,
		Position:        d.Player.Position,
		VidType:         d.VidType,
		SubtitleLangs:   langs,
	}
}

// videoEnvelope is the list wrapper for /api/video/, mirroring pageEnvelope.
type videoEnvelope struct {
	Data []videoDoc `json:"data"`
}

// VideoPage fetches one page of a channel's videos filtered by watch state
// (watch is "unwatched" or "continue"). more reports whether the page had
// entries. Mirrors ChannelPage; the video API's filter key is "watch" (not
// "filter"), and the crawl is sharded by channel to stay under Elasticsearch's
// 10k-result ceiling.
func (c *Client) VideoPage(ctx context.Context, channelID, watch string, page int) (vids []Video, more bool, err error) {
	q := url.Values{}
	q.Set("channel", channelID)
	q.Set("watch", watch)
	q.Set("page", strconv.Itoa(page))

	var env videoEnvelope
	found, err := c.do(ctx, "/api/video/", q, &env)
	if err != nil {
		return nil, false, err
	}
	// The video list endpoint ends pages with 200 + empty data (only the
	// single-video GET 404s), so terminate on an empty batch — do() maps a 404
	// to found=false, which this also covers.
	if !found || len(env.Data) == 0 {
		return nil, false, nil
	}

	out := make([]Video, 0, len(env.Data))
	for _, d := range env.Data {
		out = append(out, d.toVideo())
	}
	return out, true, nil
}

// maxVideoPages bounds the per-channel walk. At TubeArchivist's default page
// size of 25 this allows 100k videos in one channel — far beyond any real
// channel, while still guaranteeing the loop terminates on a misbehaving
// instance.
const maxVideoPages = 4000

// ChannelVideos pages through every video of one channel in the given watch
// state, in the order TubeArchivist returns them.
func (c *Client) ChannelVideos(ctx context.Context, channelID, watch string) ([]Video, error) {
	var all []Video
	for page := 1; page <= maxVideoPages; page++ {
		batch, more, err := c.VideoPage(ctx, channelID, watch, page)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if !more {
			return all, nil
		}
	}
	return nil, fmt.Errorf("taimport: video listing for channel %s exceeded %d pages; refusing to keep paging", channelID, maxVideoPages)
}
