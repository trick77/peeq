package ytdlp

import (
	"regexp"
	"testing"
)

// chromeUAPattern matches the desktop Chrome UA template TubeArchivist and
// this helper both use.
var chromeUAPattern = regexp.MustCompile(
	`^Mozilla/5\.0 \(Windows NT 10\.0; Win64; x64\) AppleWebKit/537\.36 \(KHTML, like Gecko\) Chrome/\d+\.\d+\.\d+\.\d+ Safari/537\.36$`,
)

// TestRandomUserAgent_looksLikeChrome asserts the returned string matches
// the expected desktop Chrome UA shape.
func TestRandomUserAgent_looksLikeChrome(t *testing.T) {
	ua := RandomUserAgent()
	if !chromeUAPattern.MatchString(ua) {
		t.Fatalf("RandomUserAgent() = %q, does not look like a desktop Chrome UA", ua)
	}
}

// TestRandomUserAgent_varies proves the UA isn't a hardcoded constant: over
// enough draws, more than one distinct value must appear.
func TestRandomUserAgent_varies(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		seen[RandomUserAgent()] = true
	}
	if len(seen) < 2 {
		t.Fatalf("RandomUserAgent() returned only %d distinct value(s) across 50 calls, want variation", len(seen))
	}
}

// TestRequestHeaders_setsUserAgent locks the header-shape contract for
// future Phase 3 direct HTTP requests.
func TestRequestHeaders_setsUserAgent(t *testing.T) {
	h := RequestHeaders()
	ua := h.Get("User-Agent")
	if !chromeUAPattern.MatchString(ua) {
		t.Fatalf("RequestHeaders() User-Agent = %q, does not look like a desktop Chrome UA", ua)
	}
}
