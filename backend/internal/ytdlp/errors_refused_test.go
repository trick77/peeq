package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// YouTube answers a refused address or session with the wording of a dead
// video. Captured from a server whose IPv4 address was blocked: the same
// videos played in a browser and over the server's IPv6. Classified terminal
// ("deleted"), each one was failed and parked for two weeks. It is a run-wide
// condition, matched before "video unavailable".
func TestClassify_hostRefused(t *testing.T) {
	for _, stderr := range []string{
		// Curly apostrophe, as YouTube sends it.
		"ERROR: [youtube] UWqFLDO48dE: Video unavailable. This content isn’t available.",
		// Straight apostrophe.
		"ERROR: [youtube] UWqFLDO48dE: Video unavailable. This content isn't available.",
		// yt-dlp's own rate-limit rendering.
		"ERROR: [youtube] UWqFLDO48dE: Video unavailable. This content isn't available, try again later. " +
			"The current session has been rate-limited by YouTube for up to an hour.",
	} {
		t.Run(stderr, func(t *testing.T) {
			err := Classify(stderr, fmt.Errorf("exit status 1"))
			if !errors.Is(err, ErrHostRefused) {
				t.Fatalf("want ErrHostRefused, got %T: %v", err, err)
			}
			// yt-dlp's own words travel with it.
			if !strings.Contains(err.Error(), "UWqFLDO48dE") {
				t.Fatalf("error %q lost the stderr detail", err)
			}
		})
	}
}

// A specific reason beside the generic sentence keeps its own verdict: the
// refusal match must not turn a members-only, geo or private video into a
// server-wide pause.
func TestClassify_hostRefused_specificReasonWins(t *testing.T) {
	cases := []struct{ stderr, reason string }{
		{"ERROR: [youtube] x: Private video. This content isn't available.", "private"},
		{"ERROR: [youtube] x: Join this channel to get access. This content isn't available.", "members"},
		{"ERROR: [youtube] x: This video is not available in your country. This content isn't available.", "geo"},
		{"ERROR: [youtube] x: Sign in to confirm your age. This content isn't available.", "age"},
	}
	for _, c := range cases {
		t.Run(c.reason, func(t *testing.T) {
			var te *TerminalError
			err := Classify(c.stderr, fmt.Errorf("exit status 1"))
			if !errors.As(err, &te) || te.Reason != c.reason {
				t.Fatalf("want TerminalError %q, got %v", c.reason, err)
			}
		})
	}
}

// A plain "Video unavailable" with no refusal wording stays terminal.
func TestClassify_plainVideoUnavailableStaysTerminal(t *testing.T) {
	err := Classify("ERROR: [youtube] dQw4w9WgXcQ: Video unavailable. This video has been removed by the uploader", fmt.Errorf("exit status 1"))
	var te *TerminalError
	if !errors.As(err, &te) || te.Reason != "deleted" {
		t.Fatalf("want TerminalError deleted, got %v", err)
	}
}

// The Runner acts on a refusal itself: it throws the kill switch through
// OnHostRefused, once, and hands the caller an ErrPaused, which every worker
// already settles without burning an attempt, parking a video or counting a
// failure. Every further call would go out on the same refused address.
func TestExec_hostRefused_throwsKillSwitch(t *testing.T) {
	script := filepath.Join(t.TempDir(), "refused.sh")
	content := "#!/bin/sh\necho 'ERROR: [youtube] x: Video unavailable. This content isn’t available.' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	var got []error
	r := New(RunnerConfig{
		Bin:            script,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		OnHostRefused:  func(err error) error { got = append(got, err); return nil },
	})
	_, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ")
	if !errors.Is(err, ErrPaused) || !errors.Is(err, ErrHostRefused) {
		t.Fatalf("want ErrPaused and ErrHostRefused, got %v", err)
	}
	if len(got) != 1 || !errors.Is(got[0], ErrHostRefused) {
		t.Fatalf("OnHostRefused calls = %v, want one ErrHostRefused", got)
	}
}

// Only an ERROR line counts. peeq passes several player clients, and a WARNING
// about one of them saying the content isn't available must not throw the
// kill switch for a run that failed for another reason.
func TestClassify_hostRefused_warningLineIgnored(t *testing.T) {
	stderr := "WARNING: [youtube] x: web_embedded client: This content isn't available.\n" +
		"ERROR: unable to download video data: HTTP Error 503: Service Unavailable"
	err := Classify(stderr, fmt.Errorf("exit status 1"))
	if errors.Is(err, ErrHostRefused) {
		t.Fatalf("a WARNING line classified as ErrHostRefused: %v", err)
	}
}

// A channel-gone verdict beside the refusal sentence keeps auto-unsubscribe
// working.
func TestClassify_hostRefused_channelGoneWins(t *testing.T) {
	stderr := "ERROR: [youtube:tab] UCzzzzzzzzzzzzzzzzzzzzzz: YouTube said: This channel does not exist. This content isn't available."
	var te *TerminalError
	if err := Classify(stderr, fmt.Errorf("exit status 1")); !errors.As(err, &te) || te.Reason != "deleted" {
		t.Fatalf("want TerminalError deleted, got %v", err)
	}
}

// When the pause cannot be written, the caller must not be told "paused":
// the worker would requeue without an attempt, the gate would read "not
// paused", and the same job would hit the refused address forever.
func TestExec_hostRefused_pauseWriteFails_staysAFailure(t *testing.T) {
	script := filepath.Join(t.TempDir(), "refused.sh")
	content := "#!/bin/sh\necho \"ERROR: [youtube] x: Video unavailable. This content isn't available.\" >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	r := New(RunnerConfig{
		Bin:            script,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		OnHostRefused:  func(error) error { return errors.New("database is locked") },
	})
	_, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ")
	if errors.Is(err, ErrPaused) {
		t.Fatalf("got ErrPaused although the pause was not written: %v", err)
	}
	if !errors.Is(err, ErrHostRefused) {
		t.Fatalf("want ErrHostRefused, got %v", err)
	}
}

// A refusal is "not now": the partial file of a long download must survive
// for --continue once the user resumes.
func TestDownload_hostRefused_leavesStagingForContinue(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "refusedVid1"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_STDERR", "ERROR: [youtube] refusedVid1: Video unavailable. This content isn't available.")
	t.Setenv("FAKE_YTDLP_EXIT", "1")

	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
		OnHostRefused:  func(error) error { return nil },
	})
	_, err := r.Download(context.Background(), DownloadReq{URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4"}, nil)
	if !errors.Is(err, ErrHostRefused) {
		t.Fatalf("want ErrHostRefused, got %v", err)
	}
	if _, e := os.Stat(filepath.Join(mediaDir, ".staging", id)); e != nil {
		t.Fatalf("staging dir should survive a host refusal: %v", e)
	}
}
