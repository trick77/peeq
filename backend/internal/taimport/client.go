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
