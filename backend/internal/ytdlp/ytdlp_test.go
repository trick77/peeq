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

// fakeBinTouching writes a tiny throwaway shell script that touches marker
// when invoked and exits 0. Used to prove (or disprove) that the real
// binary was ever exec'd, without depending on the shared testdata stub.
func fakeBinTouching(marker string) string {
	script := marker + ".sh"
	content := "#!/bin/sh\ntouch '" + marker + "'\nexit 0\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		panic(err)
	}
	return script
}

func fakeBinPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("testdata/fake-ytdlp.sh")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	return p
}

// TestMetadata_noCookie_doesNotCallBinary is the cookie-gate invariant: no
// binary invocation may ever happen before a cookie is confirmed present.
func TestMetadata_noCookie_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "", "" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	_, err := r.Metadata(context.Background(), "https://youtu.be/abc")
	if !errors.Is(err, ErrNoCookie) {
		t.Fatalf("want ErrNoCookie, got %v", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run without cookie")
	}
}

func TestMetadata_withCookie_callsBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	_, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ")
	// The touch-only fake prints no JSON, so parsing may fail — that's fine
	// here, we only care that the binary ran.
	if err != nil {
		if _, e := os.Stat(called); e != nil {
			t.Fatalf("binary should have run even though parse failed: %v (stat err: %v)", err, e)
		}
	}
	if _, e := os.Stat(called); e != nil {
		t.Fatalf("binary must run once a cookie is present: %v", e)
	}
}

// TestMetadata_throttle_sleepsWithinBounds locks the throttle invariant:
// Sleep is called once per invocation with a duration in
// [floor, floor+jitter), where floor is clamped to >= 20s.
func TestMetadata_throttle_sleepsWithinBounds(t *testing.T) {
	var got time.Duration
	calls := 0
	floor := 30 * time.Second
	jitter := 5 * time.Second
	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		ThrottleFloor:  floor,
		ThrottleJitter: jitter,
		RandFloat64:    func() float64 { return 0.5 },
		Sleep: func(_ context.Context, d time.Duration) error {
			got = d
			calls++
			return nil
		},
	})
	t.Setenv("FAKE_YTDLP_JSON", `{"id":"dQw4w9WgXcQ","title":"t"}`)
	if _, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ"); err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if calls != 1 {
		t.Fatalf("Sleep called %d times, want 1", calls)
	}
	if got < floor || got >= floor+jitter {
		t.Fatalf("Sleep(%v) outside [%v, %v)", got, floor, floor+jitter)
	}
}

// TestThrottle_floorAlwaysAtLeast20Seconds locks the hard product
// invariant: the minimum wait between YouTube calls is 20 seconds,
// regardless of a low or zero configured floor.
func TestThrottle_floorAlwaysAtLeast20Seconds(t *testing.T) {
	cases := []struct {
		name          string
		throttleFloor time.Duration
	}{
		{"unset/zero", 0},
		{"below 20s (stored default 10s)", 10 * time.Second},
		{"5s", 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got time.Duration
			r := New(RunnerConfig{
				ThrottleFloor:  tc.throttleFloor,
				ThrottleJitter: 0, // still must not push below 20s
				RandFloat64:    func() float64 { return 0 },
				Sleep:          func(_ context.Context, d time.Duration) error { got = d; return nil },
			})
			r.throttle(context.Background())
			if got < minThrottleFloor {
				t.Fatalf("Sleep(%v) below hard floor %v (configured floor was %v)", got, minThrottleFloor, tc.throttleFloor)
			}
		})
	}
}

// TestThrottle_jitterAddsRandomComponent proves the wait is never a bare
// fixed floor: with jitter > 0, differing random draws produce differing
// waits, and each draw falls in [floor, floor+jitter).
func TestThrottle_jitterAddsRandomComponent(t *testing.T) {
	floor := minThrottleFloor
	jitter := 15 * time.Second

	draw := func(f float64) time.Duration {
		var got time.Duration
		r := New(RunnerConfig{
			ThrottleFloor:  floor,
			ThrottleJitter: jitter,
			RandFloat64:    func() float64 { return f },
			Sleep:          func(_ context.Context, d time.Duration) error { got = d; return nil },
		})
		r.throttle(context.Background())
		return got
	}

	low := draw(0)
	high := draw(0.999999)

	if low != floor {
		t.Fatalf("draw(0) = %v, want exactly floor %v", low, floor)
	}
	if high < floor || high >= floor+jitter {
		t.Fatalf("draw(~1) = %v outside [%v, %v)", high, floor, floor+jitter)
	}
	if low == high {
		t.Fatal("expected differing random draws to produce differing waits")
	}
}

