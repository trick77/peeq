package media

import "testing"

// The mime an image is served as comes from its extension, and the extension a
// stored image is named with comes back from its mime. Both directions matter:
// ServeContent infers Content-Type from the name it is handed, so a wrong
// mapping means a poster served as the wrong type.
func TestThumbnailMimeAndExtRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		path string
		mime string
		ext  string
	}{
		{"a/b/v1.jpg", "image/jpeg", ".jpg"},
		{"a/b/v1.jpeg", "image/jpeg", ".jpg"},
		{"a/b/v1.png", "image/png", ".png"},
		{"a/b/v1.webp", "image/webp", ".webp"},
		{"a/b/v1.JPG", "image/jpeg", ".jpg"},
	} {
		if got := ThumbnailMime(tc.path); got != tc.mime {
			t.Errorf("ThumbnailMime(%q) = %q, want %q", tc.path, got, tc.mime)
		}
		if got := ThumbnailExtForMime(tc.mime); got != tc.ext {
			t.Errorf("ThumbnailExtForMime(%q) = %q, want %q", tc.mime, got, tc.ext)
		}
	}
}

// Neither direction guesses. An extension outside the list peeq's own writers
// produce is octet-stream, and an unknown mime gets a neutral extension rather
// than a plausible-looking image one.
func TestThumbnailMimeAndExt_unknownIsNeutral(t *testing.T) {
	if got := ThumbnailMime("a/b/v1.gif"); got != "application/octet-stream" {
		t.Errorf("ThumbnailMime(gif) = %q, want application/octet-stream", got)
	}
	if got := ThumbnailMime("a/b/noextension"); got != "application/octet-stream" {
		t.Errorf("ThumbnailMime(no ext) = %q, want application/octet-stream", got)
	}
	if got := ThumbnailExtForMime("image/gif"); got != ".bin" {
		t.Errorf("ThumbnailExtForMime(image/gif) = %q, want .bin", got)
	}
}
