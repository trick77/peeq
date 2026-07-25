// Package mediaprobe reads the handful of facts about a downloaded media
// file that the player shows: container, resolution and the two codecs.
//
// It shells out to ffprobe, which is already in the runtime image because
// yt-dlp needs ffmpeg to merge separate video and audio streams. mediainfo
// would report the same facts but would add an apt package for nothing.
//
// The values here are ffprobe's own ("h264", "aac", 1080). Turning those
// into something a person reads ("H.264", "AAC", "1080p") is the UI's job,
// the same split the short category labels use: the wire carries the raw
// value so the display wording can change without a migration.
package mediaprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// Info is what a successful Probe produces. Every field is best-effort: a
// file with no audio track yields an empty AudioCodec rather than an error,
// because a partial answer is still worth showing.
type Info struct {
	// Container is the lowercase file extension without its dot ("mp4",
	// "mkv", "webm").
	//
	// It deliberately does NOT come from ffprobe's format_name: for an mp4
	// that field is the shared demuxer list "mov,mp4,m4a,3gp,3g2,mj2", whose
	// first entry is "mov" — so reading it would label every mp4 as MOV. The
	// extension is authoritative here because yt-dlp names the file after the
	// container it actually muxed.
	Container string
	// VideoCodec is ffprobe's codec_name for the first video stream ("h264",
	// "vp9", "av01"), or "" if the file has no video stream.
	VideoCodec string
	// VideoHeight is the first video stream's pixel height (1080), or 0 if
	// unknown. Height alone drives the resolution label; width is not stored
	// because the label is "1080p", never "1920x1080".
	VideoHeight int64
	// AudioCodec is ffprobe's codec_name for the first audio stream ("aac",
	// "opus"), or "" if the file has no audio stream.
	AudioCodec string
}

// Empty reports whether the probe found nothing worth showing. A caller can
// use it to distinguish "probed, but the file told us nothing" from a real
// error.
func (i Info) Empty() bool {
	return i.VideoCodec == "" && i.AudioCodec == "" && i.VideoHeight == 0
}

// ffprobeOutput is the subset of `ffprobe -print_format json` that matters.
type ffprobeOutput struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		CodecName string `json:"codec_name"`
		Height    int64  `json:"height"`
	} `json:"streams"`
}

// Config configures a Prober. Run is injectable so tests drive a fixture
// instead of needing a real ffprobe binary.
type Config struct {
	// Bin is the path to (or name of) the ffprobe executable. Defaults to
	// "ffprobe", resolved on PATH.
	Bin string
	// Run executes bin with args and returns its stdout. Defaults to a real
	// exec.CommandContext.
	Run func(ctx context.Context, bin string, args ...string) ([]byte, error)
}

// Prober probes local media files.
type Prober struct {
	bin string
	run func(ctx context.Context, bin string, args ...string) ([]byte, error)
}

// New returns a Prober, filling in the production defaults for anything
// Config leaves unset.
func New(cfg Config) *Prober {
	p := &Prober{bin: cfg.Bin, run: cfg.Run}
	if p.bin == "" {
		p.bin = "ffprobe"
	}
	if p.run == nil {
		p.run = execRun
	}
	return p
}

func execRun(ctx context.Context, bin string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, bin, args...).Output()
}

// Probe reads path and returns what ffprobe reports about it.
//
// A missing file, an unreadable one, or anything ffprobe refuses to parse is
// an error; the caller is expected to treat that as non-fatal and record the
// attempt anyway, so a permanently unprobeable file is not retried forever.
func (p *Prober) Probe(ctx context.Context, path string) (Info, error) {
	out, err := p.run(ctx, p.bin,
		"-v", "quiet",
		"-print_format", "json",
		"-show_streams",
		path,
	)
	if err != nil {
		return Info{}, fmt.Errorf("mediaprobe: run ffprobe on %s: %w", path, err)
	}
	var parsed ffprobeOutput
	if err := json.Unmarshal(out, &parsed); err != nil {
		return Info{}, fmt.Errorf("mediaprobe: parse ffprobe output for %s: %w", path, err)
	}

	info := Info{Container: containerOf(path)}
	// First stream of each kind wins. A file can carry several (alternate
	// audio languages) plus non-media streams such as bin_data, which is why
	// this switches on codec_type rather than indexing.
	for _, s := range parsed.Streams {
		switch s.CodecType {
		case "video":
			if info.VideoCodec == "" {
				info.VideoCodec = s.CodecName
				info.VideoHeight = s.Height
			}
		case "audio":
			if info.AudioCodec == "" {
				info.AudioCodec = s.CodecName
			}
		}
	}
	return info, nil
}

// containerOf derives the container from the file extension, lowercased and
// stripped of its dot. See Info.Container for why not format_name.
func containerOf(path string) string {
	return strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
}
