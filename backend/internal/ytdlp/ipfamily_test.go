package ytdlp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsIPFamily(t *testing.T) {
	for _, v := range []string{"", "4", "6"} {
		if !IsIPFamily(v) {
			t.Errorf("IsIPFamily(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"5", "ipv6", "v4", " 4", "46"} {
		if IsIPFamily(v) {
			t.Errorf("IsIPFamily(%q) = true, want false", v)
		}
	}
}

// captureArgs runs one Metadata call through a stub binary that writes its
// argv to a file, and returns that argv line.
func captureArgs(t *testing.T, cfg RunnerConfig) string {
	t.Helper()
	captureOut := filepath.Join(t.TempDir(), "capture.out")
	content := "#!/bin/sh\necho \"$@\" > '" + captureOut + "'\necho '{}'\nexit 0\n"
	script := filepath.Join(t.TempDir(), "capture.sh")
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Bin = script
	cfg.Sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := New(cfg).Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ"); err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	out, err := os.ReadFile(captureOut)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	return string(out)
}

// TestExec_ipFamily_pinsTheAddressFamily locks the setting to the ONE argv
// choke point, read per call: YouTube can block a server's IPv4 address while
// its IPv6 one still answers (and the reverse), and yt-dlp alone keeps trying
// the blocked one.
func TestExec_ipFamily_pinsTheAddressFamily(t *testing.T) {
	cases := []struct {
		family string
		want   string
		absent []string
	}{
		{"4", "--force-ipv4", []string{"--force-ipv6"}},
		{"6", "--force-ipv6", []string{"--force-ipv4"}},
		{"", "", []string{"--force-ipv4", "--force-ipv6"}},
	}
	for _, c := range cases {
		t.Run("family="+c.family, func(t *testing.T) {
			args := captureArgs(t, RunnerConfig{
				CookieProvider:   func() (string, string) { return "cookie-text", "valid" },
				IPFamilyProvider: func() string { return c.family },
			})
			if c.want != "" && !strings.Contains(args, c.want) {
				t.Fatalf("args %q must contain %q", args, c.want)
			}
			for _, a := range c.absent {
				if strings.Contains(args, a) {
					t.Fatalf("args %q must not contain %q", args, a)
				}
			}
		})
	}
}

// The block is on the host's address, not the account, so an anonymous run
// (the dev carve-out) needs the pin just as much.
func TestExec_ipFamily_appliesWithoutCookie(t *testing.T) {
	args := captureArgs(t, RunnerConfig{
		AllowAnonymous:   true,
		CookieProvider:   func() (string, string) { return "", "absent" },
		IPFamilyProvider: func() string { return "6" },
	})
	if !strings.Contains(args, "--force-ipv6") {
		t.Fatalf("args %q must contain --force-ipv6 in anonymous mode", args)
	}
}

// A value IsIPFamily refuses never reaches argv: the column's CHECK and the
// handler both refuse it, but the runner is the last line, and an unknown
// value is safer as yt-dlp's own default than as a flag guessed at.
func TestExec_ipFamily_ignoresUnknownValue(t *testing.T) {
	args := captureArgs(t, RunnerConfig{
		CookieProvider:   func() (string, string) { return "cookie-text", "valid" },
		IPFamilyProvider: func() string { return "5" },
	})
	if strings.Contains(args, "--force-ipv") {
		t.Fatalf("args %q must not pin a family for an unknown value", args)
	}
}
