package httpapi

import (
	"strings"
	"testing"
)

// TestSPA_imagesAreSameOriginOnly asserts every page peeq serves forbids the
// browser from loading an image off another host. This is the backstop for the
// rule that the browser never contacts YouTube: a component that puts an
// i.ytimg.com URL in an <img> gets a CSP block, not a hotlink.
func TestSPA_imagesAreSameOriginOnly(t *testing.T) {
	deps, _ := shareMetaDeps(t)
	h := New(deps)

	for _, path := range []string{"/", "/library", "/channels/UCa", "/s/no-such-token", "/api/pending"} {
		rec := getPublic(t, h, path)
		if got := rec.Header().Get("Content-Security-Policy"); got != imgSrcPolicy {
			t.Fatalf("%s: Content-Security-Policy = %q, want %q", path, got, imgSrcPolicy)
		}
	}

	// Only same-origin and inline sources: a host-source of any kind (an
	// https://i.ytimg.com allowance, a scheme like https:, a wildcard) would
	// reopen the hotlink this policy exists to stop.
	fields := strings.Fields(imgSrcPolicy)
	if len(fields) == 0 || fields[0] != "img-src" {
		t.Fatalf("imgSrcPolicy = %q, want an img-src directive", imgSrcPolicy)
	}
	for _, src := range fields[1:] {
		switch src {
		case "'self'", "data:", "blob:":
		default:
			t.Fatalf("imgSrcPolicy allows %q; only 'self', data: and blob: are permitted", src)
		}
	}
}
