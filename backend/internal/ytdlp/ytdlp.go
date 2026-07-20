package ytdlp

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"time"
)

// minThrottleFloor is the hard, non-negotiable minimum wait time between
// YouTube calls. This applies to everything that talks to YouTube through
// the Runner (Metadata today; Download and channel-scan later), because
// they all funnel through exec/throttle. No configuration value, however
// low (including zero), may push the effective floor below this.
const minThrottleFloor = 20 * time.Second

// defaultThrottleJitter is used when RunnerConfig.ThrottleJitter is left
// unset (zero). The floor alone must never be a bare fixed wait, so a
// random component is always added on top of the floor.
const defaultThrottleJitter = 15 * time.Second

// RunnerConfig configures a Runner. Every external dependency (the binary
// path, the cookie source, the sleep function) is injectable so tests
// never need the real yt-dlp binary and never actually sleep. A sidecar
// process could later implement the same Runner surface without changing
// callers.
type RunnerConfig struct {
	// Bin is the path to (or name of) the yt-dlp executable. It is used only
	// when BinResolver is nil (New wraps it in a constant resolver). Prefer
	// BinResolver for production so a self-updated binary is picked up.
	Bin string
	// BinResolver, when set, is called ONCE PER INVOCATION to resolve the
	// yt-dlp executable path, so a binary written to disk after boot (e.g. by
	// the 24h self-update) takes effect on the very next call without a
	// restart. When nil, New defaults it to a constant resolver returning Bin
	// (or "yt-dlp"). Injectable so tests can point it at a stub binary.
	BinResolver func() string
	// CookieProvider returns the current cookie text (Netscape format) and
	// its status string (e.g. "valid", "expired", "absent"). An empty text
	// means no cookie is configured.
	CookieProvider func() (text string, status string)
	// ThrottleFloor is the configured minimum wait between YouTube calls.
	// It maps to the settings.throttle_base_seconds column. It is always
	// clamped up to minThrottleFloor (20s) in New/effectiveFloor: a stored
	// value below 20s (including the historical default of 10s, or zero)
	// still yields waits of at least 20s. This is a firm product
	// invariant, not a tunable that can be lowered below 20s.
	ThrottleFloor time.Duration
	// ThrottleJitter is the size of the random window added on top of
	// ThrottleFloor: the actual wait is ThrottleFloor + rand[0, ThrottleJitter).
	// Zero (unset) defaults to defaultThrottleJitter (15s) so the wait is
	// never a bare fixed duration. Set a non-zero negative-free value
	// explicitly if a smaller jitter window is ever needed; there is no
	// way to disable jitter entirely short of passing a near-zero value.
	ThrottleJitter time.Duration
	// RandFloat64 returns a float64 in [0, 1) and drives the jitter
	// component. Injectable/seedable so tests can assert exact bounds and
	// observe variation without depending on math/rand's global state.
	// Defaults to math/rand/v2's auto-seeded Float64.
	RandFloat64 func() float64
	// Sleep is called with the computed throttle duration before every
	// binary invocation. It must respect ctx cancellation, returning
	// ctx.Err() if ctx is done before d elapses. Defaults to a production
	// sleeper that selects between a timer and ctx.Done(); tests inject a
	// no-op (still taking ctx so a cancellation test can exercise it).
	Sleep func(ctx context.Context, d time.Duration) error
	// MediaDir is the directory downloads are written into. Not used by
	// Metadata, but part of the shared config so download-related methods
	// added later don't need a second constructor.
	MediaDir string
	// PauseProvider reports the global youtube_paused kill-switch. When it
	// returns true, every call is refused with ErrPaused before the binary
	// runs and before the throttle sleep — the strongest enforcement point.
	PauseProvider func() (paused bool, reason string)
	// AllowAnonymous is a dev-only escape hatch (config.AllowAnonymousYoutube):
	// when true, cookieGate lets an EMPTY cookie through instead of failing
	// with ErrNoCookie, and exec omits --cookies entirely for that empty-text
	// run. It does NOT weaken the "expired"/"blocked" cookie-status branches —
	// those mean a real cookie exists and YouTube rejected it, a genuine
	// signal that must still fail. The throttle floor and pause gate are
	// completely unaffected. Callers must only ever set this from a config
	// value that was itself gated on BACKEND_AUTH_MODE=dev at boot
	// (config.Load); Runner does not re-derive or re-validate that here.
	AllowAnonymous bool
}

