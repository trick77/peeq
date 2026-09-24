package media

import (
	"os"
	"path/filepath"
	"testing"
)

// SafeMediaPath is the single guard every os.Open/os.ReadFile of a
// database-stored media path relies on, so the traversal and symlink-escape
// rejections it documents are asserted here rather than assumed.
//
// These assert the behaviour, not one internal check: containment is verified
// twice, lexically and again after symlink resolution, and the second is a
// complete backstop for the first. Probed by neutering each in turn. Removing
// the post-symlink check fails TestSafeMediaPathRejectsSymlinkEscape; removing
// the lexical one alone changes no outcome here, which is why it is called
// defence in depth rather than the load-bearing guard.

func TestSafeMediaPathAcceptsPathInsideMediaDir(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "video.mp4")
	if err := os.WriteFile(want, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := SafeMediaPath(dir, "video.mp4")
	if err != nil {
		t.Fatalf("SafeMediaPath(%q) = %v, want no error", "video.mp4", err)
	}
	resolvedWant, err := filepath.EvalSymlinks(want)
	if err != nil {
		t.Fatal(err)
	}
	if got != resolvedWant {
		t.Errorf("SafeMediaPath = %q, want %q", got, resolvedWant)
	}
}

func TestSafeMediaPathRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, stored := range []string{
		"../outside.txt",
		"sub/../../outside.txt",
		filepath.Join(filepath.Dir(dir), "outside.txt"), // absolute, outside
	} {
		if _, err := SafeMediaPath(dir, stored); err == nil {
			t.Errorf("SafeMediaPath(%q) = nil error, want rejection", stored)
		}
	}
}

func TestSafeMediaPathRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symlink that lives inside mediaDir but points out of it: the path is
	// lexically contained, so only symlink evaluation catches this.
	if err := os.Symlink(secret, filepath.Join(dir, "escape.mp4")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := SafeMediaPath(dir, "escape.mp4"); err == nil {
		t.Error("SafeMediaPath(escape.mp4) = nil error, want rejection: it resolves outside mediaDir")
	}
}

func TestSafeMediaPathRejectsEmptyInput(t *testing.T) {
	if _, err := SafeMediaPath("", "video.mp4"); err == nil {
		t.Error("SafeMediaPath with empty mediaDir = nil error, want rejection")
	}
	if _, err := SafeMediaPath(t.TempDir(), ""); err == nil {
		t.Error("SafeMediaPath with empty storedPath = nil error, want rejection")
	}
}
