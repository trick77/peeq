package media

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveVideoFilesUnlinksSubtitle guards against a stale subtitle file
// surviving a delete/tombstone alongside the media and thumbnail files.
func TestRemoveVideoFilesUnlinksSubtitle(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "vid.en.vtt")
	if err := os.WriteFile(sub, []byte("WEBVTT\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	RemoveVideoFiles(dir, "", "", "vid.en.vtt")
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Errorf("subtitle file still present, want removed")
	}
}
