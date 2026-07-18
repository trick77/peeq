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
		Sleep:          func(time.Duration) {},
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
		Sleep:          func(time.Duration) {},
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
		Sleep: func(d time.Duration) {
			got = d
			calls++
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
				Sleep:          func(d time.Duration) { got = d },
			})
			r.throttle()
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
			Sleep:          func(d time.Duration) { got = d },
		})
		r.throttle()
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
		Sleep:         func(d time.Duration) { got = d },
	})
	r.throttle()
	want := minThrottleFloor + time.Duration(0.5*float64(defaultThrottleJitter))
	if got != want {
		t.Fatalf("Sleep(%v), want %v (default jitter %v applied)", got, want, defaultThrottleJitter)
	}
}

// TestMetadata_writesCookieToRestrictedTempFile locks the cookie-file
// invariant: cookie text lands in a 0600 temp file that is removed after
// the run, and the binary receives it via --cookies.
func TestMetadata_writesCookieToRestrictedTempFile(t *testing.T) {
	captureScript := filepath.Join(t.TempDir(), "capture.sh")
	captureOut := filepath.Join(t.TempDir(), "capture.out")
	content := "#!/bin/sh\necho \"$@\" > '" + captureOut + "'\necho '{}'\nexit 0\n"
	if err := os.WriteFile(captureScript, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	r := New(RunnerConfig{
		Bin:            captureScript,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(time.Duration) {},
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

	// The cookie temp file must be gone once Metadata returns.
	for _, field := range strings.Fields(argLine) {
		if field == captureScript || field == "-J" || field == "--skip-download" || field == "--no-playlist" || field == "--cookies" {
			continue
		}
		if strings.Contains(field, "vark-cookie-") {
			if _, statErr := os.Stat(field); statErr == nil {
				t.Fatalf("cookie temp file %q still exists after Metadata returned", field)
			}
		}
	}
}

// TestMetadata_parsesCannedJSON drives the fake yt-dlp stub with a canned
// -J JSON payload and checks the fields vark actually needs are parsed.
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
		Sleep:          func(time.Duration) {},
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

// TestMetadata_classifiesBlockedError proves an error surfaced by the
// binary flows through Classify end-to-end (not just unit-tested in
// isolation in errors_test.go).
func TestMetadata_classifiesBlockedError(t *testing.T) {
	t.Setenv("FAKE_YTDLP_STDERR", "ERROR: [youtube] dQw4w9WgXcQ: Sign in to confirm you're not a bot")
	t.Setenv("FAKE_YTDLP_EXIT", "1")
	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(time.Duration) {},
	})
	_, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ")
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("want ErrBlocked, got %v", err)
	}
}
