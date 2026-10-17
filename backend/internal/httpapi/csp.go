package httpapi

// csp is kit's httpx.DefaultCSP with blob: added to img-src. It confines every
// image a peeq page loads to peeq itself. The browser must never contact a
// YouTube host: posters, channel art and every other picture come through
// peeq's endpoints, which fetch them once on the serial queue and cache them.
// A component that hotlinks i.ytimg.com anyway is blocked here, visibly (a
// broken image and a console error), rather than silently making the request.
//
// httpx.SecurityHeaders sets it on every response, not per HTML route: a page
// served by a handler added later is covered without remembering to wrap it.
const csp = "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
