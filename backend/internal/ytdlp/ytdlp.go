package ytdlp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/trick77/peeq/internal/logx"
	"github.com/trick77/peeq/internal/sched"
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

// playerClients is the --extractor-args value peeq forces on every
// cookie-bearing call.
//
// Left to itself, yt-dlp picks YouTube's tv_downgraded client for a logged-in
// session, and YouTube answers that client with an UNPLAYABLE player response —
// surfacing as "ERROR: [youtube] <id>: The page needs to be reloaded." on EVERY
// video (yt-dlp#17389). It costs captions, downloads and metadata alike, and it
// does so quietly: captionfetch spends its whole retry ladder on the error and
// settles the video as no_transcript, so an inbox card reads "Summarizing…" for
// 31 hours and then loses its marker with nothing said.
//
// Naming the client list explicitly is the yt-dlp maintainers' workaround.
// "default" keeps yt-dlp's own choices rather than replacing them, and
// web_embedded is the one that still answers with cookies attached.
//
// Scoped to the youtube extractor, so it does not disturb the youtubetab one
// channel.go passes: yt-dlp keys --extractor-args by extractor and merges
// repeated flags carrying different keys.
const playerClients = "youtube:player_client=default,web_embedded"

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
	// its status string — one of "absent", "valid", "stale", "blocked", the
	// only values settings.cookie_status permits. An empty text means no
	// cookie is configured.
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
	// Now is the clock the queue measures gaps against. Injectable so
	// a test can drive the queue deterministically instead of waiting real
	// seconds. Defaults to time.Now.
	Now func() time.Time
	// MediaDir is the directory downloads are written into. Not used by
	// Metadata, but part of the shared config so download-related methods
	// added later don't need a second constructor.
	MediaDir string
	// PauseProvider reports the global youtube_paused kill-switch. When it
	// returns true, every call is refused with ErrPaused before the binary
	// runs. It is consulted before the throttle sleep and again after it, so
	// a switch thrown while a call was queued still stops that call.
	PauseProvider func() (paused bool, reason string)
	// AllowAnonymous is a dev-only escape hatch (config.AllowAnonymousYoutube):
	// when true, cookieGate lets an EMPTY cookie through instead of failing
	// with ErrNoCookie, and exec omits --cookies entirely for that empty-text
	// run. It does NOT weaken the "stale"/"blocked" cookie-status branches —
	// those mean a real cookie exists and YouTube rejected it, a genuine
	// signal that must still fail. The throttle floor and pause gate are
	// completely unaffected. Callers must only ever set this from a config
	// value that was itself gated on BACKEND_AUTH_MODE=dev at boot
	// (config.Load); Runner does not re-derive or re-validate that here.
	AllowAnonymous bool
	// Logger receives yt-dlp's own narration during a download: every stdout
	// line that is NOT a progress update, at debug level. Those lines
	// ("[youtube] Extracting URL", "[info] Downloading 1 format(s)",
	// "[Merger] Merging formats into …", "[SponsorBlock] …") already arrived
	// and were silently discarded, which left what yt-dlp actually did
	// unobservable from outside the process — peeq could report a percentage
	// and, when something went wrong, whatever landed on stderr.
	//
	// Progress lines are deliberately NOT logged: they are already visible as
	// progress, and at one line per update they would bury the handful that say
	// which phase a download is in.
	//
	// It also receives every stderr line of every Runner call, download or not:
	// at debug on success, at warn on failure. (The package-level Version helper
	// is not a Runner call: it reports its own stderr in the error it returns
	// and never reaches this logger.) Classify keeps only a short tail for the
	// job's error text; the log gets all of it.
	//
	// Defaults to slog.Default(). Only the download path logs STDOUT — Metadata
	// returns JSON there, and logging that would dump a whole info blob per
	// call. Stderr never carries the JSON, so it is safe to log for everything.
	Logger *slog.Logger
}

