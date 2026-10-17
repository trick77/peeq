package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/trick77/ship/kit/httpx"
)

// Body limits. Every JSON body peeq accepts is small — a URL, a flag, a
// position, a settings patch — except the pasted cookie file, which is a
// Netscape jar of a few kilobytes at most. Without a bound the decoder would
// read whatever a client sends, and two of these routes take an API token
// rather than a session.
//
// maxRequestBody is the server-wide ceiling (httpx.MaxBody) on every request
// before any handler runs, so a handler that decodes r.Body on its own cannot
// reopen an unbounded read; each route's httpx.DecodeJSONWith then applies the
// tighter bound (readers compose: the smaller limit wins).
const (
	maxJSONBody    = 64 << 10 // 64 KiB
	maxCookieBody  = 1 << 20  // 1 MiB
	maxRequestBody = maxCookieBody
)

// decodeJSONLenient bounds the body like httpx.DecodeJSONWith but treats an
// empty or malformed one as "no preference" and leaves dst untouched: the
// favorite toggle and the share-link post both have a meaning for a bare
// POST. Only a body over the limit is refused, with a 413. kit has no lenient
// form (AllowEmpty still answers a malformed body 400).
//
// The base ResponseWriter is unwrapped first: net/http's limited reader
// type-asserts the writer it is given for the hook that closes the
// connection after a 413, and the middleware's StatusWriter is not it.
func decodeJSONLenient(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	base := w
	for {
		u, ok := base.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		base = u.Unwrap()
	}
	r.Body = http.MaxBytesReader(base, r.Body, limit)
	err := json.NewDecoder(r.Body).Decode(dst)
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	}
	return true
}
