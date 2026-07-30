package summarize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/llm"
	"github.com/trick77/peeq/internal/media"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/subtitles"
	"github.com/trick77/peeq/internal/summaryjobs"
	"github.com/trick77/peeq/internal/videos"
)

// Embedder is the subset of rag.EmbedClient the worker needs. Batched, because
// chapter chunks roughly doubled how many texts one video contributes and the
// whole set used to ride in a single request under a one-minute timeout.
type Embedder interface {
	EmbedBatched(ctx context.Context, inputs []string, gap time.Duration) ([][]float32, error)
}

// ActivityRecorder records a summary outcome for the Activity feed. Nil-safe.
type ActivityRecorder interface {
	Record(activity.Event)
}

// WorkerDeps are the worker's collaborators and tunables. The stores,
// Summarizer, and Embedder are required; the rest have safe defaults applied
// in NewWorker.
type WorkerDeps struct {
	Jobs         *summaryjobs.Store
	Videos       *videos.Store
	Rag          *rag.Store
	Summarizer   *Summarizer
	Embedder     Embedder
	MediaDir     string
	EmbedModel   string
	EmbedDim     int
	PollInterval time.Duration
	// VideoDelay is a pause between videos, giving a slow/rate-limited LLM
	// endpoint room to breathe. 0 disables it.
	VideoDelay time.Duration
	Logger     *slog.Logger

	// OnPhase, when set, is called at each summary state transition so an SSE
	// hub can push live progress to the Player. videoID is always set so the
	// client can filter to the open video.
	OnPhase func(videoID, status, phase string)
	// Activity, when set, records each terminal summary for the Activity feed.
	Activity ActivityRecorder
}

// Worker is the single-concurrency summarization+embedding loop: the twin of
// internal/download/worker.go for the summary_jobs queue.
type Worker struct {
	d WorkerDeps
	// classifyFailed holds video ids whose idle-sweep classify call errored, so
	// the sweep moves past them instead of retrying the same video forever.
	// Process-lifetime only: a restart gives every video another chance, which
	// is the right bound for a transient LLM outage.
	classifyFailed map[string]bool
}

// NewWorker builds a Worker, filling in defaults for the optional deps.
func NewWorker(d WorkerDeps) *Worker {
	if d.PollInterval <= 0 {
		d.PollInterval = 2 * time.Second
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	return &Worker{d: d, classifyFailed: map[string]bool{}}
}

// Run is the worker loop; it blocks until ctx is cancelled. It first resets
// any orphaned running jobs left by a previous process and backfills jobs for
// downloaded videos that never got one, then repeatedly claims and processes
// the next job.
func (w *Worker) Run(ctx context.Context) {
	if err := w.d.Jobs.ResetOrphans(); err != nil {
		w.d.Logger.Error("summarize worker: reset orphans", "err", err)
	}
	// A video can be downloaded without a summary job when a process dies in
	// the window between the two writes (see taimport.importOne). Nothing else
	// ever revisits such a video, so sweep for them once at boot.
	if n, err := w.d.Jobs.EnqueueMissing(); err != nil {
		w.d.Logger.Error("summarize worker: backfill missing jobs", "err", err)
	} else if n > 0 {
		w.d.Logger.Info("summarize worker: backfilled summary jobs", "count", n)
	}
	for {
		if ctx.Err() != nil {
			return
		}
		did, err := w.processOne(ctx)
		if err != nil {
			w.d.Logger.Error("summarize worker: process", "err", err)
		}
		// Idle: poll again shortly. Busy: pause VideoDelay so the LLM endpoint
		// gets room to breathe between videos.
		gap := w.d.PollInterval
		if did {
			gap = w.d.VideoDelay
		}
		if gap > 0 {
			t := time.NewTimer(gap)
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
			}
		}
	}
}