// TestThrottle_defaultJitterAppliedWhenUnset locks the default: an unset
// ThrottleJitter must not degenerate into a bare fixed wait.
func TestThrottle_defaultJitterAppliedWhenUnset(t *testing.T) {
	var got time.Duration
	r := New(RunnerConfig{
		ThrottleFloor: 20 * time.Second,
		RandFloat64:   func() float64 { return 0.5 },
		Sleep:         func(_ context.Context, d time.Duration) error { got = d; return nil },
	})
	r.throttle(context.Background())
	want := minThrottleFloor + time.Duration(0.5*float64(defaultThrottleJitter))
	if got != want {
		t.Fatalf("Sleep(%v), want %v (default jitter %v applied)", got, want, defaultThrottleJitter)
	}
}

// TestThrottle_cancelledContextReturnsPromptly proves the production
// (default) sleeper is cancellable: throttling with an already-cancelled
// context must return ctx.Err() immediately, not block for the full
// floor+jitter wait. This is what lets the download worker cancel a queued
// download during its pre-call throttle wait.
func TestThrottle_cancelledContextReturnsPromptly(t *testing.T) {
	r := New(RunnerConfig{
		ThrottleFloor:  minThrottleFloor,
		ThrottleJitter: 15 * time.Second,
		RandFloat64:    func() float64 { return 0.999999 },
		// Sleep left unset: exercises the real defaultSleep production
		// sleeper, not a test no-op.
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := r.throttle(ctx)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("throttle(cancelled ctx) error = %v, want context.Canceled", err)
	}
	if elapsed > time.Second {
		t.Fatalf("throttle(cancelled ctx) took %v, want prompt return well under the ~20-35s floor+jitter wait", elapsed)
	}
}

// TestMetadata_cookieStatusExpired_doesNotCallBinary locks the pre-exec
// short-circuit: a "expired" cookie status must stop the run (with
// ErrCookieExpired) before the binary is ever invoked and before the
// throttle sleep, saving a burned 20s+ wait on a cookie already known bad.
func TestMetadata_cookieStatusExpired_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "cookie-text", "expired" },
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("throttle must not sleep when the cookie status is already known bad")
			return nil
		},
	})
	_, err := r.Metadata(context.Background(), "https://youtu.be/abc")
	if !errors.Is(err, ErrCookieExpired) {
		t.Fatalf("want ErrCookieExpired, got %v", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run when the cookie status is expired")
	}
}

// TestMetadata_cookieStatusBlocked_doesNotCallBinary mirrors
// TestMetadata_cookieStatusExpired_doesNotCallBinary for the "blocked"
// status.
func TestMetadata_cookieStatusBlocked_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "cookie-text", "blocked" },
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("throttle must not sleep when the cookie status is already known bad")
			return nil
		},
	})
	_, err := r.Metadata(context.Background(), "https://youtu.be/abc")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("want ErrBlocked, got %v", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run when the cookie status is blocked")
	}
}

