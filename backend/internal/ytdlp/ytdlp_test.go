package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
// Sleep is called once per invocation, and the wait BETWEEN two calls is in
// [floor, floor+jitter), where floor is clamped to >= 20s. The gap is trailing,
// so the measured wait is the second call's — the first, on an idle Runner, has
// nothing to be spaced from and waits nothing.
func TestMetadata_throttle_sleepsWithinBounds(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
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
		Now:            func() time.Time { return now },
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
	if got != 0 {
		t.Fatalf("first call on an idle Runner slept %v, want 0", got)
	}
	if _, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ"); err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if calls != 2 {
		t.Fatalf("Sleep called %d times, want 2 (once per invocation)", calls)
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
			now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
			var got time.Duration
			r := New(RunnerConfig{
				ThrottleFloor:  tc.throttleFloor,
				ThrottleJitter: 0, // still must not push below 20s
				RandFloat64:    func() float64 { return 0 },
				Now:            func() time.Time { return now },
				Sleep:          func(_ context.Context, d time.Duration) error { got = d; return nil },
			})
			// The floor is the gap BETWEEN calls, so prime the Runner first: the
			// measured wait is the second call's.
			r.throttle(context.Background())
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
		now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
		var got time.Duration
		r := New(RunnerConfig{
			ThrottleFloor:  floor,
			ThrottleJitter: jitter,
			RandFloat64:    func() float64 { return f },
			Now:            func() time.Time { return now },
			Sleep:          func(_ context.Context, d time.Duration) error { got = d; return nil },
		})
		// Prime: the jitter rides the gap between calls, and the first call on
		// an idle Runner has no gap to ride.
		r.throttle(context.Background())
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
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	var got time.Duration
	r := New(RunnerConfig{
		ThrottleFloor: 20 * time.Second,
		RandFloat64:   func() float64 { return 0.5 },
		Now:           func() time.Time { return now },
		Sleep:         func(_ context.Context, d time.Duration) error { got = d; return nil },
	})
	// Prime, then measure the gap between calls.
	r.throttle(context.Background())
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

// TestCookieGate_anonymousAllowed_emptyCookieOK locks the dev-only anonymous
// escape hatch: with AllowAnonymous set, an empty (absent) cookie must NOT
// return ErrNoCookie — the run proceeds with an empty cookie text instead.
func TestCookieGate_anonymousAllowed_emptyCookieOK(t *testing.T) {
	r := New(RunnerConfig{
		AllowAnonymous: true,
		CookieProvider: func() (string, string) { return "", "absent" },
	})
	text, err := r.cookieGate()
	if err != nil {
		t.Fatalf("cookieGate() error = %v, want nil (anonymous allowed)", err)
	}
	if text != "" {
		t.Fatalf("cookieGate() text = %q, want empty", text)
	}
}

// TestCookieGate_notAnonymous_emptyCookieStillErrors locks the existing
// guarantee: without AllowAnonymous, an empty cookie still returns
// ErrNoCookie exactly as before.
func TestCookieGate_notAnonymous_emptyCookieStillErrors(t *testing.T) {
	r := New(RunnerConfig{
		CookieProvider: func() (string, string) { return "", "absent" },
	})
	_, err := r.cookieGate()
	if !errors.Is(err, ErrNoCookie) {
		t.Fatalf("cookieGate() error = %v, want ErrNoCookie", err)
	}
}

// TestCookieGate_anonymousAllowed_staleStillErrors and its blocked sibling
// below lock the carve-out: a stale/blocked cookie STATUS means a real
// cookie exists and YouTube rejected it — that is a genuine signal, not an
// absence, and anonymous mode must not weaken it.
//
// The status string MUST be one settings actually persists. This test
// previously passed "expired", which nothing writes and the schema's CHECK
// constraint forbids, so it went green against a branch production could
// never reach while real stale cookies were handed to yt-dlp anyway.
func TestCookieGate_anonymousAllowed_staleStillErrors(t *testing.T) {
	r := New(RunnerConfig{
		AllowAnonymous: true,
		CookieProvider: func() (string, string) { return "cookie-text", "stale" },
	})
	_, err := r.cookieGate()
	if !errors.Is(err, ErrCookieExpired) {
		t.Fatalf("cookieGate() error = %v, want ErrCookieExpired even in anonymous mode", err)
	}
}

// TestCookieGate_coversEveryPersistedStatus walks the complete set of
// cookie_status values the settings schema permits and asserts the gate
// makes a deliberate decision about each. It is the guard against the
// failure mode above: a status that no branch matches must not quietly
// fall through to "run yt-dlp with it".
func TestCookieGate_coversEveryPersistedStatus(t *testing.T) {
	// Mirrors the CHECK constraint on settings.cookie_status.
	cases := []struct {
		status  string
		wantErr error
	}{
		{"valid", nil},
		{"stale", ErrCookieExpired},
		{"blocked", ErrBlocked},
		{"absent", ErrNoCookie},
	}

	for _, c := range cases {
		t.Run(c.status, func(t *testing.T) {
			text := "cookie-text"
			if c.status == "absent" {
				text = ""
			}
			r := New(RunnerConfig{
				CookieProvider: func() (string, string) { return text, c.status },
			})

			_, err := r.cookieGate()

			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("cookieGate() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("cookieGate() error = %v, want %v", err, c.wantErr)
			}
		})
	}
}

func TestCookieGate_anonymousAllowed_blockedStillErrors(t *testing.T) {
	r := New(RunnerConfig{
		AllowAnonymous: true,
		CookieProvider: func() (string, string) { return "cookie-text", "blocked" },
	})
	_, err := r.cookieGate()
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("cookieGate() error = %v, want ErrBlocked even in anonymous mode", err)
	}
}

