package mediaprobe

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// realMP4Output is the shape ffprobe actually returns for a yt-dlp mp4,
// trimmed to the keys mediaprobe reads. The bin_data stream is real: mp4s
// muxed by ffmpeg routinely carry one, and it is why the parser switches on
// codec_type instead of indexing streams.
const realMP4Output = `{
  "streams": [
    {"codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080, "profile": "Main"},
    {"codec_type": "audio", "codec_name": "aac", "profile": "LC"},
    {"codec_type": "data", "codec_name": "bin_data"}
  ]
}`

func stubRun(out string, err error) func(context.Context, string, ...string) ([]byte, error) {
	return func(context.Context, string, ...string) ([]byte, error) {
		return []byte(out), err
	}
}

func TestProbe_readsCodecsAndHeight(t *testing.T) {
	p := New(Config{Run: stubRun(realMP4Output, nil)})

	got, err := p.Probe(context.Background(), "/media/x/y/y.mp4")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got.VideoCodec != "h264" || got.VideoHeight != 1080 {
		t.Errorf("video = %q/%d, want h264/1080", got.VideoCodec, got.VideoHeight)
	}
	if got.AudioCodec != "aac" {
		t.Errorf("AudioCodec = %q, want aac", got.AudioCodec)
	}
	if got.Empty() {
		t.Error("Empty() = true for a fully populated probe")
	}
}

// The container must come from the extension. ffprobe's format_name for an
// mp4 is "mov,mp4,m4a,3gp,3g2,mj2" — reading its first entry would label
// every mp4 MOV, which is the bug this test exists to prevent.
func TestProbe_containerComesFromTheExtension(t *testing.T) {
	p := New(Config{Run: stubRun(realMP4Output, nil)})

	for path, want := range map[string]string{
		"/media/a/b/b.mp4":  "mp4",
		"/media/a/b/b.MKV":  "mkv",
		"/media/a/b/b.webm": "webm",
		"/media/a/b/b":      "",
	} {
		got, err := p.Probe(context.Background(), path)
		if err != nil {
			t.Fatalf("Probe(%s): %v", path, err)
		}
		if got.Container != want {
			t.Errorf("Container for %s = %q, want %q", path, got.Container, want)
		}
	}
}

func TestProbe_firstStreamOfEachKindWins(t *testing.T) {
	const multi = `{"streams": [
		{"codec_type": "audio", "codec_name": "opus"},
		{"codec_type": "audio", "codec_name": "aac"},
		{"codec_type": "video", "codec_name": "vp9", "height": 2160},
		{"codec_type": "video", "codec_name": "h264", "height": 720}
	]}`
	p := New(Config{Run: stubRun(multi, nil)})

	got, err := p.Probe(context.Background(), "/m/v.webm")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got.AudioCodec != "opus" {
		t.Errorf("AudioCodec = %q, want opus", got.AudioCodec)
	}
	if got.VideoCodec != "vp9" || got.VideoHeight != 2160 {
		t.Errorf("video = %q/%d, want vp9/2160", got.VideoCodec, got.VideoHeight)
	}
}

// An audio-only or video-only file is still worth reporting, so a missing
// track leaves its field empty rather than failing the probe.
func TestProbe_missingTrackIsNotAnError(t *testing.T) {
	const videoOnly = `{"streams": [{"codec_type": "video", "codec_name": "av01", "height": 1440}]}`
	p := New(Config{Run: stubRun(videoOnly, nil)})

	got, err := p.Probe(context.Background(), "/m/v.mp4")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got.AudioCodec != "" {
		t.Errorf("AudioCodec = %q, want empty", got.AudioCodec)
	}
	if got.VideoCodec != "av01" {
		t.Errorf("VideoCodec = %q, want av01", got.VideoCodec)
	}
}

func TestProbe_emptyReportsNothingWorthShowing(t *testing.T) {
	p := New(Config{Run: stubRun(`{"streams": []}`, nil)})

	got, err := p.Probe(context.Background(), "/m/v.mp4")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !got.Empty() {
		t.Errorf("Empty() = false for %+v", got)
	}
}

func TestProbe_binaryFailureIsAnError(t *testing.T) {
	p := New(Config{Run: stubRun("", errors.New("exit status 1"))})

	if _, err := p.Probe(context.Background(), "/m/gone.mp4"); err == nil {
		t.Fatal("Probe: want error, got nil")
	} else if !strings.Contains(err.Error(), "/m/gone.mp4") {
		t.Errorf("error %q does not name the file", err)
	}
}

func TestProbe_unparseableOutputIsAnError(t *testing.T) {
	p := New(Config{Run: stubRun("not json", nil)})

	if _, err := p.Probe(context.Background(), "/m/v.mp4"); err == nil {
		t.Fatal("Probe: want error, got nil")
	}
}

func TestNew_defaultsToFfprobeOnPath(t *testing.T) {
	if got := New(Config{}).bin; got != "ffprobe" {
		t.Errorf("bin = %q, want ffprobe", got)
	}
	if got := New(Config{Bin: "/opt/ffprobe"}).bin; got != "/opt/ffprobe" {
		t.Errorf("bin = %q, want /opt/ffprobe", got)
	}
}

// Every test above injects Config.Run, so the default runner — the one
// production actually uses — would otherwise never be executed at all. Driving
// it through /bin/echo pins that it runs the named binary, passes the args
// through in order, and hands back stdout.
func TestNew_defaultRunnerExecutesTheBinary(t *testing.T) {
	p := New(Config{Bin: "/bin/echo"})

	out, err := p.run(context.Background(), p.bin, "-show_streams", "/m/v.mp4")
	if err != nil {
		t.Fatalf("default runner: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "-show_streams /m/v.mp4" {
		t.Errorf("stdout = %q, want the args echoed back in order", got)
	}
}

// A binary that is not there must surface as an error, not an empty parse:
// Probe's caller distinguishes "ffprobe is missing" from "this file has no
// streams", and only the former should keep the row queued for a retry.
func TestNew_defaultRunnerReportsAMissingBinary(t *testing.T) {
	p := New(Config{Bin: "/nonexistent/ffprobe"})

	if _, err := p.run(context.Background(), p.bin); err == nil {
		t.Fatal("default runner: want an error for a missing binary, got nil")
	}
}
