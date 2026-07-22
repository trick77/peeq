package sponsorblock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/version"
)

// DefaultBaseURL is the public SponsorBlock instance.
const DefaultBaseURL = "https://sponsor.ajay.app"

// requestTimeout bounds a single segment lookup. Short on purpose: this runs
// in a background backfill that has thousands of videos to get through, and a
// video whose lookup times out is simply retried on a later pass.
const requestTimeout = 10 * time.Second

// Client fetches segments from a SponsorBlock instance.
type Client struct {
	baseURL   string
	hc        *http.Client
	userAgent string
}

// NewClient returns a Client for the SponsorBlock instance at baseURL (empty
// = DefaultBaseURL). Pass nil for hc to get a client with a sane timeout.
func NewClient(baseURL string, hc *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if hc == nil {
		hc = &http.Client{Timeout: requestTimeout}
	}
	return &Client{
		baseURL:   strings.TrimRight(baseURL, "/"),
		hc:        hc,
		userAgent: "peeq/" + version.Version,
	}
}

// segmentDoc is one entry of the API's per-video segment list.
type segmentDoc struct {
	Category string `json:"category"`
	// Segment is [start, end] in seconds.
	Segment [2]float64 `json:"segment"`
	// VideoDuration is the duration the submitter's client saw. Zero when the
	// submission predates the field. Used to reject segments belonging to a
	// different cut of the video — see keepSegment.
	VideoDuration float64 `json:"videoDuration"`
}

// videoDoc is one entry of the hash-prefix response: the endpoint answers with
// EVERY video whose id hash starts with the prefix, not just the one asked for.
type videoDoc struct {
	VideoID  string       `json:"videoID"`
	Segments []segmentDoc `json:"segments"`
}

// Segments returns the stored-shape segments for videoID, sorted by start
// time. durationSeconds is peeq's own duration for the video and is used to
// reject stale submissions; pass 0 when it is unknown, which skips that check
// rather than rejecting everything.
//
// A video with no segments is not an error: the API answers 404 for it, which
// returns (nil, nil).
func (c *Client) Segments(ctx context.Context, videoID string, durationSeconds float64) ([]Segment, error) {
	// Only the first four characters of the video id's SHA-256 go over the
	// wire. The server answers with every video sharing that prefix, so it
	// never learns which one was actually asked for — the same privacy trick
	// yt-dlp's own SponsorBlock postprocessor uses.
	sum := sha256.Sum256([]byte(videoID))
	prefix := hex.EncodeToString(sum[:])[:4]

	categories, err := json.Marshal(Categories)
	if err != nil {
		return nil, fmt.Errorf("sponsorblock: encode categories: %w", err)
	}
	q := url.Values{}
	q.Set("service", "YouTube")
	q.Set("categories", string(categories))
	// Only "skip" segments are wanted: "poi" and "chapter" action types map to
	// the categories deliberately left out of Categories.
	q.Set("actionTypes", `["skip"]`)

	endpoint := c.baseURL + "/api/skipSegments/" + prefix + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("sponsorblock: build request for %s: %w", videoID, err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sponsorblock: GET segments for %s: %w", videoID, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// No submissions for any video under this hash prefix. Not a failure:
		// most videos have no segments at all.
		return nil, nil
	case resp.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("sponsorblock: GET segments for %s: %s: %s",
			videoID, resp.Status, strings.TrimSpace(string(body)))
	}

	var docs []videoDoc
	if err := json.NewDecoder(resp.Body).Decode(&docs); err != nil {
		return nil, fmt.Errorf("sponsorblock: decode segments for %s: %w", videoID, err)
	}

	for _, d := range docs {
		if d.VideoID != videoID {
			// One of the decoy videos the hash prefix pulled in.
			continue
		}
		return normalize(d.Segments, durationSeconds), nil
	}
	return nil, nil
}

// normalize turns wire segments into stored ones: unwanted categories and
// stale submissions are dropped, near-boundary values are snapped, and the
// result is sorted by start time so the player can rely on the order.
func normalize(docs []segmentDoc, duration float64) []Segment {
	var out []Segment
	for _, d := range docs {
		start, end := d.Segment[0], d.Segment[1]
		// A [0,0] segment marks the ENTIRE video (a full-video label such as
		// "this whole video is self-promotion"). Skipping it would skip the
		// video, so it is not a segment peeq can use.
		if start == 0 && end == 0 {
			continue
		}
		if end <= start {
			continue
		}
		if !Wanted(d.Category) {
			continue
		}
		// Snap sub-second gaps at both ends, so a segment that starts at
		// 0.4s doesn't leave 400ms of an ad playing, and one that ends 800ms
		// before the video does doesn't leave a stub behind.
		if start <= 1 {
			start = 0
		}
		if duration > 0 && duration-end <= 1 {
			end = duration
		}
		if !keepSegment(start, end, d.VideoDuration, duration) {
			continue
		}
		out = append(out, Segment{Category: d.Category, StartTime: start, EndTime: end})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartTime < out[j].StartTime })
	return out
}

// keepSegment rejects segments submitted against a different cut of the video.
// Timestamps are absolute, so a segment from a re-uploaded or re-edited
// version points at the wrong minute of the copy peeq holds — worse than
// having no segments at all.
//
// The tolerance is yt-dlp's: durations within a second are the same video;
// beyond that, a small drift is only accepted when it is negligible relative
// to the segment's own length. An unknown duration on either side skips the
// check entirely rather than rejecting everything.
func keepSegment(start, end, submittedDuration, ourDuration float64) bool {
	if submittedDuration == 0 || ourDuration == 0 {
		return true
	}
	diff := math.Abs(ourDuration - submittedDuration)
	if diff < 1 {
		return true
	}
	return diff < 5 && diff/(end-start) < 0.05
}
