package mcpserver

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/trick77/peeq/internal/tokenauth"
	mcp "github.com/trick77/ship/kit/mcpserver"
)

// Deps configures the handler.
type Deps struct {
	Library Library
	// Token is the bearer secret (BACKEND_MCP_TOKEN). Empty rejects everything;
	// callers are expected not to mount the route at all in that case.
	Token string
	// PublicURL is peeq's external base URL. Its origin is the only browser
	// Origin accepted, and links in tool output are built against it.
	PublicURL string
	// Version is reported as serverInfo.version.
	Version string
}

// handler holds what peeq's tools and per-request texts read; the protocol is
// kit's mcpserver.
type handler struct {
	lib       Library
	publicURL string

	// hint caches channelHint for hintTTL: discovery, initialize and
	// tools/list all want it, and a burst of them must not each re-read
	// the channel list.
	hintMu      sync.Mutex
	hint        string
	hintExpires time.Time
}

// New returns the /mcp handler: origin check, then bearer auth, then kit's
// MCP server with peeq's tools.
func New(d Deps) http.Handler {
	h := &handler{lib: d.Library, publicURL: strings.TrimRight(d.PublicURL, "/")}
	return mcp.New(mcp.Server{
		Name:             "peeq",
		Version:          d.Version,
		InstructionsFunc: h.instructions,
		Tools:            h.tools(),
		Aliases:          toolAliases,
		ToolsTTL:         toolsTTL,
		MaxTextBytes:     maxToolTextBytes,
		AllowedOrigin:    d.PublicURL,
		Auth:             tokenauth.NewStaticTokenMiddleware(d.Token).RequireToken,
	})
}
