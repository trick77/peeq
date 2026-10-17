package httpapi

import "net/http"

// imgSrcPolicy confines every image a peeq page loads to peeq itself. The
// browser must never contact a YouTube host: posters, channel art and every
// other picture come through peeq's endpoints, which fetch them once on the
// serial queue and cache them. A component that hotlinks i.ytimg.com anyway is
// blocked here, visibly (a broken image and a console error), rather than
// silently making the request.
//
// Only img-src: the other directives are left at the browser default, so
// nothing else the SPA does (inline styles, same-origin media) is affected.
const imgSrcPolicy = "img-src 'self' data: blob:"

// withImagePolicy sets imgSrcPolicy on every response from next. CSP only
// takes effect on documents, but setting it everywhere means an HTML page from
// a handler added later is covered without anyone remembering to wrap it.
func withImagePolicy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", imgSrcPolicy)
		next.ServeHTTP(w, r)
	})
}
