package ytdlp

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"time"
)

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
	// ThrottleBase is the base duration for the pre-invocation throttle.
	// The actual sleep is a random value in [0.5, 1.5] * ThrottleBase. Zero
	// disables throttling (used in tests).
	ThrottleBase time.Duration
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
	return &Runner{cfg: cfg}
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

// throttle sleeps a random duration in [0.5, 1.5] * ThrottleBase via the
// injected Sleep function. A zero ThrottleBase disables throttling
// entirely (still calls Sleep(0) so callers can assert invocation count).
func (r *Runner) throttle() {
	factor := 0.5 + rand.Float64()
	d := time.Duration(float64(r.cfg.ThrottleBase) * factor)
	r.cfg.Sleep(d)
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
