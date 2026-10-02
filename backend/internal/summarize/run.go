package summarize

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
)

// analysisRun carries the logging state of one video's analysis: who it is,
// when it started, and the chat tokens it has cost so far. It exists so every
// line about a video — start, each step, failures, the total — carries the same
// identity (title and channel, not just an opaque id) without threading five
// arguments through the worker.
// A nil *analysisRun is valid and every method on it is a no-op, which is what
// lets the failure paths that run before the analysis is announced (and the
// panic recovery) call them unconditionally.
type analysisRun struct {
	log *slog.Logger
	ctx context.Context // carries the CallInfo the llm client logs against
	// store is where finished() banks the run's spend. Held here rather than
	// reached for through the worker because finished() is called from the
	// defers and panic recovery of paths that have no worker in hand.
	store   *videos.Store
	totals  *llm.Totals
	video   *videos.Video
	job     *summaryjobs.Job
	started time.Time

	// stepStarted is only for the failure lines of the step currently running;
	// each step's own duration and token delta live in its done closure, so a
	// done() called out of order cannot report another step's numbers.
	stepStarted time.Time

	// banked stops the run's spend being written twice. finished() used to only
	// log, so calling it twice cost nothing; it now writes to the video row, and
	// there is one path that can reach it twice — a panic raised AFTER a normal
	// finished() (in Jobs.Finish, say) unwinds into processOne's recover, which
	// calls finished("panic") on the same run. That would double the video's
	// recorded cost on the one occasion nobody is watching the numbers.
	banked bool
}

// startRun announces the analysis and returns its logging state. attempt/
// max_attempts come straight from the job row: ClaimNext already incremented
// attempts, so they read as "attempt N of M" for the retries the queue does on
// its own.
func (w *Worker) startRun(ctx context.Context, job *summaryjobs.Job, video *videos.Video) *analysisRun {
	totals := &llm.Totals{}
	r := &analysisRun{
		log:    w.d.Logger,
		store:  w.d.Videos,
		totals: totals,
		ctx: llm.WithTotals(llm.WithCall(ctx, llm.CallInfo{
			VideoID: video.ID,
			Title:   video.Title,
			Channel: video.ChannelName,
		}), totals),
		video:   video,
		job:     job,
		started: time.Now(),
	}
	r.log.Info("summarize worker: analysis started", append(r.ident(),
		"attempt", attemptLabel(job),
		// A resumed job already has a usable summary and is only redoing the
		// fragile key-points step.
		"resumed", video.SummaryStatus == videos.SummaryDone)...)
	return r
}

// attemptLabel renders the queue's retry counters as "1/3" — one field to read
// instead of two to correlate. ClaimNext has already incremented attempts, so
// it reads as "this attempt, of the allowed maximum".
func attemptLabel(job *summaryjobs.Job) string {
	return strconv.Itoa(job.Attempts) + "/" + strconv.Itoa(job.MaxAttempts)
}

// pipelineStages are the analysis stages in execution order. Their position is
// what "2/4" in a log line counts against, so a stage a resumed job skips still
// leaves the others numbered where a reader expects them.
var pipelineStages = []string{"summary", "classify", "keypoints", "embedding"}

// stageMessage builds a stage line's message: "stage 2/4 done". A stage that
// is not in pipelineStages is named instead of numbered — a wrong number would
// silently renumber its neighbours, and a bare "stage  done" would just look
// broken.
func stageMessage(name, verb string) string {
	for i, s := range pipelineStages {
		if s == name {
			return "summarize worker: stage " + strconv.Itoa(i+1) + "/" +
				strconv.Itoa(len(pipelineStages)) + " " + verb
		}
	}
	return "summarize worker: stage " + name + " " + verb
}

// ident is the video identity every line repeats. It returns a fresh slice so
// callers can append to it safely.
func (r *analysisRun) ident() []any {
	if r == nil {
		return nil
	}
	return []any{"video_id", r.video.ID, "title", r.video.Title, "channel", r.video.ChannelName}
}

// step marks the start of a pipeline step and returns the context its LLM
// calls must use plus the func that logs the step as done. Extra key/values
// passed to that func are appended to the line.
// A nil run has no context to hand out, and handing out a background one would
// give the caller's LLM calls no cancellation — they would outlive a shutdown
// by up to the client timeout. Steps only ever run once the analysis has
// started, so this cannot happen; it panics rather than degrading quietly if
// that ever changes.
func (r *analysisRun) step(name string) (context.Context, func(extra ...any)) {
	if r == nil {
		panic("summarize: step on a nil analysis run — a stage ran before the analysis started")
	}
	started := time.Now()
	before := r.totals.Snapshot()
	r.stepStarted = started
	// The stage rides on the context too, so the client's "still waiting"
	// heartbeat says which stage of which video is stuck.
	sctx := llm.WithStage(llm.WithStep(r.ctx, name), stageOf(name))
	r.log.Info(stageMessage(name, "started"), append([]any{"step", name}, r.ident()...)...)
	return sctx, func(extra ...any) {
		attrs := append([]any{"step", name}, r.ident()...)
		attrs = append(attrs, "duration_ms", time.Since(started).Milliseconds())
		attrs = append(attrs, extra...)
		attrs = append(attrs, r.totals.Snapshot().Sub(before).LogAttrs()...)
		r.log.Info(stageMessage(name, "done"), attrs...)
	}
}

