package httpapi

import (
	"net"
	"net/http"
	"strings"

	"github.com/trick77/ship/kit/httpx"
	"github.com/trick77/ship/kit/ratelimit"
)

// clientIP is the caller's address for the access line and the rate limits.
// peeq runs behind one Traefik on a private network: the X-Forwarded-For hop
// it appends is trusted only when the TCP peer is that private or loopback
// proxy. A peer on a public address talks to peeq directly, and whatever it
// put in the header is its own claim.
func clientIP(r *http.Request) string {
	peer := httpx.ClientIP(r, 0)
	if ip := net.ParseIP(peer); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		return httpx.ClientIP(r, 1)
	}
	return peer
}

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
// it; it bounds what one caller can make peeq do (a share stream is a disk
// read, a card a JPEG encode). Sized for a player seeking a video, which sends
// a burst of range requests: 300 a minute, 100 at once.
var (
	tokenRouteLimit = ratelimit.Options{Rate: 5, Burst: 100}
	// The OIDC callback is one request per login.
	callbackLimit = ratelimit.Options{Rate: 10.0 / 60, Burst: 10}
)

// limiter is one limiter of o, keyed by clientIP: every route it wraps
// shares its buckets.
func limiter(o ratelimit.Options) func(http.Handler) http.Handler {
	return ratelimit.New(o).Middleware(func(r *http.Request) string { return ratelimit.Key(clientIP(r)) })
}