// TestMetadata_writesCookieToRestrictedTempFile locks the cookie-file
// invariant: cookie text lands in a 0600 temp file that is removed after
// the run, and the binary receives it via --cookies. The stub also stats
// the cookie file's mode (while it still exists, mid-run) and reports it,
// so this test can assert the permission bits directly rather than only
// asserting the file's eventual absence.
func TestMetadata_writesCookieToRestrictedTempFile(t *testing.T) {
	captureScript := filepath.Join(t.TempDir(), "capture.sh")
	captureOut := filepath.Join(t.TempDir(), "capture.out")
	modeOut := filepath.Join(t.TempDir(), "mode.out")
	// stat's flag for "just the permission bits" differs between BSD/macOS
	// (-f '%Lp') and GNU/Linux (-c '%a'). Try the GNU form FIRST: on macOS
	// `stat -c` is an invalid flag and exits non-zero (falling back to -f),
	// whereas on GNU/Linux `stat -f` is a VALID flag meaning "filesystem info"
	// that succeeds with the wrong output — so trying -f first silently
	// captures garbage on Linux CI. GNU-first is the only correct order.
	content := "#!/bin/sh\n" +
		"echo \"$@\" > '" + captureOut + "'\n" +
		"cookiefile=\"\"\n" +
		"prev=\"\"\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$prev\" = \"--cookies\" ]; then cookiefile=\"$arg\"; fi\n" +
		"  prev=\"$arg\"\n" +
		"done\n" +
		"if [ -n \"$cookiefile\" ]; then\n" +
		"  (stat -c '%a' \"$cookiefile\" 2>/dev/null || stat -f '%Lp' \"$cookiefile\" 2>/dev/null) > '" + modeOut + "'\n" +
		"fi\n" +
		"echo '{}'\nexit 0\n"
	if err := os.WriteFile(captureScript, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	r := New(RunnerConfig{
		Bin:            captureScript,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	if _, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ"); err != nil {
		t.Fatalf("Metadata: %v", err)
	}

	out, err := os.ReadFile(captureOut)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	argLine := string(out)
	if !strings.Contains(argLine, "--cookies") {
		t.Fatalf("args %q missing --cookies", argLine)
	}

	// Assert the cookie mode was actually captured (i.e. --cookies had a
	// following path argument in argv), so the mode assertion below can't
	// pass vacuously on an empty/missing modeOut.
	modeBytes, err := os.ReadFile(modeOut)
	if err != nil {
		t.Fatalf("read mode capture (means --cookies path was never seen in argv): %v", err)
	}
	mode := strings.TrimSpace(string(modeBytes))
	if mode != "600" {
		t.Fatalf("cookie temp file mode = %q, want %q (0600)", mode, "600")
	}

	// The cookie temp file must be gone once Metadata returns.
	sawCookiePath := false
	for _, field := range strings.Fields(argLine) {
		if field == captureScript || field == "-J" || field == "--skip-download" || field == "--no-playlist" || field == "--cookies" {
			continue
		}
		if strings.Contains(field, "peeq-cookie-") {
			sawCookiePath = true
			if _, statErr := os.Stat(field); statErr == nil {
				t.Fatalf("cookie temp file %q still exists after Metadata returned", field)
			}
		}
	}
	if !sawCookiePath {
		t.Fatal("no peeq-cookie- path found in argv; cookie path assertion above would be vacuous")
	}
}

// TestMetadata_parsesCannedJSON drives the fake yt-dlp stub with a canned
// -J JSON payload and checks the fields peeq actually needs are parsed.
func TestMetadata_parsesCannedJSON(t *testing.T) {
	t.Setenv("FAKE_YTDLP_JSON", `{
		"id": "dQw4w9WgXcQ",
		"title": "Never Gonna Give You Up",
		"channel_id": "UCuAXFkgsw1L7xaCfnd5JJOw",
		"channel": "Rick Astley",
		"duration": 212.0,
		"thumbnail": "https://i.ytimg.com/vi/dQw4w9WgXcQ/maxresdefault.jpg",
		"upload_date": "20091025",
		"availability": "public"
	}`)
	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	meta, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ")
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.Title != "Never Gonna Give You Up" {
		t.Fatalf("Title = %q", meta.Title)
	}
	if meta.ChannelID != "UCuAXFkgsw1L7xaCfnd5JJOw" {
		t.Fatalf("ChannelID = %q", meta.ChannelID)
	}
	if meta.DurationSeconds != 212 {
		t.Fatalf("DurationSeconds = %d, want 212", meta.DurationSeconds)
	}
	if meta.Thumbnail == "" {
		t.Fatal("Thumbnail should be parsed")
	}
	if meta.PublishedAt != "2009-10-25" {
		t.Fatalf("PublishedAt = %q, want %q", meta.PublishedAt, "2009-10-25")
	}
	if meta.Availability != "public" {
		t.Fatalf("Availability = %q", meta.Availability)
	}
}

// TestPausedRunnerMakesNoCall locks the youtube_paused kill-switch
// invariant at the strongest enforcement point: when PauseProvider reports
// paused, Metadata must return ErrPaused before the cookie gate, the
// throttle sleep, or the binary are ever reached.
func TestPausedRunnerMakesNoCall(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		PauseProvider:  func() (bool, string) { return true, "paused" },
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep: func(context.Context, time.Duration) error {
			t.Fatal("throttle must not sleep while youtube_paused is set")
			return nil
		},
	})
	_, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ")
	if !errors.Is(err, ErrPaused) {
		t.Fatalf("err = %v, want ErrPaused", err)
	}
	if _, e := os.Stat(called); e == nil {
		t.Fatal("binary must not run while youtube_paused is set — a yt-dlp process would have spawned")
	}
}

