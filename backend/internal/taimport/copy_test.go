package taimport

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile_copiesAndVerifies(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	if err := os.WriteFile(src, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "sub", "dst.mp4") // parent dir must be created
	n, err := copyFile(src, dst)
	if err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	if n != 11 {
		t.Errorf("bytes = %d, want 11", n)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "hello world" {
		t.Fatalf("dst content = %q err=%v", got, err)
	}
}

func TestCopyFile_missingSourceWrapsErrNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := copyFile(filepath.Join(dir, "gone.mp4"), filepath.Join(dir, "dst.mp4"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want it to wrap fs.ErrNotExist so the caller can skip", err)
	}
}

func TestCopyFile_idempotentWhenDestMatches(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	dst := filepath.Join(dir, "dst.mp4")
	if err := os.WriteFile(src, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A dst of the same size is treated as already copied and left untouched,
	// so a re-run is cheap and non-destructive.
	if err := os.WriteFile(dst, []byte("XYZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := copyFile(src, dst)
	if err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	if n != 3 {
		t.Errorf("bytes = %d, want 3", n)
	}
	if got, _ := os.ReadFile(dst); string(got) != "XYZ" {
		t.Errorf("dst content = %q, want untouched XYZ (idempotent skip)", got)
	}
}

func TestStatSize(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "f")
	if err := os.WriteFile(f, []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if sz, ok := statSize(f); !ok || sz != 5 {
		t.Errorf("statSize(existing) = (%d,%v), want (5,true)", sz, ok)
	}
	if _, ok := statSize(filepath.Join(dir, "nope")); ok {
		t.Error("statSize(missing) ok=true, want false")
	}
	if _, ok := statSize(dir); ok {
		t.Error("statSize(dir) ok=true, want false — not a regular file")
	}
}
