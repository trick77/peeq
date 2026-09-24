package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// Body limits. Every JSON body peeq accepts is small — a URL, a flag, a
// position, a settings patch — except the pasted cookie file, which is a
// Netscape jar of a few kilobytes at most. Without a bound the decoder would
// read whatever a client sends, and two of these routes take an API token
// rather than a session.
//
// maxRequestBody is the server-wide ceiling the logging middleware puts on
// every request before any handler runs, so a handler that decodes r.Body
// on its own cannot reopen an unbounded read; the per-route helpers below
// then apply the tighter bound (readers compose: the smaller limit wins).
const (
	maxJSONBody    = 64 << 10 // 64 KiB
	maxCookieBody  = 1 << 20  // 1 MiB
	maxRequestBody = maxCookieBody
)

// readJSON bounds r.Body at limit and decodes it into dst. tooLarge reports
// a body over the limit, already answered with a 413; otherwise err is the
// parse error, for the caller to answer as its route requires.
//
// The base ResponseWriter is unwrapped first: net/http's limited reader
// type-asserts the writer it is given for the hook that closes the
// connection after a 413, and the middleware's recorder is not it.
func readJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64) (tooLarge bool, err error) {
	base := w
	for {
		u, ok := base.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		base = u.Unwrap()
	}
	r.Body = http.MaxBytesReader(base, r.Body, limit)
	err = json.NewDecoder(r.Body).Decode(dst)
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return true, err
	}
	return false, err
}

// decodeJSON reads at most limit bytes of JSON from r into dst. It answers
// 413 for a body over the limit and 400 with badRequest for one that does
// not parse (an empty body included), and reports false in both cases so
// the caller just returns. Field-level checks ("category is required") stay
// with the caller, which answers them with the same message.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, limit int64, badRequest string) bool {
	tooLarge, err := readJSON(w, r, dst, limit)
	if tooLarge {
		return false
	}
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, badRequest)
		return false
	}
	return true
}

// decodeJSONLenient bounds the body like decodeJSON but treats an empty or
// malformed one as "no preference" and leaves dst untouched: the favorite
// toggle and the share-link post both have a meaning for a bare POST. Only
// a body over the limit is refused, with a 413.
func decodeJSONLenient(w http.ResponseWriter, r *http.Request, dst any, limit int64) bool {
	tooLarge, _ := readJSON(w, r, dst, limit)
	return !tooLarge
}

// decodeJSONOptional is decodeJSON for a route whose body may be absent
// altogether (the schedule skip: a bare POST is the ordinary skip) but must
// parse when it is there: an empty body is fine, a malformed one is a 400.
func decodeJSONOptional(w http.ResponseWriter, r *http.Request, dst any, limit int64, badRequest string) bool {
	tooLarge, err := readJSON(w, r, dst, limit)
	if tooLarge {
		return false
	}
	if err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, badRequest)
		return false
	}
	return true
}
