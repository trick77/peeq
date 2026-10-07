package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// paceOnce takes the turn and gives it back as a call that ran and exited at
// once: the shape of the old start-spacing tests, which only cared about the
// wait in front of a call.
func (r *Runner) paceOnce(ctx context.Context) error {
	release, err := r.acquire(ctx)
	if err != nil {
		return err
	}
	release(true)
	return nil
}

// queued reports how many callers wait for the turn.
func (r *Runner) queued() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.interactive) + len(r.background)
}

func waitQueued(t *testing.T, r *Runner, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for r.queued() != n {
		if time.Now().After(deadline) {
			t.Fatalf("queue length = %d, want %d", r.queued(), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// clock is a settable fake clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func newClock() *clock               { return &clock{now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)} }
func noJitterRunner(c *clock, sleep func(context.Context, time.Duration) error) *Runner {
	return New(RunnerConfig{
		CookieProvider: func() (string, string) { return "c", "valid" },
		ThrottleFloor:  20 * time.Second,
		ThrottleJitter: time.Nanosecond,
		RandFloat64:    func() float64 { return 0 },
		Now:            c.Now,
		Sleep:          sleep,
	})
}

// TestQueue_nextCallWaitsForTheRunningOneToExit is the invariant: one yt-dlp
// at a time. A second caller, background or a click, does not get the turn
// while the first still holds it, however long that is.
func TestQueue_nextCallWaitsForTheRunningOneToExit(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		c := newClock()
		r := noJitterRunner(c, func(context.Context, time.Duration) error { return nil })

		release, err := r.acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if interactive {
			ctx = WithInteractive(ctx)
		}
		got := make(chan error, 1)
		go func() { _, err := r.acquire(ctx); got <- err }()
		waitQueued(t, r, 1)
		select {
		case <-got:
			t.Fatalf("interactive=%v: second call got the turn while the first was running", interactive)
		case <-time.After(50 * time.Millisecond):
		}
		release(true)
		select {
		case err := <-got:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("interactive=%v: second call never got the turn after the first exited", interactive)
		}
	}
}

// TestQueue_gapCountsFromExit: the next call starts a gap after the previous
// one EXITED. Spacing from its start would let a call follow a long download
// at once, because the download began long ago.
func TestQueue_gapCountsFromExit(t *testing.T) {
	c := newClock()
	var waited time.Duration
	r := noJitterRunner(c, func(_ context.Context, d time.Duration) error { waited = d; return nil })

	release, err := r.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c.Add(15 * time.Minute) // a long download
	release(true)

	if err := r.paceOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waited != 20*time.Second {
		t.Fatalf("wait after a 15-minute call = %v, want the full 20s gap after its exit", waited)
	}

	// Idle past the gap: nothing to be spaced from, so the next goes at once.
	c.Add(time.Hour)
	if err := r.paceOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waited != 0 {
		t.Fatalf("wait after an hour of quiet = %v, want 0", waited)
	}
}

// TestQueue_clickGoesBeforeQueuedBackgroundWork: a person's call is handed the
// turn ahead of background calls that were queued first.
func TestQueue_clickGoesBeforeQueuedBackgroundWork(t *testing.T) {
	c := newClock()
	r := noJitterRunner(c, func(context.Context, time.Duration) error { return nil })
	release, err := r.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var order []string
	var wg sync.WaitGroup
	start := func(name string, ctx context.Context) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rel, err := r.acquire(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			rel(true)
		}()
	}
	start("scan", context.Background())
	waitQueued(t, r, 1)
	start("captions", context.Background())
	waitQueued(t, r, 2)
	start("click", WithInteractive(context.Background()))
	waitQueued(t, r, 3)

	release(true)
	wg.Wait()
	if got := strings.Join(order, ","); got != "click,scan,captions" {
		t.Fatalf("order = %s, want click,scan,captions", got)
	}
}

// TestQueue_cancelledWaiterLeavesTheQueue: a caller that gives up while
// queued (a closed request, a cancelled job) is removed, and the turn still
// reaches whoever is next. A turn handed to it as it gives up is passed on.
func TestQueue_cancelledWaiterLeavesTheQueue(t *testing.T) {
	c := newClock()
	r := noJitterRunner(c, func(context.Context, time.Duration) error { return nil })
	release, err := r.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	gaveUp := make(chan error, 1)
	go func() { _, err := r.acquire(ctx); gaveUp <- err }()
	waitQueued(t, r, 1)
	next := make(chan struct{})
	go func() {
		rel, err := r.acquire(context.Background())
		if err == nil {
			rel(true)
		}
		close(next)
	}()
	waitQueued(t, r, 2)

	cancel()
	if err := <-gaveUp; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter err = %v, want context.Canceled", err)
	}
	waitQueued(t, r, 1)
	release(true)
	select {
	case <-next:
	case <-time.After(2 * time.Second):
		t.Fatal("the turn never reached the waiter behind the cancelled one")
	}
}

// gapGate is a Sleep that blocks a non-zero wait (the gap) until open is
// closed, so a test can act while the gap is running out.
type gapGate struct {
	open    chan struct{}
	waiting chan struct{}
}

func newGapGate() *gapGate {
	return &gapGate{open: make(chan struct{}), waiting: make(chan struct{}, 8)}
}

func (g *gapGate) sleep(_ context.Context, d time.Duration) error {
	if d > 0 {
		g.waiting <- struct{}{}
		<-g.open
	}
	return nil
}

// TestQueue_clickDuringTheGapGoesFirst: the next caller is picked when the gap
// has run out, not when the previous process exits, so a click arriving during
// the gap still goes before background work queued earlier. Handing the turn
// over at exit would make that click wait for the whole background call.
func TestQueue_clickDuringTheGapGoesFirst(t *testing.T) {
	g := newGapGate()
	r := noJitterRunner(newClock(), g.sleep)
	release, err := r.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	order := make(chan string, 2)
	go func() {
		if rel, err := r.acquire(context.Background()); err == nil {
			order <- "download"
			rel(true)
		}
	}()
	waitQueued(t, r, 1)
	release(true) // the gap starts
	<-g.waiting
	go func() {
		if rel, err := r.acquire(WithInteractive(context.Background())); err == nil {
			order <- "click"
			rel(true)
		}
	}()
	waitQueued(t, r, 2)
	close(g.open) // the gap is over

	if first := <-order; first != "click" {
		t.Fatalf("first after the gap = %s, want the click that arrived during it", first)
	}
	<-order
}

// TestQueue_cancelDuringTheGapLeavesNothingStuck: a caller that gives up while
// the gap runs out leaves the queue, and the Runner is free once it has.
func TestQueue_cancelDuringTheGapLeavesNothingStuck(t *testing.T) {
	g := newGapGate()
	c := newClock()
	r := noJitterRunner(c, g.sleep)
	if err := r.paceOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	gaveUp := make(chan error, 1)
	go func() { _, err := r.acquire(ctx); gaveUp <- err }()
	<-g.waiting
	cancel()
	if err := <-gaveUp; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	close(g.open)

	c.Add(time.Minute)
	done := make(chan error, 1)
	go func() { done <- r.paceOnce(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the Runner stayed busy after the only waiter gave up during the gap")
	}
}

// TestQueue_callThatNeverRanOwesNoGap: a call refused after its wait (pause,
// stale cookie) made no YouTube request, so the next one is not spaced from it.
func TestQueue_callThatNeverRanOwesNoGap(t *testing.T) {
	c := newClock()
	var waited time.Duration
	r := noJitterRunner(c, func(_ context.Context, d time.Duration) error { waited = d; return nil })
	release, err := r.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release(false)
	if err := r.paceOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waited != 0 {
		t.Fatalf("wait after a call that never ran = %v, want 0", waited)
	}
}

// TestQueue_hungCallIsCutOff: a non-download call that never exits holds the
// one turn every YouTube call needs; the ceiling ends it so the queue moves on.
func TestQueue_hungCallIsCutOff(t *testing.T) {
	var logs bytes.Buffer
	prev := maxCallRuntime
	maxCallRuntime = 200 * time.Millisecond
	t.Cleanup(func() { maxCallRuntime = prev })

	bin := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil { //nolint:gosec // test stand-in for an executable
		t.Fatal(err)
	}
	r := New(RunnerConfig{
		Bin:            bin,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
		Logger:         slog.New(slog.NewTextHandler(&logs, nil)),
	})
	start := time.Now()
	if _, err := r.Metadata(context.Background(), "https://youtu.be/dQw4w9WgXcQ"); err == nil {
		t.Fatal("a hung call returned no error")
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("hung call took %v, want it cut off near the ceiling", took)
	}
	if r.queued() != 0 {
		t.Fatal("the queue did not move on")
	}
	if !strings.Contains(logs.String(), "runtime ceiling hit") {
		t.Fatalf("a ceiling kill was not logged at warn:\n%s", logs.String())
	}
}

// TestQueue_processesNeverOverlap drives real processes through the Runner:
// two concurrent Metadata calls against a fake yt-dlp that logs its start and
// exit must run strictly one after the other.
func TestQueue_processesNeverOverlap(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls.log")
	inner := fakeBinPath(t)
	bin := filepath.Join(dir, "yt-dlp")
	script := "#!/bin/sh\necho start >> " + logPath + "\nsleep 0.3\necho end >> " + logPath + "\nexec " + inner + " \"$@\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil { //nolint:gosec // test stand-in for an executable
		t.Fatal(err)
	}
	r := New(RunnerConfig{
		Bin:            bin,
		CookieProvider: func() (string, string) { return "cookie-text", "valid" },
		Sleep:          func(context.Context, time.Duration) error { return nil },
	})

	var wg sync.WaitGroup
	for _, ctx := range []context.Context{context.Background(), WithInteractive(context.Background())} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Metadata(ctx, "https://youtu.be/dQw4w9WgXcQ")
		}()
	}
	wg.Wait()

	got, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(strings.Fields(string(got)), ","); s != "start,end,start,end" {
		t.Fatalf("process log = %s, want start,end,start,end (no overlap)", s)
	}
}
