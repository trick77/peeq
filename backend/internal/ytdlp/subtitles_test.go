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

// englishASR is the track list of an English video: YouTube's ASR original.
const englishASR = `{"language":"en","subtitles":{},"automatic_captions":{` +
	`"en-orig":[{"url":"https://www.youtube.com/api/timedtext?lang=en&fmt=vtt"}],` +
	`"en":[{"url":"https://www.youtube.com/api/timedtext?lang=en&fmt=vtt"}]}}`

// subsRunner builds a Runner whose fake binary appends each call's argv as one
// line to captureOut, prints captions for the caption list call (-J), and runs
// body for every other call, so a test can assert both the flags sent and what
// landed on disk. body runs with $1.. as the real argv.
func subsRunner(t *testing.T, mediaDir, captureOut, captions, body string) *Runner {
	t.Helper()
	list := filepath.Join(t.TempDir(), "captions.json")
	if err := os.WriteFile(list, []byte(captions), 0o644); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "fake.sh")
	content := "#!/bin/sh\necho \"$@\" >> '" + captureOut + "'\n" +
		"case \" $* \" in *\" -J \"*) cat '" + list + "'; exit 0;; esac\n" +
		body + "\n"
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

// writeVTT is a fake body that writes the track a caption fetch asked for.
func writeVTT(dir, id, key string) string {
	return "mkdir -p '" + dir + "' && printf 'WEBVTT\\n' > '" + filepath.Join(dir, id+"."+key+".vtt") + "'"
}

// TestSubtitles_fetchesOnlyCaptions is the flag contract: a track list, then
// the one track picked from it. Download's caption step makes the same two
// calls (fetchCaptions), or the .vtt summarized from the Inbox and the one
// stored after downloading are different files. Nothing pulls media, a
// thumbnail or an info.json into the library: that is the whole point.
func TestSubtitles_fetchesOnlyCaptions(t *testing.T) {
	mediaDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv")
	const id = "capsVid0123"
	dir := SummaryDir(mediaDir, id)
	r := subsRunner(t, mediaDir, capture, englishASR, writeVTT(dir, id, "en-orig"))

	rel, lang, err := r.Subtitles(context.Background(), id, "https://youtu.be/"+id, "en", false)
	if err != nil {
		t.Fatalf("Subtitles: %v", err)
	}

	calls := captureCalls(t, capture)
	if len(calls) != 2 {
		t.Fatalf("calls = %q, want the list then the fetch", calls)
	}
	list, fetch := calls[0]+" ", calls[1]+" "
	for _, want := range []string{"-J ", "--skip-download", "--write-subs", "--write-auto-subs", "--no-playlist", "https://www.youtube.com/watch?v=" + id} {
		if !strings.Contains(list, want) {
			t.Fatalf("list call missing %q: %q", want, list)
		}
	}
	for _, want := range []string{"--load-info-json ", "--skip-download", "--write-auto-subs", "--sub-langs ^en-orig$ ", "--convert-subs vtt"} {
		if !strings.Contains(fetch, want) {
			t.Fatalf("fetch call missing %q: %q", want, fetch)
		}
	}
	for _, line := range calls {
		for _, unwanted := range []string{"--write-thumbnail", "--write-info-json", "--merge-output-format", " -f "} {
			if strings.Contains(" "+line+" ", unwanted) {
				t.Fatalf("unexpected arg %q in %q", unwanted, line)
			}
		}
	}

	if want := filepath.Join(SummaryDirName, id, id+".en-orig.vtt"); rel != want {
		t.Fatalf("rel = %q, want %q (MediaDir-relative)", rel, want)
	}
	if lang != "en" {
		t.Fatalf("lang = %q, want en", lang)
	}
	if _, err := os.Stat(filepath.Join(mediaDir, rel)); err != nil {
		t.Fatalf("returned path does not resolve under MediaDir: %v", err)
	}
	// The list it picked from is not left beside the caption.
	if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(leftovers) != 0 {
		t.Fatalf("track list left behind: %v", leftovers)
	}
}

