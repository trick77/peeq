package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDownload_noCookie_doesNotCallBinary mirrors the Metadata cookie-gate
// invariant: Download must go through the exact same choke point, so an
// unconfigured cookie refuses before the binary (and the throttle) ever
// runs.
func TestDownload_noCookie_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "", "" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       t.TempDir(),
	})
	_, err := r.Download(context.Background(), DownloadReq{
		URL:     "https://youtu.be/dQw4w9WgXcQ",
		VideoID: "dQw4w9WgXcQ",
		Format:  "best-mp4",
	}, nil)
	if !errors.Is(err, ErrNoCookie) {
		t.Fatalf("want ErrNoCookie, got %v", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run without cookie")
	}
}

// TestDownload_happyPath drives the fake stub end to end and locks the
// core contract: progress callbacks fire with parsed percents, the final
// file lands under MediaDir/<channelID>/<id>/ (not .staging), the staging
// dir is gone, the required args were passed, and SponsorBlock segments
// are parsed from this download's own info.json.
func TestDownload_happyPath(t *testing.T) {
	mediaDir := t.TempDir()
	t.Setenv("FAKE_YTDLP_ID", "dQw4w9WgXcQ")
	t.Setenv("FAKE_YTDLP_CHANNEL_ID", "UCuAXFkgsw1L7xaCfnd5JJOw")

	var throttleCalls int
	var throttleDuration time.Duration
	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep: func(_ context.Context, d time.Duration) error {
			throttleCalls++
			throttleDuration = d
			return nil
		},
		MediaDir: mediaDir,
	})

	var progressed []Progress
	res, err := r.Download(context.Background(), DownloadReq{
		URL:       "https://youtu.be/dQw4w9WgXcQ",
		VideoID:   "dQw4w9WgXcQ",
		Format:    "best-mp4",
		LimitRate: "2M",
	}, func(p Progress) {
		progressed = append(progressed, p)
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	// Download must go through the same throttle as Metadata (CRITICAL
	// requirement: "the throttle must run"), waiting out at least the hard
	// 20s floor before the binary is invoked.
	if throttleCalls != 1 {
		t.Fatalf("throttle Sleep called %d times, want 1", throttleCalls)
	}
	if throttleDuration < minThrottleFloor {
		t.Fatalf("throttle duration %v below hard floor %v", throttleDuration, minThrottleFloor)
	}

	// (a) onProgress called with parsed percents.
	if len(progressed) != 2 {
		t.Fatalf("onProgress called %d times, want 2: %+v", len(progressed), progressed)
	}
	if progressed[0].Percent != 10.0 {
		t.Fatalf("progressed[0].Percent = %v, want 10.0", progressed[0].Percent)
	}
	if progressed[0].Speed == "" {
		t.Fatalf("progressed[0].Speed empty, want parsed speed")
	}
	if progressed[0].ETA == "" {
		t.Fatalf("progressed[0].ETA empty, want parsed ETA")
	}
	if progressed[1].Percent != 100.0 {
		t.Fatalf("progressed[1].Percent = %v, want 100.0", progressed[1].Percent)
	}

	// (b) final file under MediaDir/<channel>/<id>/, not .staging.
	finalDir := filepath.Join(mediaDir, "UCuAXFkgsw1L7xaCfnd5JJOw", "dQw4w9WgXcQ")
	if res.MediaPath != filepath.Join(finalDir, "dQw4w9WgXcQ.mp4") {
		t.Fatalf("MediaPath = %q, want file under %q", res.MediaPath, finalDir)
	}
	if _, e := os.Stat(res.MediaPath); e != nil {
		t.Fatalf("final media file missing: %v", e)
	}
	if strings.Contains(res.MediaPath, ".staging") {
		t.Fatalf("MediaPath %q must not reference .staging", res.MediaPath)
	}
	if res.ThumbnailPath == "" {
		t.Fatal("ThumbnailPath should be set (--write-thumbnail)")
	}
	if _, e := os.Stat(res.ThumbnailPath); e != nil {
		t.Fatalf("thumbnail file missing: %v", e)
	}
	if res.FilesizeBytes <= 0 {
		t.Fatalf("FilesizeBytes = %d, want > 0", res.FilesizeBytes)
	}
	if res.FormatUsed != Presets["best-mp4"] {
		t.Fatalf("FormatUsed = %q, want %q", res.FormatUsed, Presets["best-mp4"])
	}

	// (c) .staging/<id> cleaned up.
	if _, e := os.Stat(filepath.Join(mediaDir, ".staging", "dQw4w9WgXcQ")); !os.IsNotExist(e) {
		t.Fatalf("staging dir should be gone after atomic rename, stat err = %v", e)
	}

	// (e) SponsorBlock segments parsed from this download's info.json.
	if len(res.SponsorblockSegments) != 1 {
		t.Fatalf("SponsorblockSegments = %+v, want 1 segment", res.SponsorblockSegments)
	}
	seg := res.SponsorblockSegments[0]
	// The stored category is the API's own slug ("sponsor"), matching what the
	// backfill client stores for the same video — not yt-dlp's display title.
	if seg.Category != "sponsor" {
		t.Fatalf("segment category = %q, want %q", seg.Category, "sponsor")
	}
	if seg.StartTime != 10 || seg.EndTime != 25 {
		t.Fatalf("segment = %+v, want start=10 end=25", seg)
	}

	// (f) the release date comes off the same info.json, normalized to the
	// YYYY-MM-DD shape the videos table stores. Channel-driven downloads have
	// no other source for it — nothing calls Metadata on their behalf.
	if res.PublishedAt != "2024-01-15" {
		t.Fatalf("PublishedAt = %q, want %q", res.PublishedAt, "2024-01-15")
	}
}

// TestDownload_noUploadDate_leavesPublishedAtEmpty asserts a download whose
// info.json carries no upload_date (some live streams and premieres) yields
// an empty PublishedAt rather than a malformed date — the store treats empty
// as "leave whatever is already known".
func TestDownload_noUploadDate_leavesPublishedAtEmpty(t *testing.T) {
	mediaDir := t.TempDir()
	t.Setenv("FAKE_YTDLP_ID", "nodate12345")
	t.Setenv("FAKE_YTDLP_CHANNEL_ID", "UCnodate")
	t.Setenv("FAKE_YTDLP_UPLOAD_DATE", "")

	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
	})

	res, err := r.Download(context.Background(), DownloadReq{
		URL:     "https://youtu.be/nodate12345",
		VideoID: "nodate12345",
		Format:  "best-mp4",
	}, func(Progress) {})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.PublishedAt != "" {
		t.Fatalf("PublishedAt = %q, want empty", res.PublishedAt)
	}
}