// Runner wraps the yt-dlp binary: cookie gate, throttle, and error
// classification for every invocation. Runner is the ONLY thing in peeq
// that shells out to yt-dlp.
type Runner struct {
	cfg RunnerConfig
}

// New builds a Runner from cfg, filling in safe defaults for any
// injectable dependency that was left unset.
func New(cfg RunnerConfig) *Runner {
	if cfg.BinResolver == nil {
		bin := cfg.Bin
		if bin == "" {
			bin = "yt-dlp"
		}
		cfg.BinResolver = func() string { return bin }
	}
	if cfg.Sleep == nil {
		cfg.Sleep = defaultSleep
	}
	if cfg.CookieProvider == nil {
		cfg.CookieProvider = func() (string, string) { return "", "absent" }
	}
	if cfg.PauseProvider == nil {
		cfg.PauseProvider = func() (bool, string) { return false, "" }
	}
	if cfg.ThrottleJitter == 0 {
		cfg.ThrottleJitter = defaultThrottleJitter
	}
	if cfg.RandFloat64 == nil {
		cfg.RandFloat64 = rand.Float64
	}
	return &Runner{cfg: cfg}
}

// effectiveThrottleFloor clamps the configured floor up to the hard 20s
// minimum. Nothing — not a low or zero settings value — may push the
// effective floor below minThrottleFloor.
func (r *Runner) effectiveThrottleFloor() time.Duration {
	if r.cfg.ThrottleFloor < minThrottleFloor {
		return minThrottleFloor
	}
	return r.cfg.ThrottleFloor
}

// cookieGate is the single choke point that enforces the cookie
// invariant: every run must first observe a non-empty, non-flagged cookie,
// or it must stop before the binary is ever invoked (and before the
// throttle sleep, so a known-bad cookie never burns a 20s+ wait).
//
// The "expired"/"blocked" branches always fail, even when AllowAnonymous is
// set: those statuses mean a real cookie exists and YouTube rejected it,
// which is a genuine signal, not an absence, so anonymous mode must not
// weaken them. Only the empty-cookie (absent) branch is relaxed, and only
// when AllowAnonymous is true — this is the dev-only escape hatch for the
// case where authenticated yt-dlp requests currently get no usable formats
// from YouTube while anonymous ones work.
func (r *Runner) cookieGate() (string, error) {
	text, status := r.cfg.CookieProvider()
	switch status {
	case "expired":
		return "", ErrCookieExpired
	case "blocked":
		return "", ErrBlocked
	}
	if text == "" {
		if r.cfg.AllowAnonymous {
			return "", nil
		}
		return "", ErrNoCookie
	}
	return text, nil
}

// pauseGate enforces the youtube_paused kill-switch. Like cookieGate, it stops
// before the binary and before the throttle sleep — a paused peeq makes zero
// yt-dlp calls.
func (r *Runner) pauseGate() error {
	if paused, _ := r.cfg.PauseProvider(); paused {
		return ErrPaused
	}
	return nil
}

// throttle sleeps floor + rand[0, jitter) via the injected Sleep function,
// where floor is the configured throttle floor clamped up to the hard 20s
// minimum (see effectiveThrottleFloor) and jitter is ThrottleJitter. This
// runs before EVERY yt-dlp invocation (exec is the single choke point), so
// it covers every call that touches YouTube: Metadata today, and any
// future Download / channel-scan calls that go through the same Runner.
// The wait is never a bare fixed duration — the random component is
// always added on top of the floor. The wait is cancellable: if ctx is
// cancelled before the wait elapses, throttle returns ctx.Err() without
// completing the full sleep, so a queued download can be cancelled during
// its pre-call wait instead of blocking until it ends.
func (r *Runner) throttle(ctx context.Context) error {
	floor := r.effectiveThrottleFloor()
	jitter := time.Duration(r.cfg.RandFloat64() * float64(r.cfg.ThrottleJitter))
	return r.cfg.Sleep(ctx, floor+jitter)
}

