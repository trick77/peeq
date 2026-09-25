package ytdlp

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCallWithCap_stalledOnceRunning(t *testing.T) {
	stalled, err := CallWithCap(context.Background(), 20*time.Millisecond, func(ctx context.Context) error {
		SignalStart(ctx) // the process is running; the cap arms now
		<-ctx.Done()
		return ctx.Err()
	})
	if !stalled {
		t.Fatal("a call cut by its own cap must report stalled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cap's cancellation", err)
	}
}

func TestCallWithCap_queueingWaitIsNotCounted(t *testing.T) {
	// The call spends longer than the cap before its process starts, then
	// finishes promptly: no stall.
	stalled, err := CallWithCap(context.Background(), 20*time.Millisecond, func(ctx context.Context) error {
		time.Sleep(50 * time.Millisecond)
		SignalStart(ctx)
		// Observe the context AFTER the start: a cap armed on entry would
		// have fired during the sleep and this would report it.
		return ctx.Err()
	})
	if stalled || err != nil {
		t.Fatalf("stalled=%v err=%v, want neither", stalled, err)
	}
}

func TestCallWithCap_failureBeforeExecIsNotAStall(t *testing.T) {
	boom := errors.New("gate refused")
	stalled, err := CallWithCap(context.Background(), time.Hour, func(context.Context) error { return boom })
	if stalled {
		t.Fatal("a failure that never reached exec was reported as a stall")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the call's own error", err)
	}
}

func TestCallWithCap_parentCancelIsNotAStall(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	stalled, err := CallWithCap(parent, time.Hour, func(ctx context.Context) error {
		SignalStart(ctx)
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	if stalled {
		t.Fatal("a parent cancellation (shutdown) was reported as a stall")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestCallWithCap_disabledCapNeverFires(t *testing.T) {
	stalled, err := CallWithCap(context.Background(), 0, func(ctx context.Context) error {
		SignalStart(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(30 * time.Millisecond):
			return nil
		}
	})
	if stalled || err != nil {
		t.Fatalf("stalled=%v err=%v with the cap disabled", stalled, err)
	}
}

// A process that fails on its own just before the cap expires is a failure,
// not a stall: retrying a private video as if it might come back, or telling
// the user a refresh "timed out", would be wrong.
func TestCallWithCap_ownFailureBeforeTheCapIsNotAStall(t *testing.T) {
	boom := errors.New("terminal (private)")
	stalled, err := CallWithCap(context.Background(), 15*time.Millisecond, func(ctx context.Context) error {
		SignalStart(ctx)
		time.Sleep(10 * time.Millisecond)
		return boom
	})
	if stalled {
		t.Fatal("a failure that came before the cap was reported as a stall")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	// The timer may still fire after the return; it must not flip anything.
	time.Sleep(20 * time.Millisecond)
}

// A panic inside fn (the resolver parses external input; callers recover)
// leaves no armed timer behind.
func TestCallWithCap_stopsTheTimerOnPanic(t *testing.T) {
	fired := make(chan struct{}, 1)
	func() {
		defer func() { _ = recover() }()
		_, _ = CallWithCap(context.Background(), 10*time.Millisecond, func(ctx context.Context) error {
			SignalStart(ctx)
			go func() {
				<-ctx.Done()
				fired <- struct{}{}
			}()
			panic("yt-dlp output went sideways")
		})
	}()
	select {
	case <-fired:
		// cancel() ran on the way out, as deferred; the timer itself was stopped.
	case <-time.After(50 * time.Millisecond):
		t.Fatal("the capped context was never released after a panic")
	}
}
