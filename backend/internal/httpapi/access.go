package httpapi

import (
	"net/http"
	"strings"

	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/ratelimit"
)

// clientIP is the caller's address for the access line and the rate limits:
// the X-Forwarded-For entry the one Traefik in front of peeq appended (compose
// publishes no port, so nothing else reaches it), the TCP peer when there is
// no header (make dev). A caller can prepend to the header but never replace
// the hop Traefik adds.
func clientIP(r *http.Request) string { return httpx.ClientIP(r, 1) }

// tokenPrefixes are the routes whose next path segment is a bearer secret: a
// share link, its API, and a playback grant. Holding the path is holding the
// video, so the access and panic lines log it masked.
var tokenPrefixes = []string{"/s/", "/api/s/", "/api/p/"}

// maskPath is r.URL.Path with the token segment of a token route replaced.
func maskPath(r *http.Request) string {
	p := r.URL.Path
	for _, prefix := range tokenPrefixes {
		rest, ok := strings.CutPrefix(p, prefix)
		if !ok || rest == "" {
			continue
		}
		_, tail, hasTail := strings.Cut(rest, "/")
		if hasTail {
			return prefix + "***/" + tail
		}
		return prefix + "***"
	}
	return p
}

// Rate limits on the routes a caller reaches without a session. A share or
// grant token is 128 random bits, so the limit is no defence against guessing
// it; it bounds what one caller can make peeq do (a card is a JPEG encode, a
// page a DB read): 300 a minute, 100 at once.
var (
	tokenRouteLimit = ratelimit.Options{Rate: 5, Burst: 100}
	// The two media streams get a bucket of their own: a player seeking, or an
	// Apple TV on a grant, sends a range request per seek, and a 429 there is
	// fatal to the player. Shared with the page, a household behind one IP
	// could stop its own playback by opening the share page twice.
	streamRouteLimit = ratelimit.Options{Rate: 20, Burst: 300}
	// The OIDC callback is one request per login.
	callbackLimit = ratelimit.Options{Rate: 10.0 / 60, Burst: 10}
)

// limiter is one limiter of o, keyed by clientIP: every route it wraps
// shares its buckets.
func limiter(o ratelimit.Options) func(http.Handler) http.Handler {
	return ratelimit.New(o).Middleware(func(r *http.Request) string { return ratelimit.Key(clientIP(r)) })
}
