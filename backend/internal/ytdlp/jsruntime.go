package ytdlp

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// JSRuntime runs `<bin> --version` and returns the runtime it found, in the
// same display form yt-dlp uses in its own debug output (e.g. "deno-2.9.3").
//
// yt-dlp needs a JavaScript runtime to solve YouTube's sig and n challenges;
// without one it falls back to a deprecated Python reimplementation where
// formats silently disappear. Nothing here touches YouTube, so it does NOT
// go through the Runner's cookie gate or throttle — it only inspects a local
// binary.
//
// Real `deno --version` prints three lines (deno, v8, typescript); only the
// first is parsed, and output that does not match is an error rather than a
// silently-passed-through string.
func JSRuntime(ctx context.Context, bin string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, "--version")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ytdlp: jsruntime: run %s: %w: %s",
			bin, err, strings.TrimSpace(stderr.String()))
	}

	first, _, _ := strings.Cut(strings.TrimSpace(stdout.String()), "\n")
	fields := strings.Fields(first)
	if len(fields) < 2 || fields[0] != "deno" {
		return "", fmt.Errorf("ytdlp: jsruntime: %s: unrecognized --version output %q", bin, first)
	}
	return fields[0] + "-" + fields[1], nil
}