// TestPausedRunnerMakesNoCall_allEntryPoints extends
// TestPausedRunnerMakesNoCall's invariant to the other three Runner methods
// that shell out to yt-dlp: ChannelVideos, ResolveChannel, and Download.
// Each must return ErrPaused (via errors.Is) and never spawn the binary,
// locking the zero-calls IP-protection invariant across every entry point,
// not just Metadata.
func TestPausedRunnerMakesNoCall_allEntryPoints(t *testing.T) {
	fatalSleep := func(t *testing.T) func(context.Context, time.Duration) error {
		return func(context.Context, time.Duration) error {
			t.Fatal("throttle must not sleep while youtube_paused is set")
			return nil
		}
	}

	t.Run("ChannelVideos", func(t *testing.T) {
		called := filepath.Join(t.TempDir(), "called")
		r := New(RunnerConfig{
			PauseProvider:  func() (bool, string) { return true, "paused" },
			Bin:            fakeBinTouching(called),
			CookieProvider: func() (string, string) { return "cookie-text", "valid" },
			Sleep:          fatalSleep(t),
		})
		_, err := r.ChannelVideos(context.Background(), "UCuAXFkgsw1L7xaCfnd5JJOw", 10)
		if !errors.Is(err, ErrPaused) {
			t.Fatalf("err = %v, want ErrPaused", err)
		}
		if _, e := os.Stat(called); e == nil {
			t.Fatal("binary must not run while youtube_paused is set — a yt-dlp process would have spawned")
		}
	})

	t.Run("ResolveChannel", func(t *testing.T) {
		called := filepath.Join(t.TempDir(), "called")
		r := New(RunnerConfig{
			PauseProvider:  func() (bool, string) { return true, "paused" },
			Bin:            fakeBinTouching(called),
			CookieProvider: func() (string, string) { return "cookie-text", "valid" },
			Sleep:          fatalSleep(t),
		})
		_, _, err := r.ResolveChannel(context.Background(), "https://www.youtube.com/@someone")
		if !errors.Is(err, ErrPaused) {
			t.Fatalf("err = %v, want ErrPaused", err)
		}
		if _, e := os.Stat(called); e == nil {
			t.Fatal("binary must not run while youtube_paused is set — a yt-dlp process would have spawned")
		}
	})

	t.Run("Download", func(t *testing.T) {
		called := filepath.Join(t.TempDir(), "called")
		r := New(RunnerConfig{
			PauseProvider:  func() (bool, string) { return true, "paused" },
			Bin:            fakeBinTouching(called),
			CookieProvider: func() (string, string) { return "cookie-text", "valid" },
			Sleep:          fatalSleep(t),
			MediaDir:       t.TempDir(),
		})
		_, err := r.Download(context.Background(), DownloadReq{
			URL:     "https://youtu.be/dQw4w9WgXcQ",
			VideoID: "dQw4w9WgXcQ",
			Format:  "best-mp4",
		}, nil)
		if !errors.Is(err, ErrPaused) {
			t.Fatalf("err = %v, want ErrPaused", err)
		}
		if _, e := os.Stat(called); e == nil {
			t.Fatal("binary must not run while youtube_paused is set — a yt-dlp process would have spawned")
		}
	})
}

// TestMetadata_classifiesBlockedError proves an error surfaced by the
// binary flows through Classify end-to-end (not just unit-tested in
// isolation in errors_test.go).
func TestMetadata_classifiesBlockedError(t *testing.T) {
	t.Setenv("FAKE_YTDLP_STDERR", "ERROR: [youtube] dQw4w9WgXcQ: Sign in to confirm you're not a bot")
	t.Setenv("FAKE_YTDLP_EXIT", "1")
	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})
	_, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("want ErrBlocked, got %v", err)
	}
}