// Runner wraps the yt-dlp binary: cookie gate, throttle, and error
// classification for every invocation. Runner is the ONLY thing in peeq
// that shells out to yt-dlp.
//
// One Runner is shared by every caller that touches YouTube — the download
// worker, the scan scheduler, the metadata refresher, and the HTTP handlers
// that resolve a channel or read a video's metadata on demand. That sharing is
// what makes the pacer below global rather than per-caller; a second Runner
// would silently double the call rate.
type Runner struct {
	cfg RunnerConfig

	// mu guards the queue below. It is never held across a sleep or an exec.
	mu sync.Mutex
	// busy is true while a caller holds the turn, until its yt-dlp process
	// exits. dispatching is true while the gap after that exit runs out and
	// nobody may start; the next caller is picked only when it has. See acquire.
	busy        bool
	dispatching bool
	// lastEnd is when the last yt-dlp process exited, and gap how long the next
	// one must wait after it (floor + jitter, drawn at that exit).
	lastEnd time.Time
	gap     time.Duration
	// interactive and background are the callers waiting for the turn, in
	// arrival order. A released turn goes to the head of interactive first.
	interactive []*waiter
	background  []*waiter
}

// waiter is one caller queued for the turn. ready is closed when the turn is
// handed to it; granted records that under mu, for a waiter that gives up at
// the same moment.
type waiter struct {
	ready   chan struct{}
	granted bool
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
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
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
// invariant: every run must observe a non-empty, non-flagged cookie, or it
// must stop before the binary is ever invoked. execWithProgress runs it both
// before the throttle sleep (so a known-bad cookie never burns a 20s+ wait)
// and after it (so a cookie that went bad during the wait is not used).
//
// The "stale"/"blocked" branches always fail, even when AllowAnonymous is
// set: those statuses mean a real cookie exists and YouTube rejected it,
// which is a genuine signal, not an absence, so anonymous mode must not
// weaken them. Only the empty-cookie (absent) branch is relaxed, and only
// when AllowAnonymous is true — this is the dev-only escape hatch for the
// case where authenticated yt-dlp requests currently get no usable formats
// from YouTube while anonymous ones work.
//
// The status strings here must match what settings actually persists —
// the schema's CHECK constraint permits only absent/valid/stale/blocked.
// This branch once read "expired", a value nothing ever writes, so a
// rejected cookie sailed through the gate and was handed to yt-dlp anyway.
func (r *Runner) cookieGate() (string, error) {
	text, status := r.cfg.CookieProvider()
	switch status {
	case "stale":
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

// pauseGate enforces the youtube_paused kill-switch. Like cookieGate, it runs
// before and after the throttle sleep and stops before the binary — a paused
// peeq makes zero yt-dlp calls, including calls that were already queued when
// the switch was thrown.
func (r *Runner) pauseGate() error {
	if paused, _ := r.cfg.PauseProvider(); paused {
		return ErrPaused
	}
	return nil
}

// gates runs the kill-switch gate and then the cookie gate and returns the
// cookie text the run may use. execWithProgress calls it twice: once before the
// queue wait, so a call already known to be refused never takes a turn or
// waits out a gap, and once after it, because the wait can last minutes
// on a busy Runner and the world moves meanwhile — a scan can flag the cookie
// stale or the operator can throw the kill-switch. Only the second answer is
// trusted: it is the cookie current when the process actually starts.
func (r *Runner) gates() (string, error) {
	if err := r.pauseGate(); err != nil {
		return "", err
	}
	return r.cookieGate()
}

// acquire waits for the turn to run one yt-dlp process and returns the func
// that gives it back. It runs before EVERY yt-dlp invocation
// (execWithProgress is the single choke point), so it covers downloads,
// channel scans, caption fetches, metadata refreshes and on-demand resolves
// alike.
//
// The turn makes YouTube calls strictly serial: one yt-dlp process at a time,
// and the next one starts no earlier than a gap after the previous one EXITED.
// The gap is floor + rand[0, jitter): floor is the configured throttle floor
// clamped up to the hard 20s minimum (see effectiveThrottleFloor), and the
// random component is always added on top, so the wait is never a bare fixed
// duration that a rate-limiter could recognise as a pattern.
//
// This replaced a pacer that only spaced STARTS: each call reserved a slot a
// gap after the previous call's start and never waited for it to finish, so a
// 15-minute download overlapped the scans, caption fetches and resolves queued
// behind it, and several yt-dlp processes talked to YouTube at once.
//
// The gap is TRAILING: it is owed after a call, never in front of one that has
// nothing to be spaced from, so on an idle Runner a caller goes at once.
//
// A person goes first. A call whose ctx carries WithInteractive is handed the
// turn ahead of every queued background call, but it still waits for the
// running process to exit and for the gap after it: a click never runs
// alongside another yt-dlp. A handler a person waits on can therefore wait as
// long as the download in front of it.
//
// The next caller is picked when the gap has run out, not when the previous
// process exits: a click arriving during the gap still goes before background
// work that was already queued.
//
// Waiting is cancellable. A caller whose ctx ends while queued leaves the
// queue (if it was handed the turn at that very moment, it hands it on), and
// acquire returns ctx.Err() with nothing owed.
//
// The returned release must be called (further calls do nothing). ran says
// whether a yt-dlp process actually started: a call refused after its wait
// made no YouTube request, so the next one is not spaced from it.
func (r *Runner) acquire(ctx context.Context) (release func(ran bool), err error) {
	r.mu.Lock()
	free := !r.busy && !r.dispatching && len(r.interactive) == 0 && len(r.background) == 0
	if free && !r.now().Before(r.lastEnd.Add(r.gap)) {
		// Idle and the gap is long past: go at once. Sleep still runs (for
		// nothing) so a cancelled ctx is honoured the same way on every path.
		r.busy = true
		r.mu.Unlock()
		if err := r.cfg.Sleep(ctx, 0); err != nil {
			r.handOff(false)
			return nil, err
		}
		return r.releaser(), nil
	}
	w := &waiter{ready: make(chan struct{})}
	if IsInteractive(ctx) {
		r.interactive = append(r.interactive, w)
	} else {
		r.background = append(r.background, w)
	}
	if !r.busy && !r.dispatching {
		r.startDispatch()
	}
	r.mu.Unlock()

	select {
	case <-w.ready:
		return r.releaser(), nil
	case <-ctx.Done():
		r.mu.Lock()
		if !w.granted {
			r.interactive = removeWaiter(r.interactive, w)
			r.background = removeWaiter(r.background, w)
			r.mu.Unlock()
			return nil, ctx.Err()
		}
		r.mu.Unlock()
		// Handed the turn as it gave up: pass it on.
		r.handOff(false)
		return nil, ctx.Err()
	}
}

func (r *Runner) releaser() func(ran bool) {
	var once sync.Once
	return func(ran bool) { once.Do(func() { r.handOff(ran) }) }
}

// handOff gives the turn back. When a process ran, its exit starts the next
// gap. If anyone is waiting, a dispatch picks the next caller once the gap has
// run out; otherwise the Runner is free.
func (r *Runner) handOff(ran bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ran {
		r.lastEnd = r.now()
		r.gap = r.drawGap()
	}
	r.busy = false
	if (len(r.interactive) > 0 || len(r.background) > 0) && !r.dispatching {
		r.startDispatch()
	}
}

// startDispatch sleeps out the gap in the background and then hands the turn
// to the longest-waiting interactive caller, else the longest-waiting
// background one. Called with mu held.
func (r *Runner) startDispatch() {
	r.dispatching = true
	due := r.lastEnd.Add(r.gap)
	go func() {
		wait := time.Duration(0)
		if now := r.now(); due.After(now) {
			wait = due.Sub(now)
		}
		// Not tied to any caller's ctx: a caller giving up only leaves the
		// queue; the gap still has to pass before anyone else starts.
		_ = r.cfg.Sleep(context.Background(), wait)

		r.mu.Lock()
		defer r.mu.Unlock()
		r.dispatching = false
		var next *waiter
		switch {
		case len(r.interactive) > 0:
			next, r.interactive = r.interactive[0], r.interactive[1:]
		case len(r.background) > 0:
			next, r.background = r.background[0], r.background[1:]
		default:
			return
		}
		r.busy = true
		next.granted = true
		close(next.ready)
	}()
}

func removeWaiter(list []*waiter, w *waiter) []*waiter {
	for i, x := range list {
		if x == w {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

// callLabelKey carries a short name for what a call is about — a video id —
// so a log line can be attributed to it. Package-private: it exists for the
// logger, not as a public API, and unlike WithInteractive it changes no
// behaviour.
type callLabelKey struct{}

// withCallLabel names the subject of a call for the logger, so a stderr line
// says which video it came from rather than leaving that to the timestamps.
func withCallLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, callLabelKey{}, label)
}

func callLabel(ctx context.Context) string {
	v, _ := ctx.Value(callLabelKey{}).(string)
	return v
}

// interactiveKey marks a context as belonging to a call a person is waiting on.
type interactiveKey struct{}

// WithInteractive marks ctx as user-facing, so the queue hands it the turn
// ahead of background work instead of behind it. It never runs alongside the
// yt-dlp already running: it waits for that one to exit and for the gap after
// it (see acquire). Use it for the handlers a person waits on with a spinner
// in front of them.
//
// Not for downloads, approved ones included: the download worker keeps every
// job on the background lane (download/process.go). A download holds the turn
// for its whole run, so on this lane a run of approved videos would starve
// every scan and caption fetch until it drained. A download's subtitle call
// does not queue at all: it runs under the turn the download holds.
func WithInteractive(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactiveKey{}, true)
}

// IsInteractive reports whether ctx was marked by WithInteractive. Exported so
// a caller that decides the lane — the download worker, which reads a job's
// priority — can assert it actually did, without reaching into the unexported
// context key or shelling out to a real yt-dlp to observe the pacing.
func IsInteractive(ctx context.Context) bool {
	v, _ := ctx.Value(interactiveKey{}).(bool)
	return v
}

// startKey carries a callback fired when a call stops queueing and the yt-dlp
// process is about to be launched.
type startKey struct{}

// WithStartHook marks ctx so fn is called at the moment a call leaves the pacer
// and the yt-dlp process is about to start — after the pause gate, the cookie
// gate and the throttle wait, before exec.
//
// It exists because "the call was entered" and "the process is running" are not
// the same instant, and callers that bound a call with an inactivity watchdog
// or a wall-clock cap mean the second one. The pacer's whole job is to make a
// call wait its turn, so a timer armed on entry counts that deliberate wait as
// though the process were hung: a deep enough queue in front of a job kills it
// before yt-dlp ever runs, and it surfaces as a failure when nothing was wrong.
// Arming on this hook makes such a timer mean "the process is running and has
// gone quiet", which is what those timers are for.
//
// fn runs on the goroutine making the call, synchronously, immediately before
// exec — so it must not block. It fires at most once per call, and NOT at all
// when the call never reaches exec: a pause gate, a missing cookie, or a
// context cancelled during the queue wait all return early. That is the
// point — there is no process to bound, and a user Cancel during the pre-call
// wait is already handled by the queue wait's own cancellation.
//
// A context carrying no hook is the normal case and costs a nil check.
func WithStartHook(ctx context.Context, fn func()) context.Context {
	return context.WithValue(ctx, startKey{}, fn)
}

// SignalStart fires ctx's start hook, if it carries one. Exported for the same
// reason IsInteractive is: a fake Runner standing in for this package in tests
// must be able to reproduce the signal without shelling out to a real yt-dlp,
// and the alternative is exposing the context key itself.
//
// Safe on a context with no hook, which is the normal case.
func SignalStart(ctx context.Context) {
	if fn, _ := ctx.Value(startKey{}).(func()); fn != nil {
		fn()
	}
}

// logStderr records what yt-dlp wrote to stderr on a call that SUCCEEDED.
//
// A failing call already surfaces stderr through Classify, which is how the
// job's error text and the Activity row get written. A call that exits 0 threw
// it away entirely — so a download that finished while warning about a
// throttled fragment retry, or a subtitle language it could not fetch, left no
// trace of having warned at all. That is the same blind spot the stdout logging
// closed, on the other stream.
//
// Line by line rather than one blob, so the log stays greppable and a long
// warning cannot swallow the entries around it. Blank lines are skipped.
func (r *Runner) logStderr(ctx context.Context, stderr string) {
	r.logStderrLines(ctx, slog.LevelDebug, "yt-dlp stderr", stderr)
}

// failed classifies a failed run and logs its stderr.
func (r *Runner) failed(ctx context.Context, stderr string, runErr error) error {
	err := Classify(stderr, runErr)
	r.logFailedStderr(ctx, stderr, runErr, err)
	return err
}

// logFailedStderr records EVERY line yt-dlp wrote to stderr on a call that
// failed. Classify keeps only a short tail for the job's error text, and a
// matched signature used to keep nothing: a download failed as the bare
// "ytdlp: retryable (rate limited or server error)", with no way to tell a
// 429 from a 503, or which request of the run it was.
//
// Warn, so the production level sees it. Routine failures go to debug: a
// missing channel tab (most channels have no /streams tab, so that fails on
// every scan by design), a TerminalError (the availability recheck probes
// walled-off videos and expects exactly that; its Detail already carries the
// ERROR line into the caller's error), and a run whose context was cancelled
// (shutdown, job cancel).
func (r *Runner) logFailedStderr(ctx context.Context, stderr string, runErr, classified error) {
	level := slog.LevelWarn
	var terminal *TerminalError
	if IsMissingTab(classified) || errors.As(classified, &terminal) || ctx.Err() != nil {
		level = slog.LevelDebug
	}
	var attrs []any
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		attrs = append(attrs, "exit_code", exitErr.ExitCode())
	}
	r.logStderrLines(ctx, level, "yt-dlp failed", stderr, attrs...)
}

// logStderrLines logs each non-blank stderr line under msg, attributed to the
// call's video and redacted: yt-dlp can echo a signed googlevideo URL.
func (r *Runner) logStderrLines(ctx context.Context, level slog.Level, msg, stderr string, attrs ...any) {
	if stderr == "" {
		return
	}
	// Ask before splitting. The success path runs on every call, Metadata
	// included, and Metadata runs on every channel scan — so with debug off
	// (the production default) the split and the per-line trim below would be
	// work whose every result is thrown away.
	if !r.cfg.Logger.Enabled(ctx, level) {
		return
	}
	if label := callLabel(ctx); label != "" {
		attrs = append([]any{"video_id", label}, attrs...)
	}
	for _, line := range strings.Split(stderr, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		r.cfg.Logger.Log(ctx, level, msg, append(attrs, "line", logx.RedactText(line))...)
	}
}

// now reads the injectable clock, defaulting to time.Now.
func (r *Runner) now() time.Time {
	if r.cfg.Now != nil {
		return r.cfg.Now()
	}
	return time.Now()
}

// defaultSleep is the production Sleep implementation. It waits d unless
// ctx is cancelled first, in which case it returns ctx.Err() immediately
// instead of blocking for the full duration.
func defaultSleep(ctx context.Context, d time.Duration) error {
	// A zero or negative wait is reachable: on an idle Runner acquire grants
	// the current instant. sched.Sleep checks ctx before arming a timer, so a
	// cancelled caller never proceeds on an already-due slot.
	if !sched.Sleep(ctx, d) {
		return ctx.Err()
	}
	return nil
}

// exec runs the yt-dlp binary with args, after the gates and the throttle
// (see execWithProgress). The cookie text it hands the binary is read from
// the CookieProvider once the throttle wait is over; when it is non-empty it
// is written to a restricted temp file passed via --cookies, and when it is
// empty (only reachable in dev via AllowAnonymous — see cookieGate) no temp
// file is written and --cookies is omitted entirely. It never receives a bare
// id or unparsed user input: callers must pass fully canonicalized URLs in
// args.
func (r *Runner) exec(ctx context.Context, args ...string) ([]byte, error) {
	return r.execWithProgress(ctx, nil, args...)
}

// execWithProgress is exec's superset: it goes through the exact same
// cookie-temp-file and queue choke point, but when onLine is non-nil
// it streams stdout line by line (for --newline progress parsing) instead
// of buffering it silently. Download uses this so it shares the identical
// cookie gate / queue path as Metadata rather than a parallel one.
func (r *Runner) execWithProgress(ctx context.Context, onLine func(string), args ...string) ([]byte, error) {
	// First pass: refuse early. A call that is paused or has no usable cookie
	// must not take a turn or wait out a gap just to be refused
	// afterwards. The text is discarded — see gates for why.
	if _, err := r.gates(); err != nil {
		return nil, &RefusedError{Err: err}
	}

	// The turn applies unconditionally — anonymous calls carry MORE ban risk
	// (no account to rate-limit, just the host IP), so they must never skip or
	// shorten it. It is held until this function returns, i.e. until the
	// process has exited, so no other yt-dlp can start meanwhile. A caller that
	// already holds the turn for several calls (Download: media, then
	// subtitles) passes it in ctx and keeps it across them.
	//
	// ran flips just before the process starts: anything returning earlier
	// made no YouTube request and owes no gap.
	ran := false
	held, _ := ctx.Value(heldTurnKey{}).(*heldTurn)
	if held == nil {
		release, err := r.acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer func() { release(ran) }()
	}
	markRan := func() {
		ran = true
		if held != nil {
			held.ran = true
		}
	}
	// Handed the turn at the moment the caller gave up: start nothing, so
	// nobody behind waits a gap for a process that never ran.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Second pass, after the wait: the answer that counts. This is the cookie
	// yt-dlp is handed, and this is where a cookie that went stale or a
	// kill-switch thrown while the call was queued stops it.
	cookieText, err := r.gates()
	if err != nil {
		return nil, &RefusedError{Err: err}
	}

	// An empty cookieText only ever reaches here via the anonymous carve-out
	// in cookieGate (the non-anonymous path fails above with ErrNoCookie),
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
		defer func() { _ = os.Remove(cookieFile) }()
	}

	// The queueing is over and the process is about to run: tell a caller that
	// asked to be told. Deliberately here rather than beside cmd.Start() below,
	// so both branches (buffered exec and streamed download) signal it — and
	// deliberately AFTER acquire, since everything this hook exists for is
	// about not counting the wait above as though the process were already
	// running. See WithStartHook.
	SignalStart(ctx)

	// Every call but a download gets a ceiling. This process holds the one
	// turn every YouTube call needs, so a hung one (a stuck JS-runtime child, a
	// stalled socket) would otherwise stall all of peeq's YouTube work until a
	// restart. A download (onLine set) is bounded by its caller's inactivity
	// watchdog instead, since a long one can legitimately run past any fixed
	// ceiling. Started here, after the wait, so queueing never counts.
	callerCtx := ctx
	if onLine == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, maxCallRuntime)
		defer cancel()
	}

	fullArgs := args
	if cookieFile != "" {
		// The player-client override rides with the cookie rather than being
		// unconditional: the client YouTube rejects is the one yt-dlp picks
		// BECAUSE the session is logged in, so an anonymous run (the
		// AllowAnonymous dev carve-out) is better left on yt-dlp's own defaults.
		fullArgs = append([]string{"--cookies", cookieFile, "--extractor-args", playerClients}, args...)
	}
	// Resolve the binary path fresh on every invocation (not once at boot),
	// so a self-updated yt-dlp written to disk after startup is used without
	// requiring a restart.
	cmd := exec.CommandContext(ctx, r.cfg.BinResolver(), fullArgs...) //nolint:gosec // argv, no shell. Every URL reaches here through Canonicalize, which url.Parse-es it, requires scheme and host, allowlists the youtube hosts and returns a rebuilt https://www.youtube.com/... literal, so a '-' prefixed string cannot become a flag

	// Bound how long Wait may sit on the output pipes once the process is gone
	// or the context has ended. yt-dlp starts ffmpeg and a JS runtime; a child
	// of its own that outlives it keeps the pipe open, and without this Wait
	// blocks on that pipe for as long as the stray lives.
	cmd.WaitDelay = waitDelay

	if onLine == nil {
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		// Set before Run: a start that fails costs a gap it did not need,
		// which is the conservative direction.
		markRan()
		if runErr := cmd.Run(); runErr != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) && callerCtx.Err() == nil {
				// The ceiling fired, not the caller: say so, since this is the
				// event the ceiling exists for and the stderr dump goes to debug.
				r.cfg.Logger.Warn("yt-dlp runtime ceiling hit",
					"video_id", callLabel(ctx), "after", maxCallRuntime)
			}
			return nil, r.failed(ctx, stderr.String(), runErr)
		}
		r.logStderr(ctx, stderr.String())
		return stdout.Bytes(), nil
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ytdlp: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Before Start, as on the buffered path: a failed start owes a gap too.
	markRan()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ytdlp: start: %w", err)
	}

	scanner := bufio.NewScanner(stdoutPipe)
	// yt-dlp progress lines carry carriage returns and can be long; grow
	// the buffer past bufio's small default to avoid truncating them.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	scanner.Split(scanLinesCR)
	// The lines go to onLine and nowhere else: the one caller of this branch
	// (Download) reads its result from the info.json, and a download's whole
	// progress log is not worth holding in memory to throw away.
	for scanner.Scan() {
		onLine(scanner.Text())
	}

	scanErr := scanner.Err()
	if scanErr != nil {
		// The scan stopped early (a line past the buffer). Keep reading, or the
		// process blocks on its next write and Wait never returns.
		_, _ = io.Copy(io.Discard, stdoutPipe)
	}

	runErr := cmd.Wait()
	if runErr != nil {
		return nil, r.failed(ctx, stderr.String(), runErr)
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
	// After the scan error check, not before: a truncated read is a failure of
	// this call, and logging its warnings as though it had merely succeeded
	// would put a reassuring entry under a call that is about to return an
	// error.
	r.logStderr(ctx, stderr.String())
	return nil, nil
}

// waitDelay is exec.Cmd.WaitDelay for every yt-dlp call.
const waitDelay = 10 * time.Second

// maxCallRuntime is the ceiling on a non-download yt-dlp call (scan listing,
// metadata, captions, channel resolve), which normally takes seconds. A var so
// a test can shorten it.
var maxCallRuntime = 10 * time.Minute

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
		_ = f.Close()
		_ = os.Remove(name)
		return "", err
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}
