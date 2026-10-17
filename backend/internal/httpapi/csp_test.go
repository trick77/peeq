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
		if got := rec.Header().Get("Content-Security-Policy"); got != csp {
			t.Fatalf("%s: Content-Security-Policy = %q, want %q", path, got, csp)
		}
	}

	// Only same-origin and inline sources: a host-source of any kind (an
	// https://i.ytimg.com allowance, a scheme like https:, a wildcard) would
	// reopen the hotlink this policy exists to stop. default-src covers every
	// fetch directive left unset, so it is held to the same rule.
	directives := map[string][]string{}
	for d := range strings.SplitSeq(csp, ";") {
		if f := strings.Fields(d); len(f) > 0 {
			directives[f[0]] = f[1:]
		}
	}
	for _, name := range []string{"img-src", "default-src"} {
		srcs, ok := directives[name]
		if !ok {
			t.Fatalf("csp = %q, want a %s directive", csp, name)
		}
		for _, src := range srcs {
			switch src {
			case "'self'", "data:", "blob:":
			default:
				t.Fatalf("%s allows %q; only 'self', data: and blob: are permitted", name, src)
			}
		}
	}
}
