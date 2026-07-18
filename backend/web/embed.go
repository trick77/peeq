// Package web embeds the built frontend (web/dist) and serves it as a SPA.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

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
		w.Header().Set("Cache-Control", "no-cache")
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
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
