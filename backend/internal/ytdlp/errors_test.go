package ytdlp

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
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
			// Verbatim stderr captured by running yt-dlp against a channel id
			// that does not exist. This is the load-bearing case for the
			// auto-unsubscribe feature: Task 1's Classify only matched
			// video-level "unavailable" text, so a genuinely deleted CHANNEL
			// never classified as TerminalError{Reason:"deleted"} and
			// staleUnsubscribe was never reached. Asserting on this exact,
			// observed line (rather than a hand-built TerminalError) is the
			// point of this test — a fake string that merely "looks close"
			// would not have caught the original bug.
			name:   "channel does not exist",
			stderr: "ERROR: [youtube:tab] UCzzzzzzzzzzzzzzzzzzzzzz: YouTube said: This channel does not exist.",
			check: func(t *testing.T, err error) {
				var te *TerminalError
				if !errors.As(err, &te) {
					t.Fatalf("want *TerminalError, got %v", err)
				}
				if te.Reason != "deleted" {
					t.Fatalf("Reason = %q, want %q", te.Reason, "deleted")
				}
			},
		},
		{
			// UNVERIFIED BY OBSERVATION: this is YouTube's known account-
			// termination wording, but no terminated channel was available to
			// run yt-dlp against, so this exact stderr line has not been
			// confirmed against the real binary the way the "does not exist"
			// case above was. Keep classifying it as "deleted" — if the real
			// wording differs, this test (not production behavior) is what's
			// wrong, and should be corrected once a real sample is captured.
			name:   "account terminated (unverified wording)",
			stderr: "ERROR: [youtube:tab] UCzzzzzzzzzzzzzzzzzzzzzz: YouTube said: This account has been terminated.",
			check: func(t *testing.T, err error) {
				var te *TerminalError
				if !errors.As(err, &te) {
					t.Fatalf("want *TerminalError, got %v", err)
				}
				if te.Reason != "deleted" {
					t.Fatalf("Reason = %q, want %q", te.Reason, "deleted")
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

// TestClassify_channelDeletedSignatures verifies that channel-level
// "deleted" signatures (channel does not exist, account terminated) are
// recognized and classified as TerminalError{Reason:"deleted"}. This is
// essential for auto-unsubscribe to work: a deleted channel looks different
// from a deleted video, and this test ensures yt-dlp's channel-level
// signatures do not collide with other error cases.
func TestClassify_channelDeletedSignatures(t *testing.T) {
	genericExit := fmt.Errorf("exit status 1")

	for _, stderr := range []string{
		"ERROR: [youtube:tab] UCzzzzzzzzzzzzzzzzzzzzzz: YouTube said: This channel does not exist.",
		"ERROR: [youtube:tab] UCzzzzzzzzzzzzzzzzzzzzzz: YouTube said: This account has been terminated.",
	} {
		err := Classify(stderr, genericExit)
		var te *TerminalError
		if !errors.As(err, &te) {
			t.Fatalf("%q: want *TerminalError, got %v", stderr, err)
		}
		if te.Reason != "deleted" {
			t.Fatalf("%q: Reason = %q, want %q", stderr, te.Reason, "deleted")
		}
	}
}

// TestClassify_precedence pins the switch-case ordering in Classify, which
// is safety-critical: if two signatures appear in the same stderr, the
// earliest matching case in the switch decides the outcome. This matters most
// for bot-block vs. channel-deleted: bot detection (line 140) must beat
// channel deletion (line 155) to prevent misclassification. Concretely, when
// YouTube's bot detection rejects a request to scan a deleted channel, the
// stderr will contain both "confirm you're not a bot" and "channel does not
// exist". If someone reorders the switch so deleted comes first, that stderr
// would incorrectly classify as "deleted" instead of ErrBlocked, feeding
// peeq's dead-scan counter during an untrustworthy bot-block. This test
// prevents that reordering.
func TestClassify_precedence(t *testing.T) {
	genericExit := fmt.Errorf("exit status 1")

	cases := []struct {
		name                 string
		stderr               string
		expectErrIs          error
		expectTerminalReason string
	}{
		{
			// Bot-block signature appears on one line, channel-deleted on
			// another. Bot-block case comes earlier in switch, so it wins.
			name: "bot-block beats channel-deleted",
			stderr: "ERROR: [youtube] dQw4w9WgXcQ: Sign in to confirm you're not a bot\n" +
				"ERROR: [youtube:tab] UCzzzzzzzzzzzzzzzzzzzzzz: YouTube said: This channel does not exist.",
			expectErrIs: ErrBlocked,
		},
		{
			// Cookie-expired signature on one line, channel-deleted on another.
			// Cookie case comes earlier in switch, so it wins.
			name: "cookie-expired beats channel-deleted",
			stderr: "ERROR: [youtube] dQw4w9WgXcQ: The provided YouTube account cookies are no longer valid. Try using --cookies-from-browser instead\n" +
				"ERROR: [youtube:tab] UCzzzzzzzzzzzzzzzzzzzzzz: YouTube said: This account has been terminated.",
			expectErrIs: ErrCookieExpired,
		},
		{
			// Channel-deleted alone: confirm it still works when no other
			// signature is present.
			name:                 "channel-deleted alone",
			stderr:               "ERROR: [youtube:tab] UCzzzzzzzzzzzzzzzzzzzzzz: YouTube said: This channel does not exist.",
			expectTerminalReason: "deleted",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Classify(c.stderr, genericExit)

			// If this case expects a sentinel error (ErrBlocked, ErrCookieExpired)
			if c.expectErrIs != nil {
				if !errors.Is(err, c.expectErrIs) {
					t.Fatalf("want %v, got %v", c.expectErrIs, err)
				}
				return
			}

			// Otherwise, expect a TerminalError with a specific Reason
			var te *TerminalError
			if !errors.As(err, &te) {
				t.Fatalf("want *TerminalError, got %v", err)
			}
			if c.expectTerminalReason != "" && te.Reason != c.expectTerminalReason {
				t.Fatalf("Reason = %q, want %q", te.Reason, c.expectTerminalReason)
			}
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
		t.Fatalf("tail not capped: %d bytes", len(got))
	}
}

// TestStderrTail_truncatesOnRuneBoundary guards the cap against splitting a
// multi-byte character. yt-dlp echoes video titles into its ERROR lines, so
// non-ASCII at the cut point is routine; a byte-index slice would emit
// invalid UTF-8 into the 502 body and the jobs.last_error column, where
// encoding/json silently swaps in U+FFFD rather than failing loudly.
func TestStderrTail_truncatesOnRuneBoundary(t *testing.T) {
	// "世" is 3 bytes, so repeating it guarantees the byte cap lands
	// mid-rune for at least some of the offsets tested below.
	for _, pad := range []int{0, 1, 2} {
		line := "ERROR: " + strings.Repeat("a", pad) + strings.Repeat("世", 400)

		got := stderrTail(line)

		if !utf8.ValidString(got) {
			t.Fatalf("pad=%d: tail is not valid UTF-8: %q", pad, got)
		}
		if len(got) > maxStderrTail+len("…") {
			t.Fatalf("pad=%d: tail not capped: %d bytes", pad, len(got))
		}
	}
}

func TestClassify_noErrorNoStderr(t *testing.T) {
	if err := Classify("", nil); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}