// TestMetadata_anonymous_emptyCookie_runsWithoutCookiesFlag proves the
// end-to-end anonymous path: with AllowAnonymous and no cookie configured,
// Metadata actually invokes the binary (rather than short-circuiting with
// ErrNoCookie), and the built argv contains NO --cookies flag at all —
// passing --cookies pointed at an empty file is not equivalent to omitting
// it, so the flag must be genuinely absent.
func TestMetadata_anonymous_emptyCookie_runsWithoutCookiesFlag(t *testing.T) {
	captureOut := filepath.Join(t.TempDir(), "capture.out")
	content := "#!/bin/sh\necho \"$@\" > '" + captureOut + "'\necho '{}'\nexit 0\n"
	script := filepath.Join(t.TempDir(), "capture.sh")
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	r := New(RunnerConfig{
		Bin:            script,
		AllowAnonymous: true,
		CookieProvider: func() (string, string) { return "", "absent" },
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
	if strings.Contains(argLine, "--cookies") {
		t.Fatalf("args %q must not contain --cookies when the cookie is empty in anonymous mode", argLine)
	}
}

// TestMetadata_withCookie_stillPassesCookiesFlag proves that even with
// AllowAnonymous set, a present cookie still takes the normal --cookies
// path (anonymous is an absence-only carve-out, not a global disable).
func TestMetadata_withCookie_stillPassesCookiesFlag(t *testing.T) {
	captureOut := filepath.Join(t.TempDir(), "capture.out")
	content := "#!/bin/sh\necho \"$@\" > '" + captureOut + "'\necho '{}'\nexit 0\n"
	script := filepath.Join(t.TempDir(), "capture.sh")
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	r := New(RunnerConfig{
		Bin:            script,
		AllowAnonymous: true,
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
	if !strings.Contains(string(out), "--cookies") {
		t.Fatalf("args %q must still contain --cookies when a cookie is present", string(out))
	}
}

// TestThrottle_appliesInAnonymousMode locks that the 20s throttle floor is
// unaffected by AllowAnonymous — anonymous calls carry MORE ban risk (no
// account to rate-limit, just the host IP), so the throttle must not be
// skipped or weakened for them.
func TestThrottle_appliesInAnonymousMode(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	var sleptFor time.Duration
	throttled := 0
	r := New(RunnerConfig{
		Bin:            fakeBinPath(t),
		AllowAnonymous: true,
		CookieProvider: func() (string, string) { return "", "absent" },
		Now:            func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) error {
			throttled++
			sleptFor = d
			return nil
		},
	})
	t.Setenv("FAKE_YTDLP_JSON", `{"id":"dQw4w9WgXcQ","title":"t"}`)
	// Two calls: anonymous mode must not skip or weaken the gap between them.
	for i := 0; i < 2; i++ {
		if _, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ"); err != nil {
			t.Fatalf("Metadata: %v", err)
		}
	}
	if throttled != 2 {
		t.Fatalf("throttle ran %d times, want 2 — it must not be skipped for anonymous calls", throttled)
	}
	if sleptFor < minThrottleFloor {
		t.Fatalf("anonymous throttle wait %v below hard floor %v", sleptFor, minThrottleFloor)
	}
}

// TestMetadata_cookieStatusStale_doesNotCallBinary locks the pre-exec
// short-circuit: a "stale" cookie status must stop the run (with
// ErrCookieExpired) before the binary is ever invoked and before the
// throttle sleep, saving a burned 20s+ wait on a cookie already known bad.
func TestMetadata_cookieStatusStale_doesNotCallBinary(t *testing.T) {
	called := filepath.Join(t.TempDir(), "called")
	r := New(RunnerConfig{
		Bin:            fakeBinTouching(called),
		CookieProvider: func() (string, string) { return "cookie-text", "stale" },
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
		t.Fatal("binary must not run when the cookie status is stale")
	}
}

// TestMetadata_cookieStatusBlocked_doesNotCallBinary mirrors
// TestMetadata_cookieStatusStale_doesNotCallBinary for the "blocked"
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

	t.Run("ChannelStreams", func(t *testing.T) {
		called := filepath.Join(t.TempDir(), "called")
		r := New(RunnerConfig{
			PauseProvider:  func() (bool, string) { return true, "paused" },
			Bin:            fakeBinTouching(called),
			CookieProvider: func() (string, string) { return "cookie-text", "valid" },
			Sleep:          fatalSleep(t),
		})
		_, err := r.ChannelStreams(context.Background(), "UCuAXFkgsw1L7xaCfnd5JJOw", 10)
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
		_, err := r.ResolveChannel(context.Background(), "https://www.youtube.com/@someone")
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

// TestThrottle_spacesOutConcurrentCallers is the shared-pacer invariant. Peeq
// has several things that talk to YouTube at once — the download worker, the
// scan scheduler, the metadata refresher, and HTTP handlers resolving a
// channel on demand. When throttle was a private per-call sleep, each of them
// waited its own 20s+ and they could then all fire in the same instant: peeq's
// own concurrency defeated its own throttle.
//
// Slots are reserved against a shared clock, so consecutive callers are spaced
// by at least one gap each no matter how many are asking.
func TestThrottle_spacesOutConcurrentCallers(t *testing.T) {
	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var waits []time.Duration
	r := New(RunnerConfig{
		CookieProvider: func() (string, string) { return "c", "valid" },
		ThrottleFloor:  20 * time.Second,
		ThrottleJitter: time.Nanosecond, // effectively no jitter: exact bounds
		RandFloat64:    func() float64 { return 0 },
		// A frozen clock is the harsh case: without a shared reservation every
		// caller would compute the same start time and they would all go at once.
		Now: func() time.Time { return base },
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			waits = append(waits, d)
			mu.Unlock()
			return nil
		},
	})

	const callers = 4
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			if err := r.throttle(context.Background()); err != nil {
				t.Errorf("throttle: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	got := append([]time.Duration(nil), waits...)
	mu.Unlock()
	if len(got) != callers {
		t.Fatalf("got %d waits, want %d", len(got), callers)
	}
	// Each caller's wait is measured from the same frozen "now". The gap is
	// trailing, so the first caller finds an idle Runner and goes at once; the
	// set of waits must be zero, one gap, two gaps, three gaps — in some order.
	// The spacing between consecutive starts is one gap either way, which is the
	// invariant this test exists for.
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	gap := 20*time.Second + time.Nanosecond*0
	for i, w := range got {
		want := time.Duration(i) * gap
		// Allow the sub-nanosecond jitter window to land anywhere in [0, 1ns).
		if w < want || w > want+time.Duration(i)*time.Nanosecond {
			t.Fatalf("wait[%d] = %v, want ~%v — callers were not spaced apart", i, w, want)
		}
	}
}

// TestThrottle_idleRunnerGoesImmediately: the gap is trailing, so it is
// enforced between calls and never in front of one that has nothing to be
// spaced from. A Runner that has not touched YouTube for an hour must let the
// next caller straight through — this is what stops a click after a quiet
// period sitting for 20-35s for no reason. It also proves the pacer is not a
// growing debt: the wait never accumulates across idle time.
func TestThrottle_idleRunnerGoesImmediately(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	var got time.Duration
	r := New(RunnerConfig{
		CookieProvider: func() (string, string) { return "c", "valid" },
		ThrottleFloor:  20 * time.Second,
		ThrottleJitter: time.Nanosecond,
		RandFloat64:    func() float64 { return 0 },
		Now:            func() time.Time { return now },
		Sleep:          func(_ context.Context, d time.Duration) error { got = d; return nil },
	})

	// First call on a cold Runner: nothing to be spaced from.
	if err := r.throttle(context.Background()); err != nil {
		t.Fatalf("throttle: %v", err)
	}
	if got != 0 {
		t.Fatalf("first call on an idle Runner waited %v, want 0", got)
	}
	// A second call at the same instant IS spaced — the gap still applies.
	if err := r.throttle(context.Background()); err != nil {
		t.Fatalf("throttle: %v", err)
	}
	if got < 20*time.Second || got > 21*time.Second {
		t.Fatalf("back-to-back wait = %v, want ~20s — the gap between calls must survive", got)
	}
	// Advance past every reserved slot: the Runner is idle again.
	now = now.Add(time.Hour)
	if err := r.throttle(context.Background()); err != nil {
		t.Fatalf("throttle: %v", err)
	}
	if got != 0 {
		t.Fatalf("wait after an hour of quiet = %v, want 0 (the gap is trailing, not leading)", got)
	}
}

// TestThrottle_interactiveSkipsTheBackgroundQueue is the reason WithInteractive
// exists. Three background workers queue up; then a person clicks. Before the
// priority lane the click inherited their queue and waited four gaps — on the
// request's own context, so a proxy timeout turned a merely-queued call into a
// visible failure. It must wait its own gap instead.
func TestThrottle_interactiveSkipsTheBackgroundQueue(t *testing.T) {
	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	var waits []time.Duration
	r := New(RunnerConfig{
		CookieProvider: func() (string, string) { return "c", "valid" },
		ThrottleFloor:  20 * time.Second,
		ThrottleJitter: time.Nanosecond,
		RandFloat64:    func() float64 { return 0 },
		Now:            func() time.Time { return base },
		Sleep: func(_ context.Context, d time.Duration) error {
			mu.Lock()
			waits = append(waits, d)
			mu.Unlock()
			return nil
		},
	})

	// Three background callers queue: 1, 2 and 3 gaps.
	for i := 0; i < 3; i++ {
		if err := r.throttle(context.Background()); err != nil {
			t.Fatalf("background throttle: %v", err)
		}
	}
	mu.Lock()
	backgroundWaits := len(waits)
	mu.Unlock()
	if backgroundWaits != 3 {
		t.Fatalf("expected 3 background waits, got %d", backgroundWaits)
	}

	if err := r.throttle(WithInteractive(context.Background())); err != nil {
		t.Fatalf("interactive throttle: %v", err)
	}

	mu.Lock()
	got := waits[len(waits)-1]
	mu.Unlock()
	// One gap — NOT the four it would inherit by queueing.
	if got < 20*time.Second || got > 21*time.Second {
		t.Fatalf("interactive wait = %v, want ~20s (its own gap, not the queue's)", got)
	}
}

// TestThrottle_interactiveStillWaitsItsOwnGap: skipping the queue must not mean
// skipping the throttle. On an idle Runner an interactive call goes at once —
// there is nothing to be spaced from — but it never lands on top of a call
// already admitted, and never alongside another interactive call.
func TestThrottle_interactiveStillWaitsItsOwnGap(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	var got time.Duration
	r := New(RunnerConfig{
		CookieProvider: func() (string, string) { return "c", "valid" },
		ThrottleFloor:  20 * time.Second,
		ThrottleJitter: time.Nanosecond,
		RandFloat64:    func() float64 { return 0 },
		Now:            func() time.Time { return now },
		Sleep:          func(_ context.Context, d time.Duration) error { got = d; return nil },
	})

	// A lone interactive call on an idle Runner goes straight through.
	if err := r.throttle(WithInteractive(context.Background())); err != nil {
		t.Fatalf("throttle: %v", err)
	}
	if got != 0 {
		t.Fatalf("first interactive wait on an idle Runner = %v, want 0", got)
	}
	// A second interactive call at the same instant is spaced from the first,
	// not granted alongside it.
	if err := r.throttle(WithInteractive(context.Background())); err != nil {
		t.Fatalf("throttle: %v", err)
	}
	if got < 20*time.Second {
		t.Fatalf("second interactive wait = %v, want >=20s (spaced from the first)", got)
	}
	// A third: now the priority lane's own tail is what binds, not now+gap.
	// Two clicks deep, now+gap would land on top of the second call — the
	// interactive tail is what keeps a burst of clicks spaced from each other.
	if err := r.throttle(WithInteractive(context.Background())); err != nil {
		t.Fatalf("throttle: %v", err)
	}
	if got < 40*time.Second {
		t.Fatalf("third interactive wait = %v, want >=40s (the priority lane keeps its own tail)", got)
	}
}

// TestThrottle_idleRunnerDoesNotSleepAtAll exercises the PRODUCTION sleeper on
// the newly reachable zero wait. Every other idle-Runner test injects a fake
// Sleep, so nothing would otherwise prove that defaultSleep returns straight
// away instead of arming a timer — the whole point of the change is that an
// idle click is not delayed, and a real timer with a non-positive duration is
// where that could quietly go wrong.
func TestThrottle_idleRunnerDoesNotSleepAtAll(t *testing.T) {
	r := New(RunnerConfig{
		CookieProvider: func() (string, string) { return "c", "valid" },
		ThrottleFloor:  minThrottleFloor,
		ThrottleJitter: 15 * time.Second,
		RandFloat64:    func() float64 { return 0.999999 },
		// Sleep left unset: the real defaultSleep runs.
	})

	start := time.Now()
	if err := r.throttle(context.Background()); err != nil {
		t.Fatalf("throttle: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("idle throttle took %v, want a prompt return rather than the ~20-35s gap", elapsed)
	}
}

// TestThrottle_interactiveNeverLandsOnAJustStartedBackgroundCall guards the
// subtle half of the trailing-gap change. nextInteractiveSlot tracks only the
// priority lane, so it says nothing about a background call that just began.
// If the interactive branch simply took max(now, nextInteractiveSlot), a click
// arriving the instant a background call started would fire on top of it — two
// yt-dlp processes hitting YouTube together, which is the exact failure the
// pacer exists to prevent. The busy branch must clear it by a full gap.
//
// "Never" is scoped to a call that has ALREADY STARTED. A background slot
// reserved for a time still in the future is a separate, pre-existing case the
// pacer deliberately does not push back (the burst-of-two trade-off documented
// on throttle), and this test says nothing about it.
func TestThrottle_interactiveNeverLandsOnAJustStartedBackgroundCall(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	var got time.Duration
	r := New(RunnerConfig{
		CookieProvider: func() (string, string) { return "c", "valid" },
		ThrottleFloor:  20 * time.Second,
		ThrottleJitter: time.Nanosecond,
		RandFloat64:    func() float64 { return 0 },
		Now:            func() time.Time { return now },
		Sleep:          func(_ context.Context, d time.Duration) error { got = d; return nil },
	})

	// A background call takes the current instant on the idle Runner.
	if err := r.throttle(context.Background()); err != nil {
		t.Fatalf("background throttle: %v", err)
	}
	if got != 0 {
		t.Fatalf("background wait on an idle Runner = %v, want 0", got)
	}
	// A click lands at that same instant. It skips the queue but must still
	// clear the call that just started by a full gap.
	if err := r.throttle(WithInteractive(context.Background())); err != nil {
		t.Fatalf("interactive throttle: %v", err)
	}
	if got < 20*time.Second {
		t.Fatalf("interactive wait = %v, want >=20s — it must not fire on top of the background call that just started", got)
	}
}

// TestThrottle_backgroundQueuesBehindAnInteractiveJump: an interactive call
// jumps a queue of background reservations, and work queued AFTERWARDS still
// lands a full gap past the slot the jumper took rather than piling onto it.
// (The jumper's own tail does not move nextSlot here — it is already further
// out than the jumper's tail; the spacing comes from the standing queue.)
func TestThrottle_backgroundQueuesBehindAnInteractiveJump(t *testing.T) {
	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	var got time.Duration
	r := New(RunnerConfig{
		CookieProvider: func() (string, string) { return "c", "valid" },
		ThrottleFloor:  20 * time.Second,
		ThrottleJitter: time.Nanosecond,
		RandFloat64:    func() float64 { return 0 },
		Now:            func() time.Time { return base },
		Sleep:          func(_ context.Context, d time.Duration) error { got = d; return nil },
	})

	// There must actually BE a queue for the interactive call to jump: on an
	// idle Runner it takes the current instant and the assertion below would
	// hold for a plain background call too, proving nothing about the jump.
	for i := 0; i < 3; i++ {
		if err := r.throttle(context.Background()); err != nil {
			t.Fatalf("priming background throttle: %v", err)
		}
	}

	if err := r.throttle(WithInteractive(context.Background())); err != nil {
		t.Fatalf("interactive throttle: %v", err)
	}
	jumper := got
	if jumper < 20*time.Second || jumper > 21*time.Second {
		t.Fatalf("interactive wait = %v, want ~20s (it jumped the 3-deep queue)", jumper)
	}

	if err := r.throttle(context.Background()); err != nil {
		t.Fatalf("background throttle: %v", err)
	}
	if got < jumper+20*time.Second {
		t.Fatalf("background wait after an interactive jump = %v, want >=%v — a full gap past the jumper's slot, not piled onto it",
			got, jumper+20*time.Second)
	}
}