// stageOf is the "2/4" the client's heartbeat carries; empty for a stage that
// is not in pipelineStages, since CallInfo omits an empty stage entirely.
func stageOf(name string) string {
	for i, s := range pipelineStages {
		if s == name {
			return strconv.Itoa(i+1) + "/" + strconv.Itoa(len(pipelineStages))
		}
	}
	return ""
}

// stepElapsedMs is the running step's wall time, for that step's own failure
// lines (which have no done closure to read).
func (r *analysisRun) stepElapsedMs() int64 {
	if r == nil {
		return 0
	}
	return time.Since(r.stepStarted).Milliseconds()
}

// skipped records a step a resumed job did not have to redo. Debug, not info:
// it is context for reading a retry, not news.
func (r *analysisRun) skipped(name, reason string) {
	if r == nil {
		return
	}
	r.log.Debug(stageMessage(name, "skipped"),
		append([]any{"step", name}, append(r.ident(), "reason", reason)...)...)
}

// succeeded reports whether an outcome passed to finished is a success.
//
// A prefix test rather than an equality, because "done" is not the only one:
// an inbox read finishes as done_inbox or done_inbox_indexed, and both are
// terminal — the job is marked StateDone on the very next line. Comparing
// against "done" alone printed will_retry=true beside outcome=done_inbox,
// which reads as though something were still pending on a video that is
// finished and already rendering its summary.
//
// Every failing outcome is named for its failure (error, panic, <step>_failed),
// so a new success spelled done_* is covered here on the day it is added and a
// new failure cannot pass by accident.
func succeeded(outcome string) bool { return strings.HasPrefix(outcome, "done") }

// finished logs the whole analysis: wall time plus the chat tokens it cost.
// retrying distinguishes a failure the queue will pick up again from a
// terminal one — without it an outcome of "error" reads as final on a job that
// still has attempts left. Embedding tokens are not in here: they come from a
// different endpoint and are logged by the embedding client at debug.
func (r *analysisRun) finished(outcome string) {
	if r == nil {
		return
	}
	total := r.totals.Snapshot()
	r.bankSpend(total)
	elapsed := time.Since(r.started).Milliseconds()
	// Everything that was not inference: the pacing gap, embedding, VTT
	// parsing, SQLite writes. Printed so the numbers on the line add up and a
	// slow video can be blamed on the right thing. Clamped at zero: inference
	// is a subset of the run, so a negative here would be an accounting bug,
	// and a nonsense negative in the log helps nobody.
	wait := elapsed - total.InferenceMillis()
	if wait < 0 {
		wait = 0
	}
	attrs := append(r.ident(), "outcome", outcome,
		"duration_ms", elapsed,
		"wait_ms", wait,
		"attempt", attemptLabel(r.job),
		"will_retry", !succeeded(outcome) && r.job.Attempts < r.job.MaxAttempts)
	r.log.Info("summarize worker: analysis finished", append(attrs, total.LogAttrs()...)...)
}

// bankSpend records what this run cost against the video row, so the figure
// outlives the log line beside it.
//
// Called from finished() on EVERY outcome, success and failure alike. A run
// that died in the keypoints step still paid for the summary it produced first,
// and token columns that only counted successes would quietly under-report
// exactly the videos that spent the most.
//
// Best-effort by design: the analysis is over by the time this runs and its
// real artifacts are already committed, so a bookkeeping write that fails must
// not turn a finished video into a failed job. It warns and moves on.
func (r *analysisRun) bankSpend(total llm.Usage) {
	// Nothing accounted means either no call was made (a resumed job that
	// skipped every LLM step) or the endpoint reported no usage. Neither is a
	// zero worth adding, and skipping keeps the write off the fast path of a
	// job that did no inference at all.
	if r.store == nil || total.Accounted == 0 || r.banked {
		return
	}
	// Set before the write, not after: a failed write must not leave the door
	// open for a second attempt from the panic path, which would be running
	// against a row whose state nobody has checked.
	r.banked = true
	err := r.store.AddChatUsage(r.video.ID, videos.ChatUsage{
		PromptTokens:     total.PromptTokens,
		CachedTokens:     total.CachedTokens,
		CompletionTokens: total.CompletionTokens,
	})
	if err != nil {
		r.log.Warn("summarize worker: recording chat usage failed", append(r.ident(), "err", err)...)
	}
}