// TestDownload_argsIncludeRequiredFlags captures the raw argv the fake
// binary was invoked with and checks every flag the brief calls out is
// present, including --cookies (proof Download shares the same gate as
// Metadata) and --limit-rate (only present because LimitRate was set).
func TestDownload_argsIncludeRequiredFlags(t *testing.T) {
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

	t.Setenv("FAKE_YTDLP_ID", "abcDEFghi12")
	t.Setenv("FAKE_YTDLP_CHANNEL_ID", "UCzzz")

	r := New(RunnerConfig{
		Bin:            captureScript,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
	})
	_, err := r.Download(context.Background(), DownloadReq{
		URL:       "https://youtu.be/abcDEFghi12",
		VideoID:   "abcDEFghi12",
		Format:    "apple-1080p",
		LimitRate: "5M",
	}, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	out, err := os.ReadFile(captureOut)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	argLine := string(out)

	stagingDir := filepath.Join(mediaDir, ".staging", "abcDEFghi12")
	for _, want := range []string{
		"--cookies",
		"--limit-rate 5M",
		"--merge-output-format mp4",
		"--sponsorblock-mark all",
		"--write-info-json",
		"--write-thumbnail",
		"--no-playlist",
		"--newline",
		"--socket-timeout 30",
		"--continue",
		"-f " + Presets["apple-1080p"],
		"-o " + filepath.Join(stagingDir, "%(id)s.%(ext)s"),
	} {
		if !strings.Contains(argLine, want) {
			t.Fatalf("args %q missing %q", argLine, want)
		}
	}

	// Regression guard: yt-dlp keeps its own headers, so --user-agent must
	// never be passed (a decided invariant, not an oversight).
	if strings.Contains(argLine, "--user-agent") {
		t.Fatalf("args %q must not contain --user-agent", argLine)
	}
}

// TestDownload_retryableFailure_leavesStagingForContinue: a transient
// (retryable) failure must NOT wipe the staging dir, so a later retry can
// pass --continue and resume from the partial file.
func TestDownload_retryableFailure_leavesStagingForContinue(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "retryVideo1" // 11-char valid-shaped id used consistently below
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_STDERR", "ERROR: unable to download video data: HTTP Error 429: Too Many Requests")
	t.Setenv("FAKE_YTDLP_EXIT", "1")

	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
	})
	_, err := r.Download(context.Background(), DownloadReq{
		URL:     "https://youtu.be/" + id,
		VideoID: id,
		Format:  "best-mp4",
	}, nil)

	var re *RetryableError
	if !errors.As(err, &re) {
		t.Fatalf("want *RetryableError, got %v", err)
	}
	if _, e := os.Stat(filepath.Join(mediaDir, ".staging", id)); e != nil {
		t.Fatalf("staging dir should survive a retryable failure for --continue: %v", e)
	}
}

