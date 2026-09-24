// Package logx holds the small helpers a log line needs before it is safe to
// write: today, stripping the parts of a URL an error message must not carry.
package logx

import (
	"log/slog"
	"regexp"
)

// queryStringRe matches a URL query string, from "?" up to (but not
// including) the next whitespace or double-quote character — that's how
// url.Error and most transport errors delimit an embedded URL in their
// rendered message.
var queryStringRe = regexp.MustCompile(`\?[^\s"]*`)

// userinfoRe matches "user:pass@" userinfo embedded in a URL authority.
var userinfoRe = regexp.MustCompile(`://[^/\s"]+@`)

// redacted is an error whose rendered message has been stripped of query
// strings and userinfo while its chain stays intact: errors.Is and errors.As
// see through it to the original error.
type redacted struct {
	msg string
	err error
}

func (r *redacted) Error() string { return r.msg }
func (r *redacted) Unwrap() error { return r.err }

// RedactErr strips query strings and userinfo from an error's rendered
// message. It exists for two kinds of URL that must never reach the logs: the
// OIDC callback URL, which carries a live auth code and state, and the image
// URLs peeq fetches from YouTube's CDN, whose query strings carry signed
// tokens. Both surface as *url.Error text, often wrapped with fmt.Errorf.
//
// Redaction operates on the rendered string rather than mutating an inner
// *url.Error, because fmt.Errorf's %w renders and freezes the wrapped
// error's message at wrap time — mutating a struct field inside it
// afterward has no effect on what the outer error's Error() returns. Working
// on the final string is immune to wrapping depth and to how the underlying
// libraries nest their errors.
//
// The whole query string is stripped rather than an allowlist of "safe"
// parameter names, since an allowlist is a leak waiting for the next library
// change to add a new sensitive parameter. The URL path is left visible,
// since that's the useful diagnostic.
//
// The result keeps the original error in its chain, so RedactErr may be
// applied where an error is WRAPPED (the fetch that produced it), not only
// where it is logged: a caller's errors.Is/As against the original sentinel
// or type still matches. The chain is for matching only — the fields of an
// unwrapped error (a *url.Error's URL) are NOT redacted, so never log those.
// When nothing was redacted, err is returned as is.
func RedactErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	out := queryStringRe.ReplaceAllString(msg, "")
	out = userinfoRe.ReplaceAllString(out, "://")
	if out == msg {
		return err
	}
	return &redacted{msg: out, err: err}
}

// RedactAttr is a slog.HandlerOptions.ReplaceAttr that runs RedactErr on every
// error logged under the key "err" — the one key this codebase logs errors
// under (AGENTS.md). Installed on the process's handler in main, it makes the
// "never log a URL's query string" rule hold for every log line, present and
// future, rather than only at the call sites someone remembered to wrap.
// Per-site RedactErr calls remain useful where an error's text goes somewhere
// other than a log line (an Activity row, a client message).
func RedactAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Key != "err" {
		return a
	}
	if err, ok := a.Value.Any().(error); ok {
		return slog.Any(a.Key, RedactErr(err))
	}
	return a
}
