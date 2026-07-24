package download

import "testing"

// TestFreeBytes covers the freeBytes syscall helper: a real, writable
// directory reports a positive figure.
func TestFreeBytes(t *testing.T) {
	n, err := freeBytes(t.TempDir())
	if err != nil {
		t.Fatalf("freeBytes: %v", err)
	}
	if n == 0 {
		t.Error("freeBytes = 0, want a positive free-space figure for a real filesystem")
	}
}
