package llm

import (
	"context"
	"log/slog"
	"time"
)

// DefaultHeartbeat is how often an in-flight request reports that it is still
// waiting. Shared by the chat and embedding clients so a stall looks the same
// wherever it happens.
const DefaultHeartbeat = 15 * time.Second

// StartHeartbeat logs msg every interval until the returned stop func is
// called, so a request that takes minutes is visibly alive rather than
// indistinguishable from a hung worker. Each line repeats attrs and adds
// elapsed_s. A non-positive interval disables it and yields a no-op stop.
//
// stop BLOCKS until the heartbeat goroutine has finished, including any log
// line already being written when it was called. Signalling alone would not
// be enough: "stopped" has to mean the logger is no longer in use, or a
// caller that inspects the log sink afterwards — every test that asserts on
// heartbeat output does exactly this — reads it while the goroutine is still
// writing. That is a real data race, and it failed CI on unrelated PRs.
//
// stop must be called exactly once (a defer at the call site); calling it
// twice panics on the closed channel, which is the intended loud failure.
func StartHeartbeat(ctx context.Context, log *slog.Logger, interval time.Duration, msg string, attrs ...any) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	started := time.Now()
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				// Copy attrs per tick: appending into a shared backing array
				// across ticks would have each line quietly overwrite the last.
				line := make([]any, 0, len(attrs)+2)
				line = append(line, attrs...)
				line = append(line, "elapsed_s", int64(time.Since(started).Seconds()))
				log.Info(msg, line...)
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
