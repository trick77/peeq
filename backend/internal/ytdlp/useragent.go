package ytdlp

import (
	"math/rand/v2"
	"net/http"
)

// chromeVersions is a pool of recent desktop Chrome version strings, used
// to build a plausible, varying User-Agent for direct HTTP requests to
// YouTube that do NOT go through yt-dlp (e.g. subtitle downloads, Shorts
// existence checks planned for Phase 3). Modeled on TubeArchivist's
// requests_headers() helper (backend/common/src/helper.py), modernized to
// a more recent Chrome version range.
var chromeVersions = []string{
	"120.0.6099.129",
	"120.0.6099.216",
	"121.0.6167.85",
	"121.0.6167.140",
	"122.0.6261.94",
	"122.0.6261.129",
	"123.0.6312.86",
	"123.0.6312.122",
	"124.0.6367.91",
	"124.0.6367.201",
	"125.0.6422.60",
	"125.0.6422.141",
	"126.0.6478.61",
	"126.0.6478.126",
	"127.0.6533.72",
	"127.0.6533.119",
	"128.0.6613.84",
	"128.0.6613.137",
	"129.0.6668.58",
	"129.0.6668.100",
	"130.0.6723.58",
	"130.0.6723.116",
}

// RandomUserAgent returns a random desktop Chrome User-Agent string.
//
// IMPORTANT: this is deliberately NOT used for any yt-dlp invocation. Only
// for direct (non-yt-dlp) HTTP requests peeq makes straight to YouTube.
// yt-dlp manages its own per-extractor-client headers, and overriding its
// User-Agent while also sending cookies causes client/header mismatches
// that YouTube can flag. This mirrors TubeArchivist's approach: it fakes a
// random Chrome UA only for its own direct requests (subtitle downloads,
// Shorts checks via requests_headers()) and leaves yt-dlp's own header
// handling untouched. This helper exists for Phase 3 direct-request code
// (subtitles, Shorts checks); it is intentionally unused by Runner today.
func RandomUserAgent() string {
	version := chromeVersions[rand.IntN(len(chromeVersions))]
	return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" +
		version + " Safari/537.36"
}

// RequestHeaders returns an http.Header pre-populated with a random
// desktop Chrome User-Agent (see RandomUserAgent), ready to attach to a
// direct (non-yt-dlp) HTTP request to YouTube.
func RequestHeaders() http.Header {
	h := http.Header{}
	h.Set("User-Agent", RandomUserAgent())
	return h
}