// processOne claims and processes a single job. When the queue is empty it
// spends the turn on the classification backlog instead, and returns false
// only when there is nothing left to do at all. Panics are recovered so one bad
// job never kills the loop. This is the test seam: tests call it directly to
// drive one job deterministically, without goroutine/ticker timing.
func (w *Worker) processOne(ctx context.Context) (did bool, err error) {
	job, err := w.d.Jobs.ClaimNext()
	if err != nil {
		return false, err
	}
	if job == nil {
		return w.classifyOne(ctx)
	}
	// Declared before the recover so a panic mid-analysis still gets a terminal
	// line with the tokens that video had already spent.
	var run *analysisRun
	defer func() {
		if r := recover(); r != nil {
			w.d.Logger.Error("summarize worker: recovered", "job_id", job.ID, "panic", r)
			run.finished("panic")
			_, _ = w.d.Jobs.Fail(job.ID, job.Attempts, "panic")
			_ = w.d.Videos.SetSummaryStatus(job.VideoID, videos.SummaryError, "internal error")
		}
	}()

	video, err := w.d.Videos.Get(job.VideoID)
	if err != nil || video == nil {
		w.d.Logger.Warn("summarize worker: video missing", "job_id", job.ID, "video_id", job.VideoID, "err", err)
		_ = w.d.Jobs.Finish(job.ID, summaryjobs.StateFailed, "video missing")
		return true, err
	}

	// No subtitles => clean terminal no_transcript state, not an error. Checked
	// before the analysis is announced: a video that is never analyzed must not
	// log a start line, or an import of subtitle-less videos fills the log with
	// analyses that begin and never end.
	if video.SubtitlePath == "" {
		w.finishNoTranscript(job, video, "no subtitle file")
		return true, nil
	}

	// Announce "running" only for a fresh job; a resumed one (retrying just the
	// key-points step) already has its summary marked done and must not regress.
	if video.SummaryStatus != videos.SummaryDone {
		_ = w.d.Videos.SetSummaryStatus(video.ID, videos.SummaryRunning, "")
		w.emit(video.ID, videos.SummaryRunning, PhaseSummarizing)
	}

	safe, err := media.SafeMediaPath(w.d.MediaDir, video.SubtitlePath)
	if err != nil {
		return true, w.failJob(job, video, run, "unsafe subtitle path")
	}
	f, err := os.Open(safe)
	if err != nil {
		w.finishNoTranscript(job, video, "subtitle file unreadable")
		return true, nil
	}
	parsed, perr := subtitles.ParseVTT(f)
	f.Close()
	if perr != nil {
		return true, w.failJob(job, video, run, "parse vtt: "+perr.Error())
	}
	if parsed.Transcript == "" {
		w.discardStaleAnalysis(ctx, video)
		w.finishNoTranscript(job, video, "empty transcript")
		return true, nil
	}
	// Captions that are just music/ambience with the odd lyric fragment are not
	// speech. Summarizing them produces a confident description of a video that
	// says nothing, so treat them like a missing transcript. Checked here, before
	// startRun, so a music video never opens an analysis log line either.
	if parsed.IsNonSpeech(int(video.DurationSeconds)) {
		w.discardStaleAnalysis(ctx, video)
		w.finishNoTranscript(job, video, "no speech (music only)")
		return true, nil
	}

	// There is real work to do: everything from here on is logged against this
	// video — one identity, one token accumulator, one wall clock.
	run = w.startRun(ctx, job, video)

	// The pipeline is resumable: each artifact is saved the moment it is
	// produced, and a retry skips whatever a prior attempt already stored. So a
	// failure in the fragile key-points step (step 3) never discards the summary
	// or embeddings, and only that step re-runs.

	// Step 1 — prose summary. Persist on its own; skip if already saved.
	summary := video.Summary
	if summary == "" {
		sctx, done := run.step("summary")
		s, serr := w.d.Summarizer.SummarizeText(sctx, parsed.Transcript)
		if serr != nil {
			return true, w.failJob(job, video, run, serr.Error())
		}
		if err := w.d.Videos.SetSummaryText(video.ID, s); err != nil {
			return true, w.failJob(job, video, run, err.Error())
		}
		// No chunk count here: chat_requests already says how many map calls the
		// transcript cost, without chunking it a second time just to log it.
		done()
		summary = s
	} else {
		run.skipped("summary", "already stored")
	}

	// Step 2 — category (best-effort). It needs only the title and the summary,
	// so it runs here rather than at the end: behind the fragile key-points call
	// it never ran at all for videos whose endpoint timed out there, which is
	// how a large backlog ended up summarized but 'uncategorized'. A classify
	// failure must NOT fail the job — the summary above is already saved, and
	// the idle sweep in classifyOne picks the video up later.
	//
	// `video` was read before the summary call above, so this test is against
	// a stale row; SetCategoryIfUnset re-checks at write time, which is what
	// actually protects a category the user picked on the Player while the
	// summary was still running.
	if video.Category == "" || video.Category == videos.UncategorizedCategory {
		// Surface the classify step as a live phase (summarizing → classifying →
		// embedding). It sits before the "done" emit below, so it is safe for the
		// Player, which treats "done" as terminal.
		w.emit(video.ID, videos.SummaryRunning, PhaseClassifying)
		cctx, done := run.step("classify")
		raw, cerr := w.d.Summarizer.Classify(cctx, video.Title, summary, videos.ClassifiableCategories())
		switch {
		case cerr != nil:
			w.d.Logger.Warn("summarize worker: classify failed", append(run.ident(), "duration_ms", run.stepElapsedMs(), "err", cerr)...)
		default:
			category := videos.NormalizeCategory(raw)
			applied, serr := w.d.Videos.SetCategoryIfUnset(video.ID, category)
			if serr != nil {
				w.d.Logger.Error("summarize worker: set category failed", append(run.ident(), "err", serr)...)
			} else {
				// applied=false means the user picked a category by hand while
				// this call was in flight, and theirs won.
				done("category", category, "applied", applied)
			}
		}
	} else {
		run.skipped("classify", "already categorized")
	}

	// Summary is usable now. Persist "done" so the Library shows it,
	// and emit "done" so the live Player fetches it immediately — it refetches
	// on the "done" event — even though key-points and embedding still have to
	// run, and even if the fragile key-points step later fails and requeues (the
	// emit is unconditional so a resumed attempt re-signals the open Player).
	// Note SEARCH is not ready here any more: embedding moved after key points,
	// because chapter chunks are built from what key points writes. The Player
	// does not depend on the index, so it can still refetch now; a video is
	// simply not findable for the few seconds between these two steps.
	// The event's phase rides as "keypoints", not "": that keeps the Queue meter
	// on the final stage instead
	// of reading the job as finished (the row lives until the job is Finish()ed),
	// while status "done" — not "running" — is what the Player keys on, so the
	// two consumers stay correct off one event.
	if video.SummaryStatus != videos.SummaryDone {
		_ = w.d.Videos.SetSummaryStatus(video.ID, videos.SummaryDone, "")
	}
	w.emit(video.ID, videos.SummaryDone, PhaseKeypoints)

	// Step 3 — key points (and chapters when yt-dlp didn't supply them). The
	// fragile call. It now runs BEFORE embedding rather than last, because the
	// chapters it writes are what chapter chunks are built from; embedding first
	// would index every video as though it had no chapters.
	ytChapters := decodeChapters(video.Chapters)
	kctx, done := run.step("keypoints")
	chapters, keyPoints, err := w.d.Summarizer.KeyPoints(kctx, summary, parsed.Cues, ytChapters)
	if err != nil {
		// Moving embedding after this step means a video whose key-points call
		// keeps failing would never be indexed at all — unfindable, with no
		// sign of why. So a video that has never been embedded gets a
		// best-effort pass here with whatever chapters exist (yt-dlp's, or
		// none). SetKeyPoints zeroes embed_rev, so if key points does eventually
		// succeed the video is re-indexed properly with its chapters.
		if video.EmbedModel == "" {
			if eerr := w.embedAndStore(kctx, video.ID, parsed, summary, ytChapters); eerr != nil {
				w.d.Logger.Warn("summarize worker: fallback embedding failed",
					append(run.ident(), "err", eerr)...)
			} else {
				w.d.Logger.Info("summarize worker: indexed without chapters after key-points failure",
					run.ident()...)
			}
		}
		return true, w.requeueJob(job, video, run, err.Error())
	}
	if err := w.d.Videos.SetKeyPoints(video.ID, encodeChapters(chapters), encodeKeyPoints(keyPoints)); err != nil {
		return true, w.requeueJob(job, video, run, err.Error())
	}
	done("chapters", len(chapters), "key_points", len(keyPoints))

	// Step 4 — embeddings, last so the index is built from the finished
	// analysis. Gated on the content recipe rather than "is embed_model set":
	// that older test cannot tell an index built under a previous recipe from a
	// current one, which is exactly what adding chapter chunks introduced.
	//
	// `chapters` here is the value KeyPoints just returned, not video.Chapters —
	// that field was read at claim time and is stale after SetKeyPoints.
	if video.EmbedRev < rag.ChunkRecipeRev {
		w.emit(video.ID, videos.SummaryRunning, PhaseEmbedding)
		ectx, edone := run.step("embedding")
		if err := w.embedAndStore(ectx, video.ID, parsed, summary, chapters); err != nil {
			return true, w.failJob(job, video, run, err.Error())
		}
		edone("chunks", len(chapters))
	} else {
		run.skipped("embedding", "already embedded at current recipe")
	}
	w.emit(video.ID, videos.SummaryDone, "")

	run.finished("done")
	_ = w.d.Jobs.Finish(job.ID, summaryjobs.StateDone, "")
	w.recordActivity(activity.Event{
		Kind: activity.KindSummary, Outcome: activity.OutcomeOK,
		SubjectID: video.ID, Subject: video.Title, Summary: "summarized",
		Detail: fmt.Sprintf("%d key points", len(keyPoints)),
	})
	return true, nil
}

