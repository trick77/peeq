package httpapi

import (
	"testing"
)

// TestSPA_imagesAreSameOriginOnly asserts every page peeq serves forbids the
// browser from loading an image off another host. This is the backstop for the
// rule that the browser never contacts YouTube: a component that puts an
// i.ytimg.com URL in an <img> gets a CSP block, not a hotlink.
func TestSPA_imagesAreSameOriginOnly(t *testing.T) {
	deps, _ := shareMetaDeps(t)
	h := New(deps)

	for _, path := range []string{"/", "/library", "/channels/UCa", "/s/no-such-token"} {
		rec := getPublic(t, h, path)
		if got := rec.Header().Get("Content-Security-Policy"); got != imgSrcPolicy {
			t.Fatalf("%s: Content-Security-Policy = %q, want %q", path, got, imgSrcPolicy)
		}
	}
	if imgSrcPolicy != "img-src 'self' data: blob:" {
		t.Fatalf("imgSrcPolicy = %q", imgSrcPolicy)
	}
}
