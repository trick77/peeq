// Package web embeds the built frontend (web/dist) and serves it as a SPA.
package web

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Go's built-in MIME table has no .webmanifest entry, so without this
// http.FileServer falls through to content sniffing and serves the web app
// manifest as text/plain. .ico needs no entry — net/http's sniffer recognises
// the ICO signature on its own.
func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// Handler serves the embedded frontend (web/dist) as a SPA: existing regular
// files are served directly; any other path — unknown client-side routes AND
// directory paths — falls back to index.html (so a directory never renders an
// http.FileServer listing).
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time; this is a programmer error
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info, err := fs.Stat(sub, trimLeadingSlash(r.URL.Path)); err == nil && !info.IsDir() {
			// Vite emits content-hashed bundles under /assets/, so they can be cached
			// forever; index.html (and the SPA fallback below) must not, or clients
			// pin a stale entrypoint after a deploy.
			if strings.HasPrefix(r.URL.Path, "/assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else if r.URL.Path == "/index.html" {
				w.Header().Set("Cache-Control", "no-cache")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// peeq ships an SVG icon and declares it in <head>; it deliberately has no
		// favicon.ico, because the clients that probe for one at the domain root
		// are RSS readers, Windows bookmark thumbnails and old IE. Without this
		// the probe would reach the SPA fallback and get index.html with a 200 —
		// an icon request answered with HTML, which is worse than no icon. A 404
		// is the honest answer, and every browser handles it by falling back to
		// the declared icon.
		if r.URL.Path == "/favicon.ico" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}

// IndexHTML returns the embedded index.html shell. The server serves the public
// share page from it with Open Graph meta injected, so a shared link unfurls
// into a card instead of a bare title (see httpapi/share_meta.go).
func IndexHTML() ([]byte, error) {
	return distFS.ReadFile("dist/index.html")
}

func trimLeadingSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	if p == "" {
		return "."
	}
	return p
}
