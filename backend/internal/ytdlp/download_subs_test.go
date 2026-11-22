package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// captureRunner wraps the fake binary in a script that appends each call's
// argv as one line to the returned file, and logs into the returned buffer.
func captureRunner(t *testing.T, mediaDir string) (*Runner, string, *bytes.Buffer) {
	t.Helper()
	captureOut := filepath.Join(t.TempDir(), "capture.out")
	captureScript := filepath.Join(t.TempDir(), "capture.sh")
	content := "#!/bin/sh\n" +
		"echo \"$@\" >> '" + captureOut + "'\n" +
		"exec '" + fakeBinPath(t) + "' \"$@\"\n"
	if err := os.WriteFile(captureScript, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	return New(RunnerConfig{
		Bin:            captureScript,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
		Logger:         slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}), captureOut, &buf
}

func captureCalls(t *testing.T, captureOut string) []string {
	t.Helper()
	b, err := os.ReadFile(captureOut)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// TestDownload_requestsSubtitlesAndCapturesLanguage: captions come from their
// own call after the media, never from the media call. yt-dlp writes
// subtitles BEFORE the media and aborts the run on a subtitle error, so a
// refused caption request used to fail the whole download. The Result still
// exposes the subtitle path, audio language, and yt-dlp's own
// (non-SponsorBlock) chapters as JSON.
func TestDownload_requestsSubtitlesAndCapturesLanguage(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "subsVideo01"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_CHANNEL_ID", "UCsubs")
	r, captureOut, _ := captureRunner(t, mediaDir)

	res, err := r.Download(context.Background(), DownloadReq{
		URL:     "https://youtu.be/" + id,
		VideoID: id,
		Format:  "best-mp4",
		SubLang: "en",
	}, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	calls := captureCalls(t, captureOut)
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want media, caption list, caption fetch: %q", len(calls), calls)
	}
	media, list, fetch := calls[0]+" ", calls[1]+" ", calls[2]+" "
	for _, unwanted := range []string{"--write-subs", "--write-auto-subs", "--sub-langs"} {
		if strings.Contains(media, unwanted) {
			t.Fatalf("media call carries %q: %q", unwanted, media)
		}
	}
	// The same calls the inbox caption fetch makes: both go through
	// fetchCaptions, so the two .vtt files cannot differ.
	for _, want := range []string{"-J ", "--skip-download", "--write-subs", "--write-auto-subs", "--no-playlist"} {
		if !strings.Contains(list, want) {
			t.Fatalf("caption list call missing %q: %q", want, list)
		}
	}
	for _, want := range []string{"--load-info-json ", "--skip-download", "--write-auto-subs", "--sub-langs ^en-orig$ ", "--convert-subs vtt", "--sleep-subtitles "} {
		if !strings.Contains(fetch, want) {
			t.Fatalf("caption fetch missing %q: %q", want, fetch)
		}
	}
	// The fetch reads the list it was handed: no second extraction, so no URL.
	if strings.Contains(fetch, "youtube.com") || strings.Contains(fetch, "--write-subs") {
		t.Fatalf("caption fetch re-extracts or asks for both lists: %q", fetch)
	}
	for i, line := range calls[:2] {
		fields := strings.Fields(line)
		if fields[len(fields)-1] != "https://www.youtube.com/watch?v="+id {
			t.Fatalf("call %d: watchURL not last arg: %q", i, line)
		}
	}

	if !strings.HasSuffix(res.SubtitleRelPath, id+".en-orig.vtt") {
		t.Fatalf("SubtitleRelPath = %q, want the en-orig track", res.SubtitleRelPath)
	}
	// The track list it was picked from is gone, not moved into the library.
	if leftovers, _ := filepath.Glob(filepath.Join(mediaDir, "*", id, "*.json")); len(leftovers) != 0 {
		t.Fatalf("json left in the final dir: %v", leftovers)
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

// The three German videos that settled without a transcript: YouTube lists
// "en" for them only as a machine translation (tlang=en). The fetch asks for
// the German original, and audio_language says German, not the uploader's
// declared en-US.
func TestDownload_takesTheOriginalLanguageOverATranslation(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "germanVid01"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_SUBLANG", "en-US")
	t.Setenv("FAKE_YTDLP_CAPTIONS", `{"language":"en-US","subtitles":{},"automatic_captions":{`+
		`"de-orig":[{"url":"https://www.youtube.com/api/timedtext?lang=de&fmt=vtt"}],`+
		`"de":[{"url":"https://www.youtube.com/api/timedtext?lang=de&fmt=vtt"}],`+
		`"en":[{"url":"https://www.youtube.com/api/timedtext?lang=de&tlang=en&fmt=vtt"}]}}`)
	r, captureOut, _ := captureRunner(t, mediaDir)

	res, err := r.Download(context.Background(), DownloadReq{
		URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4", SubLang: "en",
	}, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	calls := captureCalls(t, captureOut)
	if fetch := calls[len(calls)-1] + " "; !strings.Contains(fetch, "--sub-langs ^de-orig$ ") {
		t.Fatalf("caption fetch = %q, want the de-orig track", fetch)
	}
	if !strings.HasSuffix(res.SubtitleRelPath, id+".de-orig.vtt") {
		t.Fatalf("SubtitleRelPath = %q, want the de-orig track", res.SubtitleRelPath)
	}
	if res.AudioLanguage != "de" {
		t.Fatalf("AudioLanguage = %q, want de (the track's, not the declared en-US)", res.AudioLanguage)
	}
}

// An uploaded track in another language is taken when nothing better exists:
// a download gets one attempt, there is no later rung to wait for. Only the
// uploaded list is asked for, so YouTube's ASR of the same language cannot
// stand in for it.
func TestDownload_takesALoneUploadedTrack(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "frenchVid01"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_CAPTIONS", `{"subtitles":{"fr":[{"url":"https://www.youtube.com/api/timedtext?lang=fr&fmt=vtt"}]},"automatic_captions":{}}`)
	r, captureOut, _ := captureRunner(t, mediaDir)

	res, err := r.Download(context.Background(), DownloadReq{
		URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4", SubLang: "en",
	}, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	calls := captureCalls(t, captureOut)
	fetch := calls[len(calls)-1] + " "
	if !strings.Contains(fetch, "--write-subs ") || strings.Contains(fetch, "--write-auto-subs") || !strings.Contains(fetch, "--sub-langs ^fr$ ") {
		t.Fatalf("caption fetch = %q, want only the uploaded fr track", fetch)
	}
	if res.AudioLanguage != "fr" {
		t.Fatalf("AudioLanguage = %q, want fr", res.AudioLanguage)
	}
}

// No track at all: the fetch is never made (it would only sleep two minutes
// to find nothing), and the media is kept with yt-dlp's declared language.
func TestDownload_noCaptionTrackSkipsTheFetch(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "noTracks001"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_CAPTIONS", `{"subtitles":{},"automatic_captions":{}}`)
	r, captureOut, _ := captureRunner(t, mediaDir)

	res, err := r.Download(context.Background(), DownloadReq{
		URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4", SubLang: "en",
	}, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if calls := captureCalls(t, captureOut); len(calls) != 2 {
		t.Fatalf("calls = %q, want media and caption list only", calls)
	}
	if res.SubtitleRelPath != "" {
		t.Fatalf("SubtitleRelPath = %q, want empty", res.SubtitleRelPath)
	}
	if res.AudioLanguage != "en" {
		t.Fatalf("AudioLanguage = %q, want yt-dlp's declared en", res.AudioLanguage)
	}
}

// A refused caption list costs the transcript, never the download.
func TestDownload_captionListFailureKeepsDownload(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "listFail001"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_CAPLIST_STDERR", "ERROR: [youtube] "+id+": HTTP Error 429: Too Many Requests")
	r, captureOut, logs := captureRunner(t, mediaDir)

	res, err := r.Download(context.Background(), DownloadReq{
		URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4", SubLang: "en",
	}, nil)
	if err != nil {
		t.Fatalf("Download: %v, want the media kept", err)
	}
	if res.SubtitleRelPath != "" {
		t.Fatalf("SubtitleRelPath = %q, want empty", res.SubtitleRelPath)
	}
	if calls := captureCalls(t, captureOut); len(calls) != 2 {
		t.Fatalf("calls = %q, want no fetch after a failed list", calls)
	}
	if !strings.Contains(logs.String(), "download subtitles failed, keeping media") {
		t.Fatalf("no warning logged:\n%s", logs.String())
	}
}

// Bot detection on the caption list fails the download like on the fetch.
func TestDownload_captionListBlockedFailsDownload(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "listBlock01"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_CAPLIST_STDERR", "ERROR: [youtube] "+id+": Sign in to confirm you're not a bot")
	r, _, _ := captureRunner(t, mediaDir)

	_, err := r.Download(context.Background(), DownloadReq{
		URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4", SubLang: "en",
	}, nil)
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
}

// A refused caption request costs the transcript, never the download.
func TestDownload_subtitleFailureKeepsDownload(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "subs429Vid1"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_SUBS_STDERR", "ERROR: Unable to download video subtitles for 'en': HTTP Error 429: Too Many Requests")
	r, _, logs := captureRunner(t, mediaDir)

	res, err := r.Download(context.Background(), DownloadReq{
		URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4", SubLang: "en",
	}, nil)
	if err != nil {
		t.Fatalf("Download: %v, want the media kept despite the caption failure", err)
	}
	if _, err := os.Stat(res.MediaPath); err != nil {
		t.Fatalf("media file missing: %v", err)
	}
	if res.SubtitleRelPath != "" {
		t.Fatalf("SubtitleRelPath = %q, want empty", res.SubtitleRelPath)
	}
	out := logs.String()
	if want := `level=WARN msg="download subtitles failed, keeping media" video_id=` + id; !strings.Contains(out, want) {
		t.Fatalf("log missing %q:\n%s", want, out)
	}
	if !strings.Contains(out, "HTTP Error 429") {
		t.Fatalf("log lacks the cause:\n%s", out)
	}
}

// Bot detection on the caption call still fails the download, so the worker
// pauses and flags the cookie instead of sending the next job out with it.
func TestDownload_subtitleBlockedFailsDownload(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "subsBlock01"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_SUBS_STDERR", "ERROR: [youtube] "+id+": Sign in to confirm you're not a bot")
	r, _, _ := captureRunner(t, mediaDir)

	_, err := r.Download(context.Background(), DownloadReq{
		URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4", SubLang: "en",
	}, nil)
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	if _, serr := os.Stat(filepath.Join(mediaDir, ".staging", id)); !os.IsNotExist(serr) {
		t.Fatalf("staging dir left behind: %v", serr)
	}
}

// A cancel while the caption call waits its turn must not finalize: the worker
// settles a cancel without looking at the result, so finalized media would
// sit on disk with no row pointing at it.
func TestDownload_cancelDuringSubtitlesDoesNotFinalize(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "subsCancel1"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_CHANNEL_ID", "UCcancel")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sleeps := 0
	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep: func(ctx context.Context, _ time.Duration) error {
			sleeps++
			if sleeps == 2 { // the caption call's pacer wait
				cancel()
				return ctx.Err()
			}
			return nil
		},
		MediaDir: mediaDir,
	})
	// Prime: the gap is trailing, so both Download calls then wait on Sleep.
	if err := r.paceOnce(context.Background()); err != nil {
		t.Fatalf("priming throttle: %v", err)
	}
	sleeps = 0

	_, err := r.Download(ctx, DownloadReq{
		URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4", SubLang: "en",
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, serr := os.Stat(filepath.Join(mediaDir, "UCcancel", id)); !os.IsNotExist(serr) {
		t.Fatalf("media finalized despite the cancel: %v", serr)
	}
	if _, serr := os.Stat(filepath.Join(mediaDir, ".staging", id)); !os.IsNotExist(serr) {
		t.Fatalf("staging dir left behind: %v", serr)
	}
}

// TestDownload_holdsTheTurnThroughSubtitles: nothing starts between a
// download's media call and its subtitle call. Queued separately, the subtitle
// call could wait behind other work past the download watchdog, which would
// throw the finished media away. The two are still spaced by one gap.
func TestDownload_holdsTheTurnThroughSubtitles(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "subsHold001"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_CHANNEL_ID", "UChold")
	var r *Runner
	other := make(chan struct{})
	var gaps []time.Duration
	r = New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		ThrottleFloor:  20 * time.Second,
		ThrottleJitter: time.Nanosecond,
		RandFloat64:    func() float64 { return 0 },
		Sleep: func(ctx context.Context, d time.Duration) error {
			// Only the gap taken inside the download (its ctx carries the
			// download's call label); the queue's own gap for the waiting caller
			// (after the release, on a bare ctx) is not one.
			if d == 0 || callLabel(ctx) != id {
				return nil
			}
			gaps = append(gaps, d)
			// The gap between media and subtitles: another caller arrives now
			// and must wait for the whole download, subtitles included.
			go func() {
				if rel, err := r.acquire(context.Background()); err == nil {
					rel(false)
				}
				close(other)
			}()
			waitQueued(t, r, 1)
			return nil
		},
		MediaDir: mediaDir,
	})

	if _, err := r.Download(context.Background(), DownloadReq{
		URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4", SubLang: "en",
	}, nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	select {
	case <-other:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn was never released after the download")
	}
	if len(gaps) != 1 || gaps[0] != 20*time.Second {
		t.Fatalf("gaps inside the download = %v, want one 20s gap before the subtitles", gaps)
	}
}

// TestDownload_cancelWhileQueuedRemovesStaging: a download cancelled while it
// waits for its turn ran nothing, and leaves no staging dir behind.
func TestDownload_cancelWhileQueuedRemovesStaging(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "queuedCanc1"
	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
	})
	release, err := r.acquire(context.Background()) // another call holds the turn
	if err != nil {
		t.Fatal(err)
	}
	defer release(false)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := r.Download(ctx, DownloadReq{URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4"}, nil)
		done <- err
	}()
	waitQueued(t, r, 1)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, serr := os.Stat(filepath.Join(mediaDir, ".staging", id)); !os.IsNotExist(serr) {
		t.Fatalf("staging dir left behind: %v", serr)
	}
}

// SkipSubtitles: one call, no caption flags, no .vtt.
func TestDownload_skipSubtitles(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "skipSubsV01"
	t.Setenv("FAKE_YTDLP_ID", id)
	r, captureOut, _ := captureRunner(t, mediaDir)

	res, err := r.Download(context.Background(), DownloadReq{
		URL: "https://youtu.be/" + id, VideoID: id, Format: "best-mp4", SubLang: "en", SkipSubtitles: true,
	}, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	calls := captureCalls(t, captureOut)
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want only the media call: %q", len(calls), calls)
	}
	if strings.Contains(calls[0], "--write-subs") || strings.Contains(calls[0], "--sub-langs") {
		t.Fatalf("media call asks for captions: %q", calls[0])
	}
	if res.SubtitleRelPath != "" {
		t.Fatalf("SubtitleRelPath = %q, want empty", res.SubtitleRelPath)
	}
	// The stored transcript's language stays: yt-dlp's declared one is the
	// uploader's setting, not the language of that transcript.
	if res.AudioLanguage != "" {
		t.Fatalf("AudioLanguage = %q, want empty so the stored one is kept", res.AudioLanguage)
	}
}
