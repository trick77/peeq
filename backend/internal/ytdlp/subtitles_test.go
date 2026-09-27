package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// subsRunner builds a Runner whose fake binary writes argv to captureOut and
// then runs body, so a test can assert both the flags sent and what landed on
// disk. body runs with $1.. as the real argv.
func subsRunner(t *testing.T, mediaDir, captureOut, body string) *Runner {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake.sh")
	content := "#!/bin/sh\necho \"$@\" > '" + captureOut + "'\n" + body + "\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return New(RunnerConfig{
		Bin:            script,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
	})
}

// TestSubtitles_fetchesOnlyCaptions is the flag contract. Every one of these
// must match what Download sends, or the .vtt summarized from the Inbox and the
// one stored after downloading are different files — and the summary carried
// over describes a transcript the library does not have. --skip-download is the
// one that must NOT match: it is the whole point.
func TestSubtitles_fetchesOnlyCaptions(t *testing.T) {
	mediaDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv")
	const id = "capsVid0123"
	dir := SummaryDir(mediaDir, id)
	r := subsRunner(t, mediaDir, capture,
		"mkdir -p '"+dir+"' && printf 'WEBVTT\\n' > '"+filepath.Join(dir, id+".en.vtt")+"'")

	rel, err := r.Subtitles(context.Background(), id, "https://youtu.be/"+id, "en")
	if err != nil {
		t.Fatalf("Subtitles: %v", err)
	}

	argv, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	line := string(argv)
	for _, want := range []string{
		"--skip-download", "--write-subs", "--write-auto-subs",
		"--sub-langs en", "--convert-subs vtt", "--no-playlist",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("missing arg %q in %q", want, line)
		}
	}
	// Nothing that would pull down media, a thumbnail or an info.json: this
	// call exists precisely to avoid paying for any of them.
	for _, unwanted := range []string{"--write-thumbnail", "--write-info-json", "--merge-output-format", "-f "} {
		if strings.Contains(line, unwanted) {
			t.Fatalf("unexpected arg %q in %q", unwanted, line)
		}
	}

	want := filepath.Join(SummaryDirName, id, id+".en.vtt")
	if rel != want {
		t.Fatalf("rel = %q, want %q (MediaDir-relative)", rel, want)
	}
	if _, err := os.Stat(filepath.Join(mediaDir, rel)); err != nil {
		t.Fatalf("returned path does not resolve under MediaDir: %v", err)
	}
}

// TestSubtitles_noCaptionsIsNotAnError is the ordinary case for a fresh upload:
// YouTube's automatic captions run minutes to hours after publication, so
// yt-dlp exits cleanly having written nothing. Treating that as a failure would
// make the retry ladder read every fresh video as permanently caption-less.
func TestSubtitles_noCaptionsIsNotAnError(t *testing.T) {
	mediaDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv")
	r := subsRunner(t, mediaDir, capture, "exit 0")

	rel, err := r.Subtitles(context.Background(), "quietVid001", "https://youtu.be/quietVid001", "en")
	if err != nil {
		t.Fatalf("Subtitles: %v", err)
	}
	if rel != "" {
		t.Fatalf("rel = %q, want empty", rel)
	}
}

// The language falls back rather than being sent empty: `--sub-langs` with no
// value would consume the URL as its argument.
func TestSubtitles_defaultsTheLanguage(t *testing.T) {
	mediaDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv")
	r := subsRunner(t, mediaDir, capture, "exit 0")

	if _, err := r.Subtitles(context.Background(), "plainVid001", "https://youtu.be/plainVid001", ""); err != nil {
		t.Fatalf("Subtitles: %v", err)
	}
	argv, _ := os.ReadFile(capture)
	if !strings.Contains(string(argv), "--sub-langs en") {
		t.Fatalf("argv = %q, want a defaulted --sub-langs en", string(argv))
	}
}

// A caption fetch is gated like every other call. It must never be the one path
// that reaches YouTube without a cookie.
func TestSubtitles_requiresACookie(t *testing.T) {
	r := New(RunnerConfig{
		Bin:            "/nonexistent",
		CookieProvider: func() (string, string) { return "", "absent" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       t.TempDir(),
	})
	if _, err := r.Subtitles(context.Background(), "plainVid001", "https://youtu.be/plainVid001", "en"); !errors.Is(err, ErrNoCookie) {
		t.Fatalf("err = %v, want ErrNoCookie", err)
	}
}

func TestSubtitles_rejectsAnEmptyVideoID(t *testing.T) {
	r := subsRunner(t, t.TempDir(), filepath.Join(t.TempDir(), "argv"), "exit 0")
	if _, err := r.Subtitles(context.Background(), "", "https://youtu.be/plainVid001", "en"); err == nil {
		t.Fatal("expected an error for an empty video id")
	}
}

// A failing binary surfaces as an error, and the directory is left alone: the
// next attempt reuses it, and removing it here would race a concurrent read of
// a caption an earlier attempt already fetched.
func TestSubtitles_reportsAFailingBinary(t *testing.T) {
	mediaDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv")
	r := subsRunner(t, mediaDir, capture, "echo 'boom' >&2; exit 1")

	if _, err := r.Subtitles(context.Background(), "plainVid001", "https://youtu.be/plainVid001", "en"); err == nil {
		t.Fatal("expected an error from a failing binary")
	}
	if _, err := os.Stat(SummaryDir(mediaDir, "plainVid001")); err != nil {
		t.Fatalf("summary dir should survive a failed attempt: %v", err)
	}
}

// foundSubtitle's own edges, without shelling out.
func TestFoundSubtitle(t *testing.T) {
	mediaDir := t.TempDir()
	dir := SummaryDir(mediaDir, "v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	rel, err := foundSubtitle(mediaDir, dir, "v1")
	if err != nil || rel != "" {
		t.Fatalf("empty dir gave (%q, %v), want an empty path and no error", rel, err)
	}

	// A file for a DIFFERENT video in the same directory must not match: the
	// glob is anchored on the id, and returning a neighbour's captions would
	// summarize the wrong video under this one's title.
	if err := os.WriteFile(filepath.Join(dir, "other.en.vtt"), []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rel, err := foundSubtitle(mediaDir, dir, "v1"); err != nil || rel != "" {
		t.Fatalf("got (%q, %v) for a neighbour's file, want an empty path and no error", rel, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "v1.en.vtt"), []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err = foundSubtitle(mediaDir, dir, "v1")
	if err != nil {
		t.Fatalf("foundSubtitle: %v", err)
	}
	if want := filepath.Join(SummaryDirName, "v1", "v1.en.vtt"); rel != want {
		t.Fatalf("rel = %q, want %q", rel, want)
	}
}