// TestDownload_terminalFailure_cleansUpStaging: a terminal (permanent)
// failure must wipe the staging dir — there is nothing to resume.
func TestDownload_terminalFailure_cleansUpStaging(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "deletedVid1" // 11-char valid-shaped id used consistently below
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_STDERR", "ERROR: [youtube] "+id+": Video unavailable")
	t.Setenv("FAKE_YTDLP_EXIT", "1")

	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
	})
	_, err := r.Download(context.Background(), DownloadReq{
		URL:     "https://youtu.be/" + id,
		VideoID: id,
		Format:  "best-mp4",
	}, nil)

	var te *TerminalError
	if !errors.As(err, &te) {
		t.Fatalf("want *TerminalError, got %v", err)
	}
	if _, e := os.Stat(filepath.Join(mediaDir, ".staging", id)); !os.IsNotExist(e) {
		t.Fatalf("staging dir should be cleaned up on terminal failure, stat err = %v", e)
	}
}

// TestDownload_customFormat_reachesResolve locks in that DownloadReq.Format
// == "custom" actually reaches yt-dlp via CustomFormat, rather than always
// erroring (Resolve("custom", "") is always an error since custom must be
// non-empty).
func TestDownload_customFormat_reachesResolve(t *testing.T) {
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

	t.Setenv("FAKE_YTDLP_ID", "customFmt01")
	t.Setenv("FAKE_YTDLP_CHANNEL_ID", "UCcustom")

	r := New(RunnerConfig{
		Bin:            captureScript,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
	})
	res, err := r.Download(context.Background(), DownloadReq{
		URL:          "https://youtu.be/customFmt01",
		VideoID:      "customFmt01",
		Format:       "custom",
		CustomFormat: "bestvideo+bestaudio",
	}, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if res.FormatUsed != "bestvideo+bestaudio" {
		t.Fatalf("FormatUsed = %q, want %q", res.FormatUsed, "bestvideo+bestaudio")
	}

	out, err := os.ReadFile(captureOut)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if !strings.Contains(string(out), "-f bestvideo+bestaudio") {
		t.Fatalf("args %q missing %q", string(out), "-f bestvideo+bestaudio")
	}
}

// TestDownload_reDownload_overwritesExistingFinalDir: finalDir is unique to
// a given video id, so if it already exists (e.g. a prior download of the
// same video), a re-download must overwrite it rather than fail and lose
// the freshly downloaded file.
func TestDownload_reDownload_overwritesExistingFinalDir(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "redownload1"
	t.Setenv("FAKE_YTDLP_ID", id)
	t.Setenv("FAKE_YTDLP_CHANNEL_ID", "UCredownload")

	// Pre-create the final dir with an old file in it, simulating a prior
	// completed download of this same video.
	finalDir := filepath.Join(mediaDir, "UCredownload", id)
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finalDir, id+".mp4"), []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(finalDir, "stale-leftover.txt"), []byte("leftover"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
	})
	res, err := r.Download(context.Background(), DownloadReq{
		URL:     "https://youtu.be/" + id,
		VideoID: id,
		Format:  "best-mp4",
	}, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	content, err := os.ReadFile(res.MediaPath)
	if err != nil {
		t.Fatalf("read final media file: %v", err)
	}
	if string(content) != "dummy video content\n" {
		t.Fatalf("final file content = %q, want the fresh download's content, not the stale one", string(content))
	}
	if _, e := os.Stat(filepath.Join(finalDir, "stale-leftover.txt")); !os.IsNotExist(e) {
		t.Fatalf("stale leftover file should be gone after overwrite, stat err = %v", e)
	}
}

