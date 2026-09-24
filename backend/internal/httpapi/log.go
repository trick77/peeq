package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/trick77/peeq/internal/logx"
)

// serverError logs the underlying cause of a 5xx with request context and
// returns a generic JSON error to the client, so internal details never leak
// to the browser. Every 500 path should go through here so failures are never
// silent. Only r.URL.Path is logged — never the query string, which on the
// OIDC callback carries a live auth code.
func serverError(w http.ResponseWriter, r *http.Request, err error, clientMessage string) {
	slog.Error("request failed",
		"method", r.Method,
		"path", r.URL.Path,
		"client_message", clientMessage,
		"err", logx.RedactErr(err),
	)
	writeJSONError(w, http.StatusInternalServerError, clientMessage)
}

// upstreamError answers a 502 for a failure of something peeq called on the
// client's behalf (yt-dlp, today). The client keeps getting the upstream's
// own text after clientPrefix — the UI surfaces it, and it is the only thing
// that tells the user WHY a channel would not resolve — and the log gets the
// same cause, so the failure is no longer visible only as an access line.
// Both copies are redacted, so the body can never say more than the log.
//
// ERROR, not WARN, although the upstream is the one that failed: the access
// line for a 502 is at ERROR, and an operator running at that level must
// still see the reason next to it.
//
// A client that went away before the upstream answered gets nothing and
// logs nothing above DEBUG: the cancellation is the client's, not a failure.
func upstreamError(w http.ResponseWriter, r *http.Request, err error, clientPrefix string) {
	if r.Context().Err() != nil {
		slog.Debug("upstream request abandoned by client", "method", r.Method, "path", r.URL.Path)
		return
	}
	redacted := logx.RedactErr(err)
	slog.Error("upstream request failed",
		"method", r.Method,
		"path", r.URL.Path,
		"client_message", clientPrefix,
		"err", redacted,
	)
	writeJSONError(w, http.StatusBadGateway, clientPrefix+": "+redacted.Error())
}
