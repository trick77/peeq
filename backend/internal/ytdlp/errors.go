// Package ytdlp is the only place in vark that shells out to the yt-dlp
// binary. errors.go classifies the binary's stderr output into a small,
// stable error taxonomy so callers (the download worker, the API layer)
// can branch on Go errors instead of parsing yt-dlp's free-text output
// themselves.
package ytdlp

import (
	"errors"
	"strings"
)

// Sentinel errors for failure families that apply across an entire run,
// not to one specific video.
var (
	// ErrNoCookie means no cookie is configured at all. The cookie gate in
	// ytdlp.go returns this before ever invoking the binary.
	ErrNoCookie = errors.New("ytdlp: no cookie configured")
	// ErrCookieExpired means a cookie is configured but yt-dlp rejected it
	// as invalid/expired.
	ErrCookieExpired = errors.New("ytdlp: cookie expired or invalid")
	// ErrBlocked means YouTube's bot detection rejected the request.
	ErrBlocked = errors.New("ytdlp: blocked by bot detection")
)

// TerminalError is a permanent, non-retryable failure for one specific
// video: it is gone, private, members-only, age-gated, or geo-blocked, and
// retrying will not help.
type TerminalError struct {
	Reason string // one of: deleted, private, members, age, geo
}

func (e *TerminalError) Error() string {
	return "ytdlp: terminal (" + e.Reason + ")"
}

// RetryableError is a transient failure (e.g. rate limiting) that callers
// may retry later, typically after backoff.
type RetryableError struct {
	Reason string
}

func (e *RetryableError) Error() string {
	return "ytdlp: retryable (" + e.Reason + ")"
}

// Classify inspects yt-dlp's stderr output and the process exit error (if
// any) and maps them to the ytdlp error taxonomy. stderr is matched
// case-insensitively against known yt-dlp message signatures. If nothing
// recognizable is found, exitErr is returned unchanged (nil if there was
// no error at all).
func Classify(stderr string, exitErr error) error {
	s := strings.ToLower(stderr)

	switch {
	case containsAny(s, "sign in to confirm you're not a bot", "confirm you're not a bot"):
		return ErrBlocked
	case containsAny(s, "cookies are no longer valid", "failed to load cookies", "cookie file is invalid", "cookies-from-browser instead"):
		return ErrCookieExpired
	case containsAny(s, "private video"):
		return &TerminalError{Reason: "private"}
	case containsAny(s, "video is no longer available", "video unavailable"):
		return &TerminalError{Reason: "deleted"}
	case containsAny(s, "members-only", "join this channel"):
		return &TerminalError{Reason: "members"}
	case containsAny(s, "age-restricted", "confirm your age"):
		return &TerminalError{Reason: "age"}
	case containsAny(s, "not available in your country", "not available on this app"):
		return &TerminalError{Reason: "geo"}
	case containsAny(s, "http error 429", "429: too many requests", "http error 5"):
		return &RetryableError{Reason: "rate limited or server error"}
	}

	return exitErr
}

func containsAny(haystack string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(haystack, sub) {
			return true
		}
	}
	return false
}