// A German video: "en" is a translation and is never asked for.
func TestSubtitles_takesTheOriginalLanguage(t *testing.T) {
	mediaDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv")
	const id = "germanVid02"
	dir := SummaryDir(mediaDir, id)
	captions := `{"automatic_captions":{` +
		`"de-orig":[{"url":"https://www.youtube.com/api/timedtext?lang=de"}],` +
		`"en":[{"url":"https://www.youtube.com/api/timedtext?lang=de&tlang=en"}]}}`
	r := subsRunner(t, mediaDir, capture, captions, writeVTT(dir, id, "de-orig"))

	rel, lang, err := r.Subtitles(context.Background(), id, "https://youtu.be/"+id, "en", false)
	if err != nil {
		t.Fatalf("Subtitles: %v", err)
	}
	if lang != "de" || !strings.HasSuffix(rel, ".de-orig.vtt") {
		t.Fatalf("got (%q, %q), want the de-orig track", rel, lang)
	}
}

// TestSubtitles_noCaptionsIsNotAnError is the ordinary case for a fresh upload:
// YouTube's automatic captions run minutes to hours after publication, so the
// list has nothing yet. No fetch is made, and treating it as a failure would
// make the retry ladder read every fresh video as permanently caption-less.
func TestSubtitles_noCaptionsIsNotAnError(t *testing.T) {
	mediaDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv")
	r := subsRunner(t, mediaDir, capture, `{"subtitles":{},"automatic_captions":{}}`, "exit 0")

	rel, lang, err := r.Subtitles(context.Background(), "quietVid001", "https://youtu.be/quietVid001", "en", true)
	if err != nil {
		t.Fatalf("Subtitles: %v", err)
	}
	if rel != "" || lang != "" {
		t.Fatalf("got (%q, %q), want empty", rel, lang)
	}
	if calls := captureCalls(t, capture); len(calls) != 1 {
		t.Fatalf("calls = %q, want the list only", calls)
	}
}

// A lone uploaded track in another language waits for the last attempt, which
// passes anyManual: until then the speech's own ASR may still arrive.
func TestSubtitles_loneForeignUploadWaitsForTheLastAttempt(t *testing.T) {
	const id = "frenchVid02"
	captions := `{"subtitles":{"fr":[{"url":"https://www.youtube.com/api/timedtext?lang=fr"}]},"automatic_captions":{}}`

	mediaDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv")
	dir := SummaryDir(mediaDir, id)
	r := subsRunner(t, mediaDir, capture, captions, writeVTT(dir, id, "fr"))
	if rel, _, err := r.Subtitles(context.Background(), id, "https://youtu.be/"+id, "en", false); err != nil || rel != "" {
		t.Fatalf("early attempt: (%q, %v), want nothing yet", rel, err)
	}

	rel, lang, err := r.Subtitles(context.Background(), id, "https://youtu.be/"+id, "en", true)
	if err != nil {
		t.Fatalf("last attempt: %v", err)
	}
	if lang != "fr" || !strings.HasSuffix(rel, ".fr.vtt") {
		t.Fatalf("last attempt: (%q, %q), want the fr track", rel, lang)
	}
	calls := captureCalls(t, capture)
	if fetch := calls[len(calls)-1] + " "; !strings.Contains(fetch, "--write-subs ") || strings.Contains(fetch, "--write-auto-subs") {
		t.Fatalf("fetch = %q, want only the uploaded list", fetch)
	}
}