// defaultSleep is the production Sleep implementation. It waits d unless
// ctx is cancelled first, in which case it returns ctx.Err() immediately
// instead of blocking for the full duration.
func defaultSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// exec runs the yt-dlp binary with args, after writing cookieText to a
// restricted temp file passed via --cookies and throttling. It never
// receives a bare id or unparsed user input: callers must pass fully
// canonicalized URLs in args.
func (r *Runner) exec(ctx context.Context, cookieText string, args ...string) ([]byte, error) {
	return r.execWithProgress(ctx, cookieText, nil, args...)
}

// execWithProgress is exec's superset: it goes through the exact same
// cookie-temp-file and throttle choke point, but when onLine is non-nil
// it streams stdout line by line (for --newline progress parsing) instead
// of buffering it silently. Download uses this so it shares the identical
// cookie gate / throttle path as Metadata rather than a parallel one.
func (r *Runner) execWithProgress(ctx context.Context, cookieText string, onLine func(string), args ...string) ([]byte, error) {
	if paused, _ := r.cfg.PauseProvider(); paused {
		return nil, ErrPaused
	}

	// An empty cookieText only ever reaches here via the anonymous carve-out
	// in cookieGate (the non-anonymous path fails earlier with ErrNoCookie),
	// so no temp file is written and --cookies is omitted entirely — passing
	// --cookies pointed at an empty file is NOT equivalent to leaving the
	// flag off, so the flag must be genuinely absent for an anonymous run.
	var cookieFile string
	if cookieText != "" {
		f, err := writeCookieTempFile(cookieText)
		if err != nil {
			return nil, fmt.Errorf("ytdlp: write cookie temp file: %w", err)
		}
		cookieFile = f
		defer os.Remove(cookieFile)
	}

	// The throttle applies unconditionally, before AND after the cookie
	// branch above — anonymous calls carry MORE ban risk (no account to
	// rate-limit, just the host IP), so they must never skip or shorten it.
	if err := r.throttle(ctx); err != nil {
		return nil, err
	}

	fullArgs := args
	if cookieFile != "" {
		fullArgs = append([]string{"--cookies", cookieFile}, args...)
	}
	// Resolve the binary path fresh on every invocation (not once at boot),
	// so a self-updated yt-dlp written to disk after startup is used without
	// requiring a restart.
	cmd := exec.CommandContext(ctx, r.cfg.BinResolver(), fullArgs...)

	if onLine == nil {
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if runErr := cmd.Run(); runErr != nil {
			return nil, Classify(stderr.String(), runErr)
		}
		return stdout.Bytes(), nil
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ytdlp: stdout pipe: %w", err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ytdlp: start: %w", err)
	}

	scanner := bufio.NewScanner(stdoutPipe)
	// yt-dlp progress lines carry carriage returns and can be long; grow
	// the buffer past bufio's small default to avoid truncating them.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanner.Split(scanLinesCR)
	for scanner.Scan() {
		line := scanner.Text()
		stdout.WriteString(line)
		stdout.WriteByte('\n')
		onLine(line)
	}

	scanErr := scanner.Err()

	runErr := cmd.Wait()
	if runErr != nil {
		return nil, Classify(stderr.String(), runErr)
	}
	// cmd.Wait() succeeding doesn't mean the stdout scan actually saw
	// everything: a mid-stream read error (scanner.Err()) would otherwise
	// be silently swallowed, truncating output without any error being
	// reported. Surface it, but only once the command itself is confirmed
	// not to have failed (a real yt-dlp failure, classified above, always
	// takes precedence over a scan error).
	if scanErr != nil {
		return nil, fmt.Errorf("ytdlp: read stdout: %w", scanErr)
	}
	return stdout.Bytes(), nil
}

// scanLinesCR is a bufio.SplitFunc like bufio.ScanLines but also splits on
// bare '\r' (yt-dlp overwrites its progress line with '\r', not '\n').
func scanLinesCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	for i, b := range data {
		if b == '\n' || b == '\r' {
			return i + 1, trimCR(data[:i]), nil
		}
	}
	if atEOF {
		return len(data), trimCR(data), nil
	}
	return 0, nil, nil
}

func trimCR(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\r' {
		return b[:len(b)-1]
	}
	return b
}

// writeCookieTempFile writes text to a new 0600 temp file and returns its
// path. Callers MUST defer os.Remove(path) on the result.
func writeCookieTempFile(text string) (string, error) {
	f, err := os.CreateTemp("", "peeq-cookie-*.txt")
	if err != nil {
		return "", err
	}
	name := f.Name()

	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}
