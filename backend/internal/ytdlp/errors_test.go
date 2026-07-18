package ytdlp

import (
	"errors"
	"fmt"
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

func TestClassify_noErrorNoStderr(t *testing.T) {
	if err := Classify("", nil); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}
