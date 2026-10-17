package mcpserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/trick77/peeq/internal/auth"
)

// maxBodyBytes bounds one request body. Tool arguments are a query and a few
// filters; a megabyte is far past anything legitimate.
const maxBodyBytes = 1 << 20

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

type handler struct {
	lib       Library
	origin    string
	publicURL string
	version   string

	// hint caches channelHint for hintTTL: discovery, initialize and
	// tools/list all want it, and a burst of them must not each re-read
	// the channel list.
	hintMu      sync.Mutex
	hint        string
	hintExpires time.Time
}

// New returns the /mcp handler: origin check, then bearer auth, then the
// method, size and JSON-RPC handling.
func New(d Deps) http.Handler {
	h := &handler{
		lib:       d.Library,
		origin:    originOf(d.PublicURL),
		publicURL: strings.TrimRight(d.PublicURL, "/"),
		version:   d.Version,
	}
	authed := auth.NewStaticTokenMiddleware(d.Token).RequireToken(http.HandlerFunc(h.serve))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Before auth: a page on another origin (DNS rebinding included) learns
		// nothing, not even whether a guessed token was right.
		if o := r.Header.Get("Origin"); o != "" && (h.origin == "" || !strings.EqualFold(o, h.origin)) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		authed.ServeHTTP(w, r)
	})
}

// originOf reduces a base URL to scheme://host[:port], or "" when it is not
// an absolute URL.
func originOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// Stateless and JSON-only: no GET stream to open, no session to DELETE.
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		writeRPCError(w, http.StatusBadRequest, nil, codeParseError, "could not read the request body", nil)
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "batch requests are not supported", nil)
		return
	}
	var req request
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeRPCError(w, http.StatusBadRequest, nil, codeParseError, "parse error", nil)
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPCError(w, http.StatusBadRequest, req.ID, codeInvalidRequest, "invalid request", nil)
		return
	}
	h.dispatch(w, r, req)
}
