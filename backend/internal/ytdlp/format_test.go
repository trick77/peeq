package ytdlp

import "testing"

// TestResolve_knownPresets locks the exact -f selector strings for each
// built-in preset id. These strings are load-bearing: they are handed
// straight to yt-dlp's -f flag, so any accidental edit changes what gets
// downloaded.
func TestResolve_knownPresets(t *testing.T) {
	cases := []struct {
		preset string
		want   string
	}{
		{"apple-1080p", "bestvideo[height<=1080][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4"},
		{"apple-720p", "bestvideo[height<=720][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4"},
		{"best-mp4", "bestvideo+bestaudio/best"},
	}
	for _, c := range cases {
		t.Run(c.preset, func(t *testing.T) {
			got, err := Resolve(c.preset, "")
			if err != nil {
				t.Fatalf("Resolve(%q, \"\"): %v", c.preset, err)
			}
			if got != c.want {
				t.Fatalf("Resolve(%q, \"\") = %q, want %q", c.preset, got, c.want)
			}
		})
	}
}

func TestResolve_custom_returnsCustomString(t *testing.T) {
	got, err := Resolve("custom", "bestvideo+bestaudio")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "bestvideo+bestaudio" {
		t.Fatalf("Resolve(custom) = %q, want %q", got, "bestvideo+bestaudio")
	}
}

func TestResolve_custom_emptyErrors(t *testing.T) {
	if _, err := Resolve("custom", ""); err == nil {
		t.Fatal("expected error for empty custom format string")
	}
}

func TestResolve_unknownPresetErrors(t *testing.T) {
	if _, err := Resolve("does-not-exist", ""); err == nil {
		t.Fatal("expected error for unknown preset id")
	}
}