// recordActivity records a summary event for the Activity feed, nil-safe.
func (w *Worker) recordActivity(e activity.Event) {
	if w.d.Activity != nil {
		w.d.Activity.Record(e)
	}
}

// analysisRun carries the logging state of one video's analysis: who it is,
// when it started, and the chat tokens it has cost so far. It exists so every
// line about a video — start, each step, failures, the total — carries the same
// identity (title and channel, not just an opaque id) without threading five
// arguments through the worker.
// A nil *analysisRun is valid and every method on it is a no-op, which is what
// lets the failure paths that run before the analysis is announced (and the
// panic recovery) call them unconditionally.
type analysisRun struct {
	log     *slog.Logger
	ctx     context.Context // carries the CallInfo the llm client logs against
	totals  *llm.Totals
	video   *videos.Video
	job     *summaryjobs.Job
	started time.Time

	// stepStarted is only for the failure lines of the step currently running;
	// each step's own duration and token delta live in its done closure, so a
	// done() called out of order cannot report another step's numbers.
	stepStarted time.Time
}

// startRun announces the analysis and returns its logging state. attempt/
// max_attempts come straight from the job row: ClaimNext already incremented
// attempts, so they read as "attempt N of M" for the retries the queue does on
// its own.
func (w *Worker) startRun(ctx context.Context, job *summaryjobs.Job, video *videos.Video) *analysisRun {
	totals := &llm.Totals{}
	r := &analysisRun{
		log:    w.d.Logger,
		totals: totals,
		ctx: llm.WithCall(ctx, llm.CallInfo{
			VideoID: video.ID,
			Title:   video.Title,
			Channel: video.ChannelName,
			Totals:  totals,
		}),
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
		"will_retry", outcome != "done" && r.job.Attempts < r.job.MaxAttempts)
	r.log.Info("summarize worker: analysis finished", append(attrs, total.LogAttrs()...)...)
}