// List and fetch share one turn: queued separately, the fetch could wait
// behind other work while the URLs in the list go stale, and another call
// would land between a player request and its caption download. With the turn
// primed, only the one gap in front of the list is waited.
func TestSubtitles_listAndFetchShareOneTurn(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "oneTurnVid1"
	dir := SummaryDir(mediaDir, id)
	r := subsRunner(t, mediaDir, filepath.Join(t.TempDir(), "argv"), englishASR, writeVTT(dir, id, "en-orig"))
	var gaps int
	r.cfg.ThrottleFloor = 20 * time.Second
	r.cfg.RandFloat64 = func() float64 { return 0 }
	r.cfg.Sleep = func(_ context.Context, d time.Duration) error {
		if d > 0 {
			gaps++
		}
		return nil
	}
	if err := r.paceOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	gaps = 0

	if _, _, err := r.Subtitles(context.Background(), id, "https://youtu.be/"+id, "en", false); err != nil {
		t.Fatalf("Subtitles: %v", err)
	}
	if gaps != 1 {
		t.Fatalf("gaps = %d, want 1: the fetch must not queue again", gaps)
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
	if _, _, err := r.Subtitles(context.Background(), "plainVid001", "https://youtu.be/plainVid001", "en", false); !errors.Is(err, ErrNoCookie) {
		t.Fatalf("err = %v, want ErrNoCookie", err)
	}
}

func TestSubtitles_rejectsAnEmptyVideoID(t *testing.T) {
	r := subsRunner(t, t.TempDir(), filepath.Join(t.TempDir(), "argv"), englishASR, "exit 0")
	if _, _, err := r.Subtitles(context.Background(), "", "https://youtu.be/plainVid001", "en", false); err == nil {
		t.Fatal("expected an error for an empty video id")
	}
}

// A failing fetch surfaces as an error, and the directory is left alone: the
// next attempt reuses it, and removing it here would race a concurrent read of
// a caption an earlier attempt already fetched.
func TestSubtitles_reportsAFailingBinary(t *testing.T) {
	mediaDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv")
	r := subsRunner(t, mediaDir, capture, englishASR, "echo 'boom' >&2; exit 1")

	if _, _, err := r.Subtitles(context.Background(), "plainVid001", "https://youtu.be/plainVid001", "en", false); err == nil {
		t.Fatal("expected an error from a failing binary")
	}
	if _, err := os.Stat(SummaryDir(mediaDir, "plainVid001")); err != nil {
		t.Fatalf("summary dir should survive a failed attempt: %v", err)
	}
}

// A list yt-dlp printed but peeq cannot read is a failure, not "no captions":
// the latter would settle the video for good on the last attempt.
func TestSubtitles_unreadableListIsAnError(t *testing.T) {
	r := subsRunner(t, t.TempDir(), filepath.Join(t.TempDir(), "argv"), "not json", "exit 0")
	if _, _, err := r.Subtitles(context.Background(), "plainVid001", "https://youtu.be/plainVid001", "en", true); err == nil {
		t.Fatal("expected an error for an unreadable track list")
	}
}

// trackFile's own edges, without shelling out.
func TestTrackFile(t *testing.T) {
	mediaDir := t.TempDir()
	dir := SummaryDir(mediaDir, "v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	rel, err := trackFile(mediaDir, dir, "v1", "en")
	if err != nil || rel != "" {
		t.Fatalf("empty dir gave (%q, %v), want an empty path and no error", rel, err)
	}

	// Neither another video's file nor this video's file under another key
	// (left by an earlier attempt) is this track: returned, it would be stored
	// under this track's language, or as the wrong video.
	for _, name := range []string{"other.en.vtt", "v1.de-orig.vtt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("WEBVTT\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if rel, err := trackFile(mediaDir, dir, "v1", "en"); err != nil || rel != "" {
		t.Fatalf("got (%q, %v) for files of other tracks, want an empty path and no error", rel, err)
	}

	if err := os.WriteFile(filepath.Join(dir, "v1.en.vtt"), []byte("WEBVTT\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err = trackFile(mediaDir, dir, "v1", "en")
	if err != nil {
		t.Fatalf("trackFile: %v", err)
	}
	if want := filepath.Join(SummaryDirName, "v1", "v1.en.vtt"); rel != want {
		t.Fatalf("rel = %q, want %q", rel, want)
	}
}

// The caption download waits 120-180s after the player request (yt-dlp#13831:
// a caption fetched right after the player answers 429). The player request is
// now in the list call, so the wait rides on the fetch. The window's ends are
// pinned through RandFloat64.
func TestSubtitles_sleepsBeforeTheCaptionDownload(t *testing.T) {
	for _, tc := range []struct {
		rnd  float64
		want string
	}{
		{0, "--sleep-subtitles 120 "},
		{0.999, "--sleep-subtitles 179 "},
	} {
		mediaDir := t.TempDir()
		capture := filepath.Join(t.TempDir(), "argv")
		r := subsRunner(t, mediaDir, capture, englishASR, "true")
		r.cfg.RandFloat64 = func() float64 { return tc.rnd }

		if _, _, err := r.Subtitles(context.Background(), "sleepVid012", "https://youtu.be/sleepVid012", "en", false); err != nil {
			t.Fatalf("Subtitles: %v", err)
		}
		calls := captureCalls(t, capture)
		if fetch := calls[len(calls)-1] + " "; !strings.Contains(fetch, tc.want) {
			t.Fatalf("rand %v: fetch = %q, want %q", tc.rnd, fetch, tc.want)
		}
	}
}
