package ytdlp

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// TestClassify feeds the exact stderr signatures yt-dlp is known to emit
// for each failure family and checks Classify maps them to the right
// sentinel/typed error. These strings are load-bearing: later tasks (the
// download worker) branch on the returned error, not on the raw text.
func TestClassify(t *testing.T) {
	genericExit := fmt.Errorf("exit status 1")

	cases := []struct {
		name   string
		stderr string
		check  func(t *testing.T, err error)
	}{
		{
			name:   "bot detection",
			stderr: "ERROR: [youtube] dQw4w9WgXcQ: Sign in to confirm you're not a bot",
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrBlocked) {
					t.Fatalf("want ErrBlocked, got %v", err)
				}
			},
		},
		{
			name:   "private video",
			stderr: "ERROR: [youtube] dQw4w9WgXcQ: Private video. Sign in if you've been granted access to this video",
			check: func(t *testing.T, err error) {
				var te *TerminalError
				if !errors.As(err, &te) {
					t.Fatalf("want *TerminalError, got %v", err)
				}
				if te.Reason != "private" {
					t.Fatalf("Reason = %q, want %q", te.Reason, "private")
				}
			},
		},
		{
			name:   "no longer available",
			stderr: "ERROR: [youtube] dQw4w9WgXcQ: This video is no longer available",
			check: func(t *testing.T, err error) {
				var te *TerminalError
				if !errors.As(err, &te) {
					t.Fatalf("want *TerminalError, got %v", err)
				}
			},
		},
		{
			name:   "video unavailable",
			stderr: "ERROR: [youtube] dQw4w9WgXcQ: Video unavailable",
			check: func(t *testing.T, err error) {
				var te *TerminalError
				if !errors.As(err, &te) {
					t.Fatalf("want *TerminalError, got %v", err)
				}
			},
		},
		{
			name:   "rate limited",
			stderr: "ERROR: unable to download video data: HTTP Error 429: Too Many Requests",
			check: func(t *testing.T, err error) {
				var re *RetryableError
				if !errors.As(err, &re) {
					t.Fatalf("want *RetryableError, got %v", err)
				}
			},
		},
		{
			name:   "cookie expired",
			stderr: "ERROR: [youtube] dQw4w9WgXcQ: The provided YouTube account cookies are no longer valid. Try using --cookies-from-browser instead",
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrCookieExpired) {
					t.Fatalf("want ErrCookieExpired, got %v", err)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.check(t, Classify(c.stderr, genericExit))
		})
	}
}

func TestClassify_unrecognizedFallsBackToExitErr(t *testing.T) {
	exitErr := fmt.Errorf("exit status 2")
	err := Classify("ERROR: something totally unexpected happened", exitErr)
	if !errors.Is(err, exitErr) {
		t.Fatalf("want fallback to exitErr, got %v", err)
	}
}

// TestClassify_unrecognizedKeepsStderr is the regression guard for the bug
// this file used to have: an unrecognized failure surfaced to the API as a
// bare "exit status 1" and yt-dlp's own explanation was discarded, leaving
// nothing to debug from.
func TestClassify_unrecognizedKeepsStderr(t *testing.T) {
	exitErr := fmt.Errorf("exit status 1")
	stderr := "WARNING: [youtube] noise nobody needs\n" +
		"ERROR: [youtube] vynCRZwkWhE: Failed to extract any player response"

	err := Classify(stderr, exitErr)

	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("want *ExecError, got %T (%v)", err, err)
	}
	if !strings.Contains(err.Error(), "Failed to extract any player response") {
		t.Fatalf("error text lost yt-dlp's reason: %q", err.Error())
	}
	// The ERROR: line wins over the WARNING noise above it.
	if strings.Contains(ee.Stderr, "noise nobody needs") {
		t.Fatalf("kept warning noise instead of the ERROR line: %q", ee.Stderr)
	}
	// Unwrapping still reaches the process error, so existing errors.Is
	// checks on *exec.ExitError keep working.
	if !errors.Is(err, exitErr) {
		t.Fatalf("ExecError no longer unwraps to exitErr: %v", err)
	}
}

// TestClassify_stderrWithoutErrorLines covers the case that actually bit
// here: yt-dlp exits non-zero having printed only WARNING lines (e.g. the
// missing-JS-runtime warning). There is no ERROR: line to prefer, so the
// warnings themselves are the best available explanation and must survive.
func TestClassify_stderrWithoutErrorLines(t *testing.T) {
	stderr := "WARNING: [youtube] No supported JavaScript runtime could be found."

	err := Classify(stderr, fmt.Errorf("exit status 1"))

	if !strings.Contains(err.Error(), "No supported JavaScript runtime") {
		t.Fatalf("warning-only stderr was dropped: %q", err.Error())
	}
}

func TestStderrTail_capsRunawayOutput(t *testing.T) {
	huge := "ERROR: " + strings.Repeat("x", 5000)

	got := stderrTail(huge)

	if len(got) > maxStderrTail+len("…") {
		t.Fatalf("tail not capped: %d chars", len(got))
	}
}

func TestClassify_noErrorNoStderr(t *testing.T) {
	if err := Classify("", nil); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}