// TestParseProgressLine locks the --newline progress parsing against the
// exact line shapes yt-dlp emits.
func TestParseProgressLine(t *testing.T) {
	p, ok := parseProgressLine("[download]  10.0% of   50.00MiB at    2.00MiB/s ETA 01:23")
	if !ok {
		t.Fatal("expected a match")
	}
	if p.Percent != 10.0 {
		t.Fatalf("Percent = %v, want 10.0", p.Percent)
	}
	if p.Speed != "2.00MiB/s" {
		t.Fatalf("Speed = %q, want %q", p.Speed, "2.00MiB/s")
	}
	if p.ETA != "01:23" {
		t.Fatalf("ETA = %q, want %q", p.ETA, "01:23")
	}

	p2, ok2 := parseProgressLine("[download] 100% of 50.00MiB in 00:05")
	if !ok2 {
		t.Fatal("expected a match for the 100%% line")
	}
	if p2.Percent != 100.0 {
		t.Fatalf("Percent = %v, want 100.0", p2.Percent)
	}

	if _, ok3 := parseProgressLine("[youtube] Extracting URL"); ok3 {
		t.Fatal("non-progress line should not match")
	}
}

// TestSponsorblockSegmentsFromInfo_ignoresChapterTitles pins the bug this
// parser shipped with for months: it read SponsorBlock segments out of the
// info.json's "chapters" by their "[SponsorBlock]: " title prefix, a shape
// real yt-dlp never writes there (SponsorBlockPP writes
// "sponsorblock_chapters"; the prefixed titles are merged into the media
// file's own chapters afterwards). Every video therefore stored an empty
// segment list. A chapter title alone must never produce a segment.
func TestSponsorblockSegmentsFromInfo_ignoresChapterTitles(t *testing.T) {
	var info downloadInfoJSON
	raw := `{"chapters":[{"start_time":10,"end_time":25,"title":"[SponsorBlock]: Sponsor"}]}`
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if segs := sponsorblockSegmentsFromInfo(info); len(segs) != 0 {
		t.Fatalf("segments = %+v, want none from a chapter title", segs)
	}
}

// TestSponsorblockSegmentsFromInfo_filtersToCanonicalCategories: --sponsorblock-mark
// all returns categories peeq deliberately does not store, and they must be
// dropped here rather than reaching the player, so a downloaded video shows
// exactly the bands a backfilled one would.
func TestSponsorblockSegmentsFromInfo_filtersToCanonicalCategories(t *testing.T) {
	var info downloadInfoJSON
	raw := `{"sponsorblock_chapters":[
	  {"start_time":5,"end_time":9,"category":"chapter"},
	  {"start_time":30,"end_time":31,"category":"poi_highlight"},
	  {"start_time":40,"end_time":50,"category":"music_offtopic"},
	  {"start_time":60,"end_time":75,"category":"sponsor"}
	]}`
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	segs := sponsorblockSegmentsFromInfo(info)
	if len(segs) != 2 {
		t.Fatalf("segments = %+v, want the 2 canonical ones", segs)
	}
	if segs[0].Category != "music_offtopic" || segs[1].Category != "sponsor" {
		t.Fatalf("segments = %+v, want music_offtopic then sponsor", segs)
	}
	if segs[1].StartTime != 60 || segs[1].EndTime != 75 {
		t.Fatalf("sponsor segment = %+v, want start=60 end=75", segs[1])
	}
}
