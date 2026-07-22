package download

import "testing"

// TestFreeBytes covers the exported preflight helper (and the freeBytes
// syscall it wraps): a real, writable directory reports a positive figure.
func TestFreeBytes(t *testing.T) {
	n, err := FreeBytes(t.TempDir())
	if err != nil {
		t.Fatalf("FreeBytes: %v", err)
	}
	if n == 0 {
		t.Error("FreeBytes = 0, want a positive free-space figure for a real filesystem")
	}
}
