package ytdlp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A line longer than the scanner's buffer stops the scan. If nothing reads the
// pipe after that, the child blocks on its next write and the call hangs until
// its context ends — for a download, the ten-minute watchdog. The rest of the
// output must be drained so the process can finish and the read error surface.
func TestExecWithProgress_overlongLineDoesNotHangTheCall(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-ytdlp-long.sh")
	content := "#!/bin/sh\n" +
		"head -c 2000000 /dev/zero | tr '\\0' 'a'\n" +
		"echo\n" +
		"head -c 2000000 /dev/zero | tr '\\0' 'b'\n" +
		"echo\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	r := New(RunnerConfig{
		Bin:            script,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	started := time.Now()
	_, err := r.execWithProgress(ctx, func(string) {}, "ignored")
	if err == nil || !strings.Contains(err.Error(), "read stdout") {
		t.Fatalf("err = %v, want the stdout read error", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("call took %s: it waited for the context instead of draining stdout", elapsed)
	}
}
