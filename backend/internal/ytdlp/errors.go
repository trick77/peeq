// Package ytdlp is the only place in peeq that shells out to the yt-dlp
// binary. errors.go classifies the binary's stderr output into a small,
// stable error taxonomy so callers (the download worker, the API layer)
// can branch on Go errors instead of parsing yt-dlp's free-text output
// themselves.
package ytdlp

import (
	"errors"
	"strings"
	"unicode/utf8"
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
//
// Stderr is propagated verbatim (trimmed only for length) and surfaces in
// API error bodies and the jobs.last_error column. That is safe for the
// current argv, which carries neither --verbose nor --print-traffic, so
// yt-dlp never echoes cookie values. Do NOT add either flag without first
// redacting here: with them, the cookie jar's contents reach stderr and
// would be persisted and returned over HTTP.
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

// IsMissingTab reports whether err is yt-dlp refusing a channel tab that does
// not exist ("ERROR: [youtube:tab] UC…: This channel does not have a streams
// tab"). A channel that has never gone live has no /streams tab at all, so this
// is the expected, boring outcome for most channels rather than a fault —
// callers use it to keep an ordinary scan quiet.
//
// On the STREAMS side it is a logging distinction only: the caller treats any
// streams-tab failure as "no streams", so a reworded yt-dlp message costs log
// noise and nothing else. On the UPLOADS side it is correctness-bearing — it is
// what stops a channel with no /videos tab (one that publishes only
// livestreams) from failing every scan — so a reworded message there costs that
// channel its scannability. The string match is deliberately loose on the tab
// name to keep that as unlikely as possible; widen it, never narrow it.
func IsMissingTab(err error) bool {
	var ee *ExecError
	if !errors.As(err, &ee) {
		return false
	}
	return strings.Contains(strings.ToLower(ee.Stderr), "does not have a")
}

// maxStderrTail bounds what an error carries, in bytes: enough for the
// real reason, not so much that a verbose run pushes a wall of text into
// an API response body or a log line.
const maxStderrTail = 600

// stderrTail reduces yt-dlp's stderr to the part worth reporting. yt-dlp
// prints the actual failure on lines prefixed "ERROR:", usually after
// noisier WARNING lines, so those win; otherwise the last non-empty lines
// are used. The result is capped at maxStderrTail bytes, truncated on a
// rune boundary — yt-dlp echoes video titles into its ERROR lines, so a
// blind byte cut would routinely split a multi-byte character and emit
// invalid UTF-8 into the API response and the jobs.last_error column.
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
		cut := maxStderrTail
		// Back off to the start of the rune that straddles the cut, so the
		// result is never left holding a partial multi-byte character.
		for cut > 0 && !utf8.RuneStart(out[cut]) {
			cut--
		}
		out = out[:cut] + "…"
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
	// Channel-level deletion signatures. These are distinct from the
	// video-level ones above: yt-dlp emits them when the CHANNEL itself is
	// gone, which is exactly the case the auto-unsubscribe feature exists to
	// detect — a per-video "video unavailable" says nothing about whether the
	// channel is still there. Without these two lines matching, a deleted
	// channel falls through to the generic ExecError branch below,
	// staleUnsubscribe is never reached, and the whole feature never fires.
	case containsAny(s,
		// Verified against real yt-dlp output run against a nonexistent
		// channel: "ERROR: [youtube:tab] UCzzzzzzzzzzzzzzzzzzzzzz: YouTube
		// said: This channel does not exist."
		"this channel does not exist",
		// The known YouTube termination message. NOT verified against real
		// yt-dlp output (no terminated channel was available to observe) —
		// keep this comment until someone captures the real stderr line.
		"this account has been terminated",
	):
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
