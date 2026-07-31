package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"
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

	// shareImageMaxAge is the ceiling for the share-token image routes, in
	// seconds. Far shorter than the owner-side windows, and deliberately so: a
	// share link can be revoked at any moment, and a cached copy is not reachable
	// to revoke. Five minutes covers a reader's own repeat views — after that the
	// ETag makes the re-ask a 304 with no bytes — while bounding how long a
	// picture can outlive the link that authorized it.
	//
	// shareImageCacheControl clamps it further against the link's own expiry.
	shareImageMaxAge = 300

	// cacheImageMissing is the negative cache for an image that genuinely is not
	// there. The inbox is the reason it exists: its posters are requested
	// unconditionally so the backend can lazily fetch them from YouTube, so a
	// pending video with no poster anywhere used to 404 — and re-attempt an
	// outbound fetch — on every single page load. Short, because the next scan
	// may well fill the gap in.
	cacheImageMissing = "private, max-age=300"
)

// shareImageCacheControl is the Cache-Control for a share-token image, clamped
// so a cached picture cannot outlive the link that authorized it.
//
// The public routes are the only ones where the cache window is a security
// question rather than a freshness one. Resolve already refuses an expired
// token, but a copy already in a browser or an intermediary is past asking: a
// link shared with a 24h TTL and fetched at hour 23 would keep serving its
// poster into hour 24 on a fixed window. So the window is the smaller of the
// ceiling and whatever is left of the link.
//
// A link that never expires, or one whose row cannot be re-read, gets the plain
// ceiling — the same reasoning handleShareVideo uses when it re-reads the link
// for the footer: a miss here is harmless, not a reason to fail the request.
func (s *server) shareImageCacheControl(r *http.Request, videoID string) string {
	seconds := shareImageMaxAge
	if link, err := s.shareLinks.GetByVideo(r.Context(), videoID); err == nil && link != nil && link.ExpiresAt != "" {
		// Stored as a UTC datetime string (sharelink.sqliteTime), so an
		// unparsable value means the row is not what this code thinks it is —
		// keep the ceiling rather than inventing a window from a zero time.
		if exp, perr := time.Parse("2006-01-02 15:04:05", link.ExpiresAt); perr == nil {
			if remaining := int(time.Until(exp).Seconds()); remaining < seconds {
				seconds = max(remaining, 0)
			}
		}
	}
	return fmt.Sprintf("public, max-age=%d", seconds)
}

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
