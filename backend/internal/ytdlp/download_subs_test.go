package ytdlp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDownload_requestsSubtitlesAndCapturesLanguage locks in Task 9:
// subtitle flags ride the existing Download invocation (no new call path),
// watchURL stays the last arg, and the resulting Result exposes the
// subtitle path, audio language, and yt-dlp's own (non-SponsorBlock)
// chapters as JSON.
func TestDownload_requestsSubtitlesAndCapturesLanguage(t *testing.T) {
	mediaDir := t.TempDir()
	captureOut := filepath.Join(t.TempDir(), "capture.out")
	captureScript := filepath.Join(t.TempDir(), "capture.sh")
	fake := fakeBinPath(t)
	content := "#!/bin/sh\n" +
		"echo \"$@\" > '" + captureOut + "'\n" +
		"exec '" + fake + "' \"$@\"\n"
	if err := os.WriteFile(captureScript, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	const id = "subsVideo01"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_CHANNEL_ID", "UCsubs")

	r := New(RunnerConfig{
		Bin:            captureScript,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
	})
	res, err := r.Download(context.Background(), DownloadReq{
		URL:     "https://youtu.be/" + id,
		VideoID: id,
		Format:  "best-mp4",
		SubLang: "en",
	}, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	argv, err := os.ReadFile(captureOut)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	argLine := string(argv)
	for _, want := range []string{"--write-subs", "--write-auto-subs", "--sub-langs en", "--convert-subs vtt"} {
		if !strings.Contains(argLine, want) {
			t.Fatalf("missing arg %q in %q", want, argLine)
		}
	}

	// watchURL must remain the very last arg.
	fields := strings.Fields(strings.TrimSpace(argLine))
	if len(fields) == 0 || fields[len(fields)-1] != "https://www.youtube.com/watch?v="+id {
		t.Fatalf("watchURL not last arg: %q", argLine)
	}

	if res.SubtitleRelPath == "" || !strings.HasSuffix(res.SubtitleRelPath, ".vtt") {
		t.Fatalf("SubtitleRelPath = %q, want a .vtt relative path", res.SubtitleRelPath)
	}
	if res.AudioLanguage != "en" {
		t.Fatalf("AudioLanguage = %q, want %q", res.AudioLanguage, "en")
	}
	if !strings.Contains(res.ChaptersJSON, "yt-dlp") {
		t.Fatalf("ChaptersJSON = %q, want it to contain %q", res.ChaptersJSON, "yt-dlp")
	}
	if strings.Contains(res.ChaptersJSON, "SponsorBlock") {
		t.Fatalf("ChaptersJSON = %q, must not contain SponsorBlock chapters", res.ChaptersJSON)
	}
}
