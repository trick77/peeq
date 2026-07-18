package ytdlp

import (
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
	// Bin is the path to (or name of) the yt-dlp executable.
	Bin string
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
	// binary invocation. Defaults to time.Sleep; tests inject a no-op.
	Sleep func(time.Duration)
	// MediaDir is the directory downloads are written into. Not used by
	// Metadata, but part of the shared config so download-related methods
	// added later don't need a second constructor.
	MediaDir string
}

// Runner wraps the yt-dlp binary: cookie gate, throttle, and error
// classification for every invocation. Runner is the ONLY thing in vark
// that shells out to yt-dlp.
type Runner struct {
	cfg RunnerConfig
}

// New builds a Runner from cfg, filling in safe defaults for any
// injectable dependency that was left unset.
func New(cfg RunnerConfig) *Runner {
	if cfg.Bin == "" {
		cfg.Bin = "yt-dlp"
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	if cfg.CookieProvider == nil {
		cfg.CookieProvider = func() (string, string) { return "", "absent" }
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
// invariant: every run must first observe a non-empty cookie, or it must
// stop before the binary is ever invoked.
func (r *Runner) cookieGate() (string, error) {
	text, _ := r.cfg.CookieProvider()
	if text == "" {
		return "", ErrNoCookie
	}
	return text, nil
}

// throttle sleeps floor + rand[0, jitter) via the injected Sleep function,
// where floor is the configured throttle floor clamped up to the hard 20s
// minimum (see effectiveThrottleFloor) and jitter is ThrottleJitter. This
// runs before EVERY yt-dlp invocation (exec is the single choke point), so
// it covers every call that touches YouTube: Metadata today, and any
// future Download / channel-scan calls that go through the same Runner.
// The wait is never a bare fixed duration — the random component is
// always added on top of the floor.
func (r *Runner) throttle() {
	floor := r.effectiveThrottleFloor()
	jitter := time.Duration(r.cfg.RandFloat64() * float64(r.cfg.ThrottleJitter))
	r.cfg.Sleep(floor + jitter)
}

// exec runs the yt-dlp binary with args, after writing cookieText to a
// restricted temp file passed via --cookies and throttling. It never
// receives a bare id or unparsed user input: callers must pass fully
// canonicalized URLs in args.
func (r *Runner) exec(ctx context.Context, cookieText string, args ...string) ([]byte, error) {
	cookieFile, err := writeCookieTempFile(cookieText)
	if err != nil {
		return nil, fmt.Errorf("ytdlp: write cookie temp file: %w", err)
	}
	defer os.Remove(cookieFile)

	r.throttle()

	fullArgs := append([]string{"--cookies", cookieFile}, args...)
	cmd := exec.CommandContext(ctx, r.cfg.Bin, fullArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := cmd.Run(); runErr != nil {
		return nil, Classify(stderr.String(), runErr)
	}
	return stdout.Bytes(), nil
}

// writeCookieTempFile writes text to a new 0600 temp file and returns its
// path. Callers MUST defer os.Remove(path) on the result.
func writeCookieTempFile(text string) (string, error) {
	f, err := os.CreateTemp("", "vark-cookie-*.txt")
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
