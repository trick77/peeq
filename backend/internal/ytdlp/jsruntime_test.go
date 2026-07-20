package ytdlp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDeno writes an executable stub that prints body, and returns its path.
func fakeDeno(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "deno")
	script := "#!/bin/sh\ncat <<'EOF'\n" + body + "\nEOF\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake deno: %v", err)
	}
	return p
}

// realDenoOutput is what deno 2.9.3 actually prints — THREE lines. Tests must
// feed the real shape; a one-line fake would make the parser untestable.
const realDenoOutput = `deno 2.9.3 (stable, release, x86_64-unknown-linux-gnu)
v8 14.9.207.2-rusty
typescript 6.0.3`

func TestJSRuntime_realThreeLineOutput_parsesVersion(t *testing.T) {
	got, err := JSRuntime(context.Background(), fakeDeno(t, realDenoOutput))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "deno-2.9.3" {
		t.Fatalf("got %q, want %q", got, "deno-2.9.3")
	}
}

func TestJSRuntime_missingBinary_returnsError(t *testing.T) {
	_, err := JSRuntime(context.Background(), filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected an error for a missing binary, got nil")
	}
}

func TestJSRuntime_notExecutable_returnsError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "deno")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := JSRuntime(context.Background(), p); err == nil {
		t.Fatal("expected an error for a non-executable binary, got nil")
	}
}

// This is the test that proves the parser parses rather than echoes.
func TestJSRuntime_unrecognizedOutput_returnsError(t *testing.T) {
	for _, body := range []string{"hello world", "", "node v20.19.2"} {
		if _, err := JSRuntime(context.Background(), fakeDeno(t, body)); err == nil {
			t.Fatalf("expected an error for output %q, got nil", body)
		}
	}
}

func TestJSRuntime_errorMentionsBinary(t *testing.T) {
	_, err := JSRuntime(context.Background(), filepath.Join(t.TempDir(), "nope"))
	if err == nil || !strings.Contains(err.Error(), "jsruntime") {
		t.Fatalf("error should be package-tagged, got %v", err)
	}
}