// finishNoTranscript closes out a video that has nothing to summarize. It is a
// clean terminal state, not an error, but it must still be visible: otherwise
// a video simply disappears from the queue with no explanation.
func (w *Worker) finishNoTranscript(job *summaryjobs.Job, video *videos.Video, reason string) {
	_ = w.d.Videos.SetSummaryStatus(video.ID, videos.SummaryNoTranscript, "")
	w.emit(video.ID, videos.SummaryNoTranscript, "")
	_ = w.d.Jobs.Finish(job.ID, summaryjobs.StateDone, "")
	w.d.Logger.Info("summarize worker: no transcript", "video_id", video.ID, "title", video.Title,
		"channel", video.ChannelName, "reason", reason)
}

// discardStaleAnalysis throws away what an earlier run stored for a video whose
// subtitles have now been read and found to contain nothing worth summarizing.
// Without it a re-analysis only flips summary_status: the old summary text and
// its embedded chunks stay in the database and keep matching semantic search,
// where the UI gives no sign they are still there. Clearing the summary also
// drops the video out of NextUnclassified (it requires summary <> ”), so the
// idle classify sweep stops picking it up.
//
// This is deliberately NOT called when the subtitle file is simply absent or
// unreadable: Tombstone() blanks subtitle_path, so a retention-swept video
// takes that path and must keep the summary it was archived with.
//
// Best-effort throughout — the caller's path is terminal and a cleanup failure
// must not turn it into a job failure.
func (w *Worker) discardStaleAnalysis(ctx context.Context, video *videos.Video) {
	// The row write is skippable when the row is already clean, but the chunk
	// delete is NOT gated on it. An empty summary column does not mean there are
	// no chunks: handleReprocess clears the summary before enqueuing, so on
	// the flow that matters most — a user hitting Reprocess to fix a video
	// that was summarized wrongly — the worker sees a blank row and the stale
	// embeddings would live on in semantic search. DeleteVideoChunks on a video
	// with no chunks is a no-op, so running it unconditionally costs nothing.
	if video.Summary != "" || video.Chapters != "" || video.KeyPoints != "" {
		if err := w.d.Videos.ClearSummary(video.ID); err != nil {
			w.d.Logger.Error("summarize worker: clear stale summary", "video_id", video.ID, "err", err)
		}
	}
	if w.d.Rag != nil {
		if err := w.d.Rag.DeleteVideoChunks(ctx, video.ID); err != nil {
			w.d.Logger.Error("summarize worker: clear stale chunks", "video_id", video.ID, "err", err)
		}
	}
}

