package ytdlp

import "fmt"

// Presets maps a format preset id to the yt-dlp `-f` format selector
// string. "apple-*" presets constrain to H.264/AAC (avc1/mp4a) so the
// resulting file plays natively on Apple TV/tvOS without transcoding;
// "best-mp4" takes whatever the best available video+audio is.
var Presets = map[string]string{
	"apple-1080p": "bestvideo[height<=1080][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4",
	"apple-720p":  "bestvideo[height<=720][vcodec*=avc1]+bestaudio[acodec*=mp4a]/mp4",
	"best-mp4":    "bestvideo+bestaudio/best",
}

// Resolve returns the yt-dlp `-f` selector string for preset. The special
// preset id "custom" bypasses the Presets table and returns custom
// verbatim; custom must be non-empty in that case. Any other unknown
// preset id is an error.
func Resolve(preset, custom string) (string, error) {
	if preset == "custom" {
		if custom == "" {
			return "", fmt.Errorf("ytdlp: preset \"custom\" requires a non-empty custom format string")
		}
		return custom, nil
	}
	f, ok := Presets[preset]
	if !ok {
		return "", fmt.Errorf("ytdlp: unknown format preset %q", preset)
	}
	return f, nil
}
