package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// flippingProvider is a cookie/pause source whose answer can be changed from
// inside the injected Sleep, which is exactly "the world changed while this
// call sat in the pacer queue".
type flippingProvider struct {
	mu     sync.Mutex
	text   string
	status string
	paused bool
}

func (p *flippingProvider) cookie() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.text, p.status
}

func (p *flippingProvider) pause() (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused, ""
}

func (p *flippingProvider) set(text, status string, paused bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.text, p.status, p.paused = text, status, paused
}

// TestExec_cookieFlippedStaleDuringThrottleWait_doesNotCallBinary: a call
// can wait minutes in the pacer queue. If the cookie is flagged stale while it
// waits (a scan just hit a bot block, say), the call must be refused when its
// slot arrives — not run on a cookie already known to be bad.
func TestExec_cookieFlippedStaleDuringThrottleWait_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	p := &flippingProvider{text: "cookie-text", status: "valid"}
	started := false
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: p.cookie,
		PauseProvider:  p.pause,
		Sleep: func(context.Context, time.Duration) error {
			p.set("cookie-text", "stale", false)
			return nil
		},
	})
	ctx := WithStartHook(context.Background(), func() { started = true })
	_, err := r.Metadata(ctx, "https://youtu.be/dQw4w9WgXcQ")
	if !errors.Is(err, ErrCookieExpired) {
		t.Fatalf("want ErrCookieExpired, got %v", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run on a cookie that went stale during the throttle wait")
	}
	if started {
		t.Fatal("start hook must not fire for a call the gate refused")
	}
}

// TestExec_pauseFlippedDuringThrottleWait_doesNotCallBinary is the
// kill-switch half of the same invariant.
func TestExec_pauseFlippedDuringThrottleWait_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	p := &flippingProvider{text: "cookie-text", status: "valid"}
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: p.cookie,
		PauseProvider:  p.pause,
		Sleep: func(context.Context, time.Duration) error {
			p.set("cookie-text", "valid", true)
			return nil
		},
	})
	_, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ")
	if !errors.Is(err, ErrPaused) {
		t.Fatalf("want ErrPaused, got %v", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run when the kill-switch was thrown during the throttle wait")
	}
}

// TestExec_cookieTextChangedDuringThrottleWait_binaryGetsTheNewText: the
// cookie handed to yt-dlp is the one current when the process starts, not the
// one current when the call was queued.
func TestExec_cookieTextChangedDuringThrottleWait_binaryGetsTheNewText(t *testing.T) {
	captureScript := filepath.Join(t.TempDir(), "capture.sh")
	captureOut := filepath.Join(t.TempDir(), "capture.out")
	content := "#!/bin/sh\n" +
		"prev=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--cookies\" ]; then cat \"$arg\" > '" + captureOut + "'; fi\n" +
		"  prev=\"$arg\"\n" +
		"done\n" +
		"echo '{}'\nexit 0\n"
	if err := os.WriteFile(captureScript, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &flippingProvider{text: "old-cookie", status: "valid"}
	r := New(RunnerConfig{
		Bin:            captureScript,
		CookieProvider: p.cookie,
		PauseProvider:  p.pause,
		Sleep: func(context.Context, time.Duration) error {
			p.set("new-cookie", "valid", false)
			return nil
		},
	})
	if _, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ"); err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	out, err := os.ReadFile(captureOut)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "new-cookie" {
		t.Fatalf("binary saw cookie %q, want the text current at start (new-cookie)", got)
	}
}

// TestDownload_pauseFlippedDuringThrottleWait_keepsStagingAndMakesNoCall
// covers the streamed branch: a job requeued after a rate-limited attempt
// still has its .part in staging, and if the kill-switch is thrown while it
// waits for its slot the refusal must neither run yt-dlp nor delete the
// .part, since the requeued job expects to --continue from it.
func TestDownload_pauseFlippedDuringThrottleWait_keepsStagingAndMakesNoCall(t *testing.T) {
	mediaDir := t.TempDir()
	const id = "resumeVid01"
	stagingDir := filepath.Join(mediaDir, ".staging", id)
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	part := filepath.Join(stagingDir, id+".mp4.part")
	if err := os.WriteFile(part, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := filepath.Join(t.TempDir(), "called")
	p := &flippingProvider{text: "cookie-text", status: "valid"}
	started := false
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: p.cookie,
		PauseProvider:  p.pause,
		Sleep: func(context.Context, time.Duration) error {
			p.set("cookie-text", "valid", true)
			return nil
		},
		MediaDir: mediaDir,
	})
	ctx := WithStartHook(context.Background(), func() { started = true })
	_, err := r.Download(ctx, DownloadReq{
		URL:     "https://youtu.be/" + id,
		VideoID: id,
		Format:  "best-mp4",
	}, nil)
	if !errors.Is(err, ErrPaused) {
		t.Fatalf("want ErrPaused, got %v", err)
	}
	if !IsRefused(err) {
		t.Fatalf("a gate refusal must be reported as *RefusedError, got %T", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run when the kill-switch was thrown during the throttle wait")
	}
	if started {
		t.Fatal("start hook must not fire for a refused download")
	}
	if _, e := os.Stat(part); e != nil {
		t.Fatalf("a refusal must leave the resumable .part in place: %v", e)
	}
}

// TestSubtitles_refusedCall_createsNoDirectory: a paused peeq is asked for
// captions once a minute per inbox candidate; a refusal must have no
// filesystem side effect.
func TestSubtitles_refusedCall_createsNoDirectory(t *testing.T) {
	mediaDir := t.TempDir()
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		PauseProvider:  func() (bool, string) { return true, "paused" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		MediaDir:       mediaDir,
	})
	_, err := r.Subtitles(context.Background(), "dQw4w9WgXcQ", "https://youtu.be/dQw4w9WgXcQ", "en")
	if !errors.Is(err, ErrPaused) {
		t.Fatalf("want ErrPaused, got %v", err)
	}
	if _, e := os.Stat(SummaryDir(mediaDir, "dQw4w9WgXcQ")); !os.IsNotExist(e) {
		t.Fatalf("refused Subtitles call must not create the summary dir (stat err = %v)", e)
	}
}