// classifyOne repairs one video from the classification backlog: a downloaded
// video that has a summary but is still 'uncategorized'. It runs only when the
// summary queue is empty, so real work always wins, and reports did=true only
// when it actually made an LLM call — returning true on an empty backlog would
// spin the Run loop, which skips its poll interval whenever a turn did work.
//
// The backlog exists because classification used to sit behind the key-points
// call and was skipped whenever that failed; it also absorbs any future
// best-effort classify failure. Errors are logged, never returned as job
// failures — there is no job here to fail.
func (w *Worker) classifyOne(ctx context.Context) (bool, error) {
	skip := make([]string, 0, len(w.classifyFailed))
	for id := range w.classifyFailed {
		skip = append(skip, id)
	}
	video, err := w.d.Videos.NextUnclassified(skip)
	if err != nil || video == nil {
		return false, err
	}

	// Same identity/token plumbing as a full analysis, so a backlog sweep is as
	// readable as a normal run — including the "still waiting" heartbeat.
	totals := &llm.Totals{}
	cctx := llm.WithCall(ctx, llm.CallInfo{
		VideoID: video.ID, Title: video.Title, Channel: video.ChannelName,
		Step: "classify-backlog", Totals: totals,
	})
	ident := []any{"video_id", video.ID, "title", video.Title, "channel", video.ChannelName}
	started := time.Now()

	raw, cerr := w.d.Summarizer.Classify(cctx, video.Title, video.Summary, videos.ClassifiableCategories())
	elapsed := time.Since(started).Milliseconds()
	if cerr != nil {
		// Park it for this process so the sweep advances to the next video
		// rather than retrying this one on every turn.
		w.classifyFailed[video.ID] = true
		w.d.Logger.Warn("summarize worker: backlog classify failed", append(ident, "duration_ms", elapsed, "err", cerr)...)
		return true, nil
	}
	category := videos.NormalizeCategory(raw)
	// Guarded, because the classify call above is slow enough for the user to
	// have picked a category on the Player in the meantime. A no-op write is
	// not a failure and needs no parking: the video no longer matches
	// NextUnclassified, so the sweep will not offer it again.
	applied, serr := w.d.Videos.SetCategoryIfUnset(video.ID, category)
	if serr != nil {
		w.classifyFailed[video.ID] = true
		w.d.Logger.Error("summarize worker: backlog set category failed", append(ident, "duration_ms", elapsed, "err", serr)...)
		return true, nil
	}
	if !applied {
		w.d.Logger.Info("summarize worker: backlog video was categorized meanwhile; keeping it", "video_id", video.ID)
		return true, nil
	}
	if category == videos.UncategorizedCategory {
		// The call succeeded but the reply was unusable. Park it too: retrying
		// the same prompt every turn would just burn requests.
		w.classifyFailed[video.ID] = true
	}
	attrs := append(ident, "category", category, "duration_ms", elapsed)
	w.d.Logger.Info("summarize worker: classified backlog video", append(attrs, totals.Snapshot().LogAttrs()...)...)
	return true, nil
}

// emit calls OnPhase when set, so an SSE hub can push live summarize
// progress to the Player. It is a no-op when OnPhase is nil.
func (w *Worker) emit(videoID, status, phase string) {
	if w.d.OnPhase != nil {
		w.d.OnPhase(videoID, status, phase)
	}
}

