package ytdlp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

// latestReleaseAPIURL is the GitHub API endpoint that names the latest
// yt-dlp release. This is deliberately NOT the releases/latest/download/
// URL UpdateLatest uses: that one hands back a binary and reveals its
// version only after a ~30MB download and an install, which is useless for
// answering "is an update available?".
const latestReleaseAPIURL = "https://api.github.com/repos/yt-dlp/yt-dlp/releases/latest"

// latestCheckTimeout bounds the whole release check. A slow or hanging
// GitHub must not wedge the version-check ticker: an unanswered check is
// reported as a check error and retried on the next tick.
const latestCheckTimeout = 20 * time.Second

// userAgent is sent on the API request because GitHub rejects API calls
// that arrive without one.
const userAgent = "peeq/1 (+https://github.com/trick77/peeq)"

// releaseTagPattern is the exact shape yt-dlp tags its releases with:
// a bare calendar version, e.g. "2026.07.04" — no "v" prefix.
//
// The tag is compared against the installed version with a plain string
// compare (see Status.UpdateAvailable), which is only correct while both
// sides are zero-padded YYYY.MM.DD. So a tag that does not match this
// shape is rejected as an error rather than compared blind: were upstream
// to start prefixing tags, "v2026.07.04" > "2026.07.04" would pin the
// indicator to "update available" forever with no way to clear it.
var releaseTagPattern = regexp.MustCompile(`^\d{4}\.\d{2}\.\d{2}$`)

// maxLatestBodyBytes caps how much of the release JSON is read. The real
// payload is a few KB; this only stops a misbehaving endpoint from
// streaming unbounded data into memory.
const maxLatestBodyBytes = 1 << 20

// LatestVersion asks GitHub for the tag of the newest yt-dlp release. It
// performs no install and touches no local binary — knowing the latest
// version is what lets peeq report an available update instead of
// silently swapping the binary underneath the user.
func LatestVersion(ctx context.Context) (string, error) {
	return latestVersionFrom(ctx, latestReleaseAPIURL)
}

// latestVersionFrom reads the release tag published at url, as described
// on LatestVersion. Factored out so tests can point it at an
// httptest.Server instead of the real GitHub API, without ever touching
// the network — the same seam downloadReleaseFrom provides for the
// binary download.
func latestVersionFrom(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, latestCheckTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("ytdlp: build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ytdlp: check latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ytdlp: check latest release: unexpected status %s", resp.Status)
	}

	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxLatestBodyBytes)).Decode(&body); err != nil {
		return "", fmt.Errorf("ytdlp: decode release response: %w", err)
	}
	if !releaseTagPattern.MatchString(body.TagName) {
		return "", fmt.Errorf("ytdlp: unexpected release tag %q", body.TagName)
	}
	return body.TagName, nil
}
