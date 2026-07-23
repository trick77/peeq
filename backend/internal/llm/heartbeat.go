package llm

import (
	"context"
	"log/slog"
	"sync"
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
// That wait is deliberately UNBOUNDED, which is a trade-off worth stating:
// callers defer stop() in the request path, so a log sink that blocks (a full
// pipe with no reader, a wedged container log driver) now stalls the caller
// rather than just this goroutine. It is still the right side to err on:
//
//   - stop() is DEFERRED, so the caller's own log lines have already run by
//     the time it fires — llm/client.go and rag/embed.go both log their
//     outcome and then return. Against a wedged sink the caller is therefore
//     already stuck on its own write and never reaches stop(), so the wait
//     adds no stall that was not there.
//   - A timeout would restore the race precisely when writes are slowest,
//     which is exactly when the goroutine is most likely to still be inside
//     one. The bounded version is worst where it matters most.
//
// Correctness under a wedged sink beats liveness that is already lost.
//
// stop must be called exactly once (a defer at the call site); calling it
// twice panics on the closed channel, which is the intended loud failure.
func StartHeartbeat(ctx context.Context, log *slog.Logger, interval time.Duration, msg string, attrs ...any) (stop func()) {
	return StartHeartbeatFunc(ctx, log, interval, msg, nil, attrs...)
}

// StartHeartbeatFunc is StartHeartbeat with a per-tick attribute provider. It
// exists because the useful thing to say about a streaming request is not that
// it is still waiting — the plain heartbeat already said that, and said it
// identically for a model mid-thought and a socket that died — but how much has
// arrived since the last line. A tick reading chunks=0 and one reading
// chunks=412 are the two cases we could not tell apart before.
//
// progress is called ON THE HEARTBEAT GOROUTINE while the request goroutine is
// still writing those counters, so whatever it reads must be safe to read
// concurrently (the chat client uses atomics). It may be nil, which is exactly
// StartHeartbeat. Its attrs come after the static ones and before elapsed_s.
func StartHeartbeatFunc(ctx context.Context, log *slog.Logger, interval time.Duration, msg string, progress func() []any, attrs ...any) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	started := time.Now()
	done := make(chan struct{})
	// A WaitGroup rather than a second channel: "wait for this goroutine to
	// finish" is exactly what it says, and it leaves one fewer invariant than
	// a done/finished pair whose difference a reader has to infer.
	var running sync.WaitGroup
	running.Add(1)
	go func() {
		defer running.Done()
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
				var extra []any
				if progress != nil {
					extra = progress()
				}
				line := make([]any, 0, len(attrs)+len(extra)+2)
				line = append(line, attrs...)
				line = append(line, extra...)
				line = append(line, "elapsed_s", int64(time.Since(started).Seconds()))
				log.Info(msg, line...)
			}
		}
	}()
	return func() {
		close(done)
		running.Wait()
	}
}
