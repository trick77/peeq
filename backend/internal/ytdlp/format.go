package ytdlp

import "fmt"

// Presets maps a format preset id to the yt-dlp `-f` format selector
// string. The "apple-*" presets constrain the codec so the file plays on
// Apple hardware without transcoding, but they do not reach the same
// places: "apple-1080p" pins H.264/AAC because AirPlay carries H.264 and
// HEVC only, while "apple-vp9-4k" trades AirPlay away for resolution —
// VP9 is the one high-res codec an Apple TV 4K and Safari hardware-decode.
// It still pins AAC audio, since Opus would push the merge out of the mp4
// container the download always asks for (--merge-output-format mp4).
// "best-mp4" takes whatever the best available video+audio is, which on
// YouTube today usually means AV1 and Opus.
var Presets = map[string]string{
	"apple-1080p":  avc1080 + "/mp4",
	"apple-vp9-4k": vp94k + "/" + avc1080 + "/mp4",
	"best-mp4":     "bestvideo+bestaudio/best",
}

// avc1080 and vp94k cap the SHORT side, which is what "1080p" and "4K"
// mean. A plain [height<=N] only says so for landscape: a portrait Short's
// 1080p rung is 1080x1920, so apple-1080p fell to 480x854. yt-dlp cannot
// filter on min(width, height) ([res<=1080] matches nothing and drops to the
// 360p "mp4" fallback), so the two orientations are spelled apart. A square
// video takes the landscape branch, and so does a format with no known
// aspect ratio ('?'), which [height<=N] alone matched before.
const (
	avc1080 = "bestvideo[height<=1080][aspect_ratio>=?1][vcodec*=avc1]+bestaudio[acodec*=mp4a]" +
		"/bestvideo[width<=1080][aspect_ratio<1][vcodec*=avc1]+bestaudio[acodec*=mp4a]"
	vp94k = "bestvideo[height<=2160][aspect_ratio>=?1][vcodec*=vp9]+bestaudio[acodec*=mp4a]" +
		"/bestvideo[width<=2160][aspect_ratio<1][vcodec*=vp9]+bestaudio[acodec*=mp4a]"
)

// IsPreset reports whether id names an entry in Presets. "custom" is
// deliberately NOT a preset: it is Resolve's escape hatch for a
// hand-written selector, and a stored value of "custom" with no selector
// beside it would resolve to the nonsense format string "custom".
//
// This exists for callers holding a value that is EITHER a preset id or a
// raw selector — a channel's format_override, which held raw selectors
// before the picker existed — so they can tell the two apart without
// reaching into the map from another package.
func IsPreset(id string) bool {
	_, ok := Presets[id]
	return ok
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
