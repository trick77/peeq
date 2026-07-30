package media

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveVideoFilesUnlinksSubtitle guards against a stale subtitle file
// surviving a hard delete alongside the media and thumbnail files. (A
// tombstone is the opposite case — see
// TestRemoveTombstonedVideoFilesKeepsEverythingButMedia.)
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

// TestRemoveTombstonedVideoFilesKeepsEverythingButMedia pins the difference
// between the two removal flavours: a tombstone keeps the row, so it keeps
// everything cheap that row still points at — the poster, and the .vtt the
// transcript and its re-embedding are rebuilt from. Only the media file (the
// megabytes) goes. Deleting the thumbnail here while videos.Tombstone left
// thumbnail_path set is what made every tombstoned card request an image that
// 404s; deleting the .vtt made the tombstone permanent for search.
//
// The subtitle is named <videoID>.en.vtt beside <videoID>.mp4 on purpose: that
// is how yt-dlp writes it, and it is exactly the shape RemoveMediaAndSidecars
// sweeps — so this also pins that the tombstone path does NOT go through it.
func TestRemoveTombstonedVideoFilesKeepsEverythingButMedia(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "vid.mp4")
	thumb := filepath.Join(dir, "vid.jpg")
	sub := filepath.Join(dir, "vid.en.vtt")
	for _, p := range []string{mediaPath, thumb, sub} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	RemoveTombstonedVideoFiles(dir, "vid.mp4")

	if _, err := os.Stat(mediaPath); !os.IsNotExist(err) {
		t.Errorf("media file still present, want removed")
	}
	if _, err := os.Stat(sub); err != nil {
		t.Errorf("subtitle gone, want kept: %v", err)
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

// TestRemoveVideoFilesSweepsSubtitleSidecars pins that a hard delete still
// reaches a .vtt the row never named, found only as a sidecar of the media
// file. RemoveVideoFiles used to get this by delegating to the tombstone
// flavour; now that the tombstone flavour deliberately spares sidecars, the
// hard delete has to sweep them itself or a channel delete leaves orphaned
// subtitles on disk with no row left to reference them.
func TestRemoveVideoFilesSweepsSubtitleSidecars(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "vid.mp4")
	sidecar := filepath.Join(dir, "vid.en.vtt")
	for _, p := range []string{mediaPath, sidecar} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// subtitlePath deliberately empty: the sidecar must go on the media path alone.
	RemoveVideoFiles(dir, "vid.mp4", "", "")

	if _, err := os.Stat(mediaPath); !os.IsNotExist(err) {
		t.Errorf("media file still present, want removed")
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Errorf("subtitle sidecar still present, want removed")
	}
}
