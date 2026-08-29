package summarize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/llm"
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
	transcript, terr := w.d.Videos.GetTranscript(video.ID)
	if terr != nil {
		return true, w.failJob(job, video, run, "load transcript: "+terr.Error())
	}
	if transcript == nil {
		w.finishNoTranscript(job, video, "no transcript")
		return true, nil
	}

	// Announce "running" only for a fresh job; a resumed one (retrying just the
	// key-points step) already has its summary marked done and must not regress.
	if video.SummaryStatus != videos.SummaryDone {
		_ = w.d.Videos.SetSummaryStatus(video.ID, videos.SummaryRunning, "")
		w.emit(video.ID, videos.SummaryRunning, PhaseSummarizing)
	}

	parsed, perr := subtitles.ParseVTT(strings.NewReader(transcript.VTT))
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

	// Sponsor reads and the other off-topic segments are taken out of what the
	// summarizer reads, so no chapter, key point or summary sentence can be drawn
	// from one. See sponsor.go for why this is done to the INPUT rather than only
	// to the results, and why "intro" is not among them.
	//
	// Deliberately applied to a copy: `parsed` stays whole for embedAndStore, so
	// this narrows what the Player captions without narrowing what search finds.
	sponsorSpans := suppressedSpans(video.SponsorblockSegments)
	forSummary := stripCues(parsed, sponsorSpans)
	if n := len(parsed.Cues) - len(forSummary.Cues); n > 0 {
		w.d.Logger.Debug("summarize worker: sponsor segments withheld from summarizer",
			"video_id", video.ID, "cues_dropped", n, "spans", len(sponsorSpans))
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
		s, serr := w.d.Summarizer.SummarizeText(sctx, forSummary.Transcript)
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

	// An inbox video stops here, with the prose and — on a channel that asked
	// for it — a search index.
	//
	// StatusNew is "recorded, nothing requested yet": peeq read this video's
	// captions to help decide whether to download it, and the card is still
	// sitting in the Inbox awaiting that decision. Category and key points are
	// investments in a video the library keeps — something to filter by,
	// something to navigate — and this one may well be ignored, in which case
	// both were thrown away.
	//
	// The index is the exception, and only where the channel's keep_reads says
	// so (migration 0026). There the reading is worth keeping on its own: the
	// chunks survive the ignore (see dropInboxRead), so a video peeq read and
	// the user never downloaded is still findable in Search. It reaches no list
	// while doing so — status 'new' is excluded from every one of them.
	//
	// Status first, then embed. An embedder outage must fail the JOB, which is
	// retryable, rather than regress the summary the Inbox card is already
	// rendering into an error the user has to look at.
	//
	// Nothing special is needed to resume. When the video is downloaded, the
	// download worker enqueues a fresh summary job, and the skip-what-exists
	// checks around this block mean that job spends nothing on the summary and
	// runs exactly the steps deferred here — re-embedding over the top of any
	// caption-built index with the downloaded transcript and its chapters. That
	// is the whole reason deciding to download never pays for the expensive
	// call twice.
	if isInboxRead(video, transcript.Source) {
		if err := w.d.Videos.SetSummaryStatus(video.ID, videos.SummaryDone, ""); err != nil {
			return true, w.failJob(job, video, run, err.Error())
		}
		outcome := "done_inbox"
		if video.ChannelKeepReads {
			// No chapters: an inbox read never runs the key-points step that
			// produces them, so this index is transcript and summary only.
			w.emit(video.ID, videos.SummaryDone, PhaseEmbedding)
			ectx, edone := run.step("embedding")
			if err := w.embedAndStore(ectx, video.ID, parsed, summary, nil); err != nil {
				return true, w.failJob(job, video, run, err.Error())
			}
			edone()
			outcome = "done_inbox_indexed"
		}
		w.emit(video.ID, videos.SummaryDone, "")
		run.finished(outcome)
		_ = w.d.Jobs.Finish(job.ID, summaryjobs.StateDone, "")
		return true, nil
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
	chapters, keyPoints, err := w.d.Summarizer.KeyPoints(kctx, summary, forSummary.Cues, ytChapters)
	if err != nil {
		// Moving embedding after this step means a video whose key-points call
		// keeps failing would never be indexed at all — unfindable, with no
		// sign of why. So a video that has never been embedded gets a
		// best-effort pass here with whatever chapters exist (yt-dlp's, or
		// none). SetKeyPoints zeroes embed_rev, so if key points does eventually
		// succeed the video is re-indexed properly with its chapters.
		//
		// A stale rev counts as "never indexed" for this purpose. embed_model is
		// set once and never cleared, so on its own it would let Reprocess —
		// which wipes the summary and calls ClearEmbedRev, but does not delete
		// chunks — leave the OLD summary chunk indexed and served by search for
		// as long as key points keeps failing, with nothing left to repair it.
		// Reading video.EmbedRev is safe here: the only writer that raises it
		// mid-attempt is the embed below, and a second attempt that sees the
		// raised value has genuinely already been indexed from this summary.
		if !video.Indexed() {
			if eerr := w.embedAndStore(kctx, video.ID, parsed, summary, ytChapters); eerr != nil {
				w.d.Logger.Warn("summarize worker: fallback embedding failed",
					append(run.ident(), "err", eerr)...)
			} else {
				w.d.Logger.Info("summarize worker: indexed without chapters after key-points failure",
					run.ident()...)
			}
		}
		return true, w.requeueJob(job, video, run, "keypoints", err.Error())
	}
	// Backstop over the input filter above. The model was handed a cue index with
	// the suppressed passages missing, but a timestamp it infers rather than
	// reads can still land in one of those holes.
	//
	// This also covers yt-dlp's chapters, which KeyPoints passes through
	// unchanged when YouTube supplied them: a creator who titles their own
	// chapter "Sponsor" is naming the same thing, and the reader is complaining
	// about what the Player shows, not about which component wrote it.
	if dc, dk := dropCovered(sponsorSpans, chapters, keyPoints); len(dc) != len(chapters) || len(dk) != len(keyPoints) {
		w.d.Logger.Debug("summarize worker: dropped artifacts inside sponsor segments",
			"video_id", video.ID,
			"chapters_dropped", len(chapters)-len(dc), "key_points_dropped", len(keyPoints)-len(dk))
		chapters, keyPoints = dc, dk
	}
	if err := w.d.Videos.SetKeyPoints(video.ID, encodeChapters(chapters), encodeKeyPoints(keyPoints)); err != nil {
		return true, w.requeueJob(job, video, run, "keypoints", err.Error())
	}
	done("chapters", len(chapters), "key_points", len(keyPoints))

	// Step 4 — embeddings, last so the index is built from the finished
	// analysis.
	//
	// Unconditional, and it has to be: the only route here is a successful
	// SetKeyPoints above, which zeroes embed_rev in the very statement that
	// writes the chapters — so whatever index exists at this point predates
	// them by construction. Gating on `video.EmbedRev < rag.ChunkRecipeRev`
	// would be worse than redundant, because `video` was read at claim time and
	// is now stale HIGH: a retry that arrives here already at the current rev
	// (exactly what the key-points fallback embed above leaves behind) would
	// skip embedding and strand the video on its chapterless index, with
	// embed_rev=0 in the database and no queue able to repair it.
	//
	// `chapters` here is the value KeyPoints just returned, not video.Chapters —
	// that field is stale for the same reason.
	// Status "done", not "running": summary_status was persisted as done a few
	// steps up, so a "running" event here would report a state the row does not
	// have — and the Player sets its local status from any non-done event, which
	// would replace the summary it just rendered with the "Summarizing" spinner
	// until this step finished. Same shape as the keypoints emit above: a
	// terminal status carrying a live phase, so the Queue meter still advances
	// to step 4/4 (it reads phase, falling back to status).
	w.emit(video.ID, videos.SummaryDone, PhaseEmbedding)
	ectx, edone := run.step("embedding")
	if err := w.embedAndStore(ectx, video.ID, parsed, summary, chapters); err != nil {
		// requeueJob, not failJob: the summary is written, marked done and already
		// rendering in the Player. Failing the job here used to set
		// summary_status='error', so the Player said "Summarization failed" above
		// the finished summary text — an endpoint outage made that the common case.
		// What actually failed is the index, and the video reports that through
		// `indexed` on its DTO instead.
		return true, w.requeueJob(job, video, run, "embedding", err.Error())
	}
	edone("chapters", len(chapters))
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

// isInboxRead reports whether this video's transcript was fetched to help
// decide whether to download it, rather than obtained by downloading it.
//
// Both halves are load-bearing, and neither is sufficient alone.
//
// The status is not, because 'new' is the videos.status COLUMN DEFAULT: any
// row written by an Upsert whose caller has not yet reached its SetStatus is
// momentarily 'new', and so is every row in a test that never sets one.
// Truncating the pipeline on that alone would silently cost real downloads
// their category, embeddings and key points, and the symptom — analysis that
// is complete but shallow — is close to invisible.
//
// The path is not, because it is only meaningful in combination: it says where
// the .vtt came from, and captionfetch is the sole writer under
// video's stored transcript. Before migration 0023 that was inferred from a
// ".summaries/" prefix on subtitle_path; the text no longer has a path, so the
// provenance is recorded on the row instead. Once the video is downloaded, the
// download worker stores its transcript with source='download' — overwriting,
// not skipping — so the same row stops matching here and its next summary job
// runs the full pipeline, which is exactly the handover this feature promises.
func isInboxRead(v *videos.Video, source string) bool {
	return v.Status == videos.StatusNew && source == videos.TranscriptSourceCaption
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
// unreadable: a video whose transcript cannot be read on this run must keep the
// summary it already has rather than have it wiped by a run that learned
// nothing. A tombstone no longer takes that path — it keeps the stored
// transcript row, precisely so the archived analysis stays rebuildable — but a
// row tombstoned before that changed has no transcript at all and relies on
// this.
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
// summary_status. It is used for the steps that run AFTER the summary is marked
// done — key points and embedding — where a failure must retry only that step
// and must NOT regress a usable summary to "error". If retries run out the job
// is marked failed but the video keeps the summary it has.
//
// step names which one failed, in both the log and the Activity row. Passing it
// in rather than hardcoding one is what lets embedding share this path: before,
// embedding called failJob and so reported "Summarization failed" on a video
// whose summary was finished and on screen.
func (w *Worker) requeueJob(job *summaryjobs.Job, video *videos.Video, run *analysisRun, step, msg string) error {
	// will_retry=false means Jobs.Fail is about to mark this failed for good
	// rather than requeue it — same vocabulary as the finished line.
	w.d.Logger.Warn("summarize worker: "+step+" step failed",
		append(run.ident(), "attempt", attemptLabel(job),
			"will_retry", job.Attempts < job.MaxAttempts,
			"step_duration_ms", run.stepElapsedMs(), "err", msg)...)
	run.finished(step + "_failed")
	terminal, ferr := w.d.Jobs.Fail(job.ID, job.Attempts, msg)
	if ferr != nil {
		return fmt.Errorf("summarize job %d %s failed (%s); also fail-record error: %w", job.ID, step, msg, ferr)
	}
	// One row, only once retries are genuinely exhausted — a row per retry would
	// flood the feed, which is why the terminal flag exists.
	//
	// OutcomeWarn, not OutcomeFail: the summary is finished and readable, so this
	// is not the "summary failed" event failJob records. But it does need to be
	// SOMEWHERE. A job that dies here leaves summary_status="done", drops off the
	// active queue, and is skipped by the boot sweep — so without this the video
	// reads as complete forever while its chapters, highlights or search index are
	// permanently missing, with no trace anywhere but the log.
	if terminal {
		w.recordActivity(activity.Event{
			Kind: activity.KindSummary, Outcome: activity.OutcomeWarn,
			SubjectID: video.ID, Subject: video.Title,
			Summary: step + " failed", Detail: msg,
		})
	}
	return fmt.Errorf("summarize job %d %s failed: %s", job.ID, step, msg)
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
