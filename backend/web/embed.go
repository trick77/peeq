// Package web embeds the built frontend (web/dist) and serves it as a SPA.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/trick77/ship/kit/spa"
)

// dist holds the Vite build, or only .gitkeep in a fresh clone (ship check
// empties it before the UI build; the image builds its own).
//
//go:embed all:dist
var distFS embed.FS

// placeholder is what an unbuilt binary answers on the app's routes, with a
// 503 (kit's spa): a Go-only build says so instead of serving a blank page.
//
//go:embed placeholder.html
var placeholder []byte

// routes are the first path segments the client router knows (parsePath in
// ui/src/route.ts, old paths it still redirects included). They get the shell;
// any other path is a real 404, so a typo or a probe (favicon.ico: peeq ships
// an SVG icon and no .ico) is not answered with the whole app at 200.
var routes = map[string]bool{
	"": true, "video": true, "channel": true, "s": true, "channels": true, "search": true, "add": true,
	"inbox": true, "pending": true, "decide": true, "up-next": true, "history": true, "queue": true,
	"activity": true, "settings": true,
}

// IsRoute reports whether path is one of the SPA's client routes. The shell's
// own name is one too: a bookmark or an installed app may point at it, and kit
// leaves it to the route check.
func IsRoute(path string) bool {
	if path == "/index.html" {
		return true
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	return routes[first]
}

func dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time; this is a programmer error
	}
	return sub
}

// Handler serves the embedded frontend (kit's spa): files as they are,
// /assets/ cached forever, the shell on the client routes, revalidated on
// every load; the placeholder there when the UI was not built.
func Handler() http.Handler { return handler(dist()) }

func handler(d fs.FS) http.Handler {
	return spa.Handler(d, spa.Options{Routes: IsRoute, Placeholder: placeholder})
}

// IndexHTML returns the embedded index.html shell, an error when the UI was
// not built. The server serves the public share page from it with Open Graph
// meta injected, so a shared link unfurls into a card instead of a bare title
// (see httpapi/share_meta.go).
func IndexHTML() ([]byte, error) { return fs.ReadFile(dist(), "index.html") }