// failJob records the failure on both the video and the job, and always
// returns a non-nil error so the caller (processOne) surfaces the failure
// to the Run loop, which logs it. Jobs.Fail's own return is often nil on
// the common path, so it must never be returned as-is.
func (w *Worker) failJob(job *summaryjobs.Job, video *videos.Video, run *analysisRun, msg string) error {
	videoID := video.ID
	if err := w.d.Videos.SetSummaryStatus(videoID, videos.SummaryError, msg); err != nil {
		w.d.Logger.Error("summarize worker: set error status", "video_id", videoID, "err", err)
	}
	w.emit(videoID, videos.SummaryError, "")
	run.finished("error")
	terminal, ferr := w.d.Jobs.Fail(job.ID, job.Attempts, msg)
	if ferr != nil {
		return fmt.Errorf("summarize job %d failed (%s); also fail-record error: %w", job.ID, msg, ferr)
	}
	// Record an Activity row only when the job is genuinely terminal (moved to
	// 'failed'). Most failJob calls requeue to 'pending' — a retry, not news; a
	// row on every one would flood the feed.
	if terminal {
		w.recordActivity(activity.Event{
			Kind: activity.KindSummary, Outcome: activity.OutcomeFail,
			SubjectID: video.ID, Subject: video.Title, Summary: "summary failed",
			Detail: msg,
		})
	}
	return fmt.Errorf("summarize job %d failed: %s", job.ID, msg)
}

// requeueJob records a retryable failure and requeues the job WITHOUT touching
// summary_status. It is used for the key-points step, which runs after the
// summary is already marked done: a failure there must retry only that step and
// must NOT regress a usable summary to "error". If retries run out the job is
// marked failed but the video keeps its summary and search.
func (w *Worker) requeueJob(job *summaryjobs.Job, video *videos.Video, run *analysisRun, msg string) error {
	// will_retry=false means Jobs.Fail is about to mark this failed for good
	// rather than requeue it — same vocabulary as the finished line.
	w.d.Logger.Warn("summarize worker: key-points step failed",
		append(run.ident(), "attempt", attemptLabel(job),
			"will_retry", job.Attempts < job.MaxAttempts,
			"step_duration_ms", run.stepElapsedMs(), "err", msg)...)
	run.finished("keypoints_failed")
	// No Activity row even when this exhausts retries: a key-points failure keeps
	// summary_status="done" and usable search, so it is not a summary failure the
	// user needs to see in the feed (that is what failJob records).
	if _, err := w.d.Jobs.Fail(job.ID, job.Attempts, msg); err != nil {
		return fmt.Errorf("summarize job %d key-points failed (%s); also fail-record error: %w", job.ID, msg, err)
	}
	return fmt.Errorf("summarize job %d key-points failed: %s", job.ID, msg)
}

// embedAndStore rebuilds the video's chunks from the finished analysis and
// replaces its index. The chunk recipe itself lives in rag.BuildVideoChunks, so
// this path and the re-embed backfill cannot drift into producing different
// indexes for the same video.
func (w *Worker) embedAndStore(ctx context.Context, videoID string, parsed subtitles.Parsed, summaryText string, chapters []Chapter) error {
	rows := rag.BuildVideoChunks(parsed, summaryText, toRagChapters(chapters))
	if len(rows) == 0 {
		return errors.New("no chunks")
	}
	texts := make([]string, len(rows))
	for i, r := range rows {
		texts[i] = r.Text
	}
	vecs, err := w.d.Embedder.EmbedBatched(ctx, texts, 0)
	if err != nil {
		return err
	}
	meta := rag.IndexMeta{Model: w.d.EmbedModel, Dim: w.d.EmbedDim, Rev: rag.ChunkRecipeRev}
	return w.d.Rag.ReplaceVideoChunks(ctx, videoID, meta, rows, vecs)
}

// toRagChapters narrows summarize.Chapter to the two fields the chunk builder
// uses. rag cannot import summarize (summarize imports rag), so the types are
// deliberately separate.
func toRagChapters(chapters []Chapter) []rag.Chapter {
	if len(chapters) == 0 {
		return nil
	}
	out := make([]rag.Chapter, 0, len(chapters))
	for _, c := range chapters {
		out = append(out, rag.Chapter{TS: c.TS, Title: c.Title})
	}
	return out
}
