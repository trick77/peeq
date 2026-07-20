// Package ytdlp is the only place in peeq that shells out to the yt-dlp
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
	// ErrPaused means the global youtube_paused kill-switch is set. The
	// pause gate in ytdlp.go returns this before the cookie gate, the
	// throttle sleep, or the binary are ever reached — never derived from
	// stderr, so Classify does not need to know about it.
	ErrPaused = errors.New("ytdlp: youtube paused")
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

// ExecError is the fallback when stderr matches no known signature. It
// keeps yt-dlp's own words attached to the exit status: without it a
// failure collapses to the bare "exit status 1", which says nothing about
// whether the cause was a dead cookie, a bot block, an extractor change,
// or a missing JS runtime. Unwrap keeps errors.Is/As working against the
// underlying *exec.ExitError.
type ExecError struct {
	Err    error  // the process error, normally *exec.ExitError
	Stderr string // trimmed tail of yt-dlp's stderr
}

func (e *ExecError) Error() string {
	if e.Stderr == "" {
		return e.Err.Error()
	}
	return e.Err.Error() + ": " + e.Stderr
}

func (e *ExecError) Unwrap() error { return e.Err }

// maxStderrTail bounds what an error carries: enough for the real reason,
// not so much that a verbose run pushes a wall of text into an API
// response body or a log line.
const maxStderrTail = 600

// stderrTail reduces yt-dlp's stderr to the part worth reporting. yt-dlp
// prints the actual failure on lines prefixed "ERROR:", usually after
// noisier WARNING lines, so those win; otherwise the last non-empty lines
// are used. The result is capped at maxStderrTail characters.
func stderrTail(stderr string) string {
	var errLines, allLines []string
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		allLines = append(allLines, line)
		if strings.HasPrefix(line, "ERROR:") {
			errLines = append(errLines, line)
		}
	}

	lines := errLines
	if len(lines) == 0 {
		lines = allLines
	}
	if len(lines) == 0 {
		return ""
	}
	// Keep the last few lines: with multi-line tracebacks the tail carries
	// the cause, and the head is boilerplate.
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}

	out := strings.Join(lines, "; ")
	if len(out) > maxStderrTail {
		out = out[:maxStderrTail] + "…"
	}
	return out
}

// Classify inspects yt-dlp's stderr output and the process exit error (if
// any) and maps them to the ytdlp error taxonomy. stderr is matched
// case-insensitively against known yt-dlp message signatures. If nothing
// recognizable is found, exitErr is wrapped in an ExecError carrying the
// stderr tail (nil is returned unchanged if there was no error at all).
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

	if exitErr == nil {
		return nil
	}
	return &ExecError{Err: exitErr, Stderr: stderrTail(stderr)}
}

func containsAny(haystack string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(haystack, sub) {
			return true
		}
	}
	return false
}
