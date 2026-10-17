// Package web embeds the built frontend (web/dist) and serves it as a SPA.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/trick77/ship/kit/spa"
)

//go:embed all:dist
var distFS embed.FS

// routes are the first path segments the client router knows (parsePath in
// ui/src/route.ts, old paths it still redirects included). They get the shell;
// any other path is a real 404, so a typo or a probe (favicon.ico: peeq ships
// an SVG icon and no .ico) is not answered with the whole app at 200.
var routes = map[string]bool{
	"": true, "video": true, "channel": true, "s": true, "channels": true, "search": true, "add": true,
	"inbox": true, "pending": true, "decide": true, "up-next": true, "history": true, "queue": true,
	"activity": true, "settings": true,
}

// IsRoute reports whether path is one of the SPA's client routes.
func IsRoute(path string) bool {
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
// every load.
func Handler() http.Handler {
	return spa.Handler(dist(), spa.Options{Routes: IsRoute})
}

// IndexHTML returns the embedded index.html shell. The server serves the public
// share page from it with Open Graph meta injected, so a shared link unfurls
// into a card instead of a bare title (see httpapi/share_meta.go).
func IndexHTML() ([]byte, error) {
	return distFS.ReadFile("dist/index.html")
}
