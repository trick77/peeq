package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

// Cache-Control values for the routes that hand the browser bytes rather than
// JSON. They live together so the image routes cannot drift apart on how long a
// client may hold an asset, which is exactly what happened while each handler
// wrote its own header inline.
//
// "private" on the session-gated routes is deliberate: peeq runs behind Traefik,
// which does not cache by default, but "public" is a standing invitation for any
// shared cache in front of the app to keep a response that only one logged-in
// user was allowed to see. The share-token routes are public by definition — an
// unfurler fetching an og:image is the shared cache — so they keep saying so.
const (
	// cacheImageDay covers channel artwork and inbox posters. Artwork changes at
	// most weekly (the metadata refresher's interval) and an inbox poster only
	// while the item is still pending, so a day of browser caching saves a
	// request per image per page load without showing anything meaningfully old.
	cacheImageDay = "private, max-age=86400"

	// cacheImageHour covers a downloaded video's poster, which changes only when
	// the video is re-downloaded. The window is shorter than the artwork one
	// because it used to be nothing at all: every card on a 40-video Library page
	// cost a request on every load.
	cacheImageHour = "private, max-age=3600"

	// cacheImagePublicHour is the same window for the share-token routes, which
	// are served to anyone holding the link.
	cacheImagePublicHour = "public, max-age=3600"

	// cacheImageMissing is the negative cache for an image that genuinely is not
	// there. The inbox is the reason it exists: its posters are requested
	// unconditionally so the backend can lazily fetch them from YouTube, so a
	// pending video with no poster anywhere used to 404 — and re-attempt an
	// outbound fetch — on every single page load. Short, because the next scan
	// may well fill the gap in.
	cacheImageMissing = "private, max-age=300"
)

// notFoundCached is http.NotFound with the negative cache attached, for an
// image route whose answer is "there is no such image" rather than "something
// went wrong". A 404 carrying no Cache-Control is re-asked on every page load,
// and the inbox — which requests every poster unconditionally so the backend can
// lazily fetch it — turns that into a repeated outbound attempt at YouTube too.
func notFoundCached(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", cacheImageMissing)
	http.NotFound(w, r)
}

// etagFor returns a strong, quoted ETag for a blob of bytes.
//
// The quotes are not cosmetic: http.ServeContent parses the header with
// scanETag, which silently ignores an unquoted value — no 304, no error, just a
// full body every time.
//
// It hashes the content rather than deriving a token from the row's updated_at,
// for three reasons. The inbox's lazy-fetch path builds its Thumbnail value
// without a stamp at all, which is precisely where a first-time client lands.
// SQLite's datetime('now') has second resolution, so a replace inside the same
// second would not move a timestamp. And the share card is rendered per request
// and has no row of its own. The bytes are already in memory on every one of
// these paths, so hashing them costs nothing a read did not already pay for.
func etagFor(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}
