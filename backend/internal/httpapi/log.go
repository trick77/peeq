package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"regexp"
)

// queryStringRe matches a URL query string, from "?" up to (but not
// including) the next whitespace or double-quote character — that's how
// url.Error and most transport errors delimit an embedded URL in their
// rendered message.
var queryStringRe = regexp.MustCompile(`\?[^\s"]*`)

// userinfoRe matches "user:pass@" userinfo embedded in a URL authority.
var userinfoRe = regexp.MustCompile(`://[^/\s"]+@`)

// redactErr strips query strings and userinfo from an error's rendered
// message before it reaches the logs. This matters most on the OIDC path:
// the callback URL carries a live auth code and state, and HandleCallback's
// failure modes (including ones wrapped with fmt.Errorf) embed that URL
// verbatim in their Error() text.
//
// Redaction operates on the rendered string rather than mutating an inner
// *url.Error, because fmt.Errorf's %w renders and freezes the wrapped
// error's message at wrap-time — mutating a struct field inside it
// afterward has no effect on what the outer error's Error() returns. Working
// on the final string is immune to wrapping depth and to how the underlying
// libraries nest their errors.
//
// The whole query string is stripped rather than an allowlist of "safe"
// parameter names, since an allowlist is a leak waiting for the next OIDC
// library change to add a new sensitive parameter. The URL path is left
// visible, since that's the useful diagnostic.
//
// When redaction changes the message, the returned error is a plain
// errors.New of the redacted text, which does NOT preserve errors.Is/As
// against the original sentinel or type. That's acceptable only because
// redactErr's result is used solely as a log attribute, never for control
// flow — do not use its result in an errors.Is/As check. When nothing was
// redacted, the original err (and its full chain) is returned unchanged.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	redacted := queryStringRe.ReplaceAllString(msg, "")
	redacted = userinfoRe.ReplaceAllString(redacted, "://")
	if redacted == msg {
		return err
	}
	return errors.New(redacted)
}

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
		"err", redactErr(err),
	)
	writeJSONError(w, http.StatusInternalServerError, clientMessage)
}
