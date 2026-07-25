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

// TestRemoveTombstonedVideoFilesKeepsThumbnail pins the one difference
// between the two removal flavours: a tombstone keeps the row, so it keeps
// the poster with it. Deleting the thumbnail here while videos.Tombstone
// left thumbnail_path set is what made every tombstoned card request an
// image that 404s.
func TestRemoveTombstonedVideoFilesKeepsThumbnail(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "vid.mp4")
	thumb := filepath.Join(dir, "vid.jpg")
	sub := filepath.Join(dir, "vid.en.vtt")
	for _, p := range []string{mediaPath, thumb, sub} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	RemoveTombstonedVideoFiles(dir, "vid.mp4", "vid.en.vtt")

	if _, err := os.Stat(mediaPath); !os.IsNotExist(err) {
		t.Errorf("media file still present, want removed")
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Errorf("subtitle file still present, want removed")
	}
	if _, err := os.Stat(thumb); err != nil {
		t.Errorf("thumbnail gone, want kept: %v", err)
	}
}

// TestRemoveVideoFilesRemovesThumbnail is the counterpart: a hard delete
// (the channel cascade, where the row goes too) leaves nothing behind.
func TestRemoveVideoFilesRemovesThumbnail(t *testing.T) {
	dir := t.TempDir()
	thumb := filepath.Join(dir, "vid.jpg")
	if err := os.WriteFile(thumb, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	RemoveVideoFiles(dir, "", "vid.jpg", "")

	if _, err := os.Stat(thumb); !os.IsNotExist(err) {
		t.Errorf("thumbnail still present, want removed")
	}
}
