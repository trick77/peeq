package summarize

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/trick77/peeq/internal/activity"
	"github.com/trick77/peeq/internal/rag"
	"github.com/trick77/peeq/internal/sched"
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
	Activity activity.Recorder
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
		if err != nil && ctx.Err() == nil {
			// A shutdown mid-analysis surfaces as an error too; it is logged at
			// Debug where it happened, not as a failure here.
			w.d.Logger.Error("summarize worker: process", "err", err)
		}
		// Idle: poll again shortly. Busy: pause VideoDelay so the LLM endpoint
		// gets room to breathe between videos.
		gap := w.d.PollInterval
		if did {
			gap = w.d.VideoDelay
		}
		if !sched.Sleep(ctx, gap) {
			return
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
	if err != nil {
		// A read error is not a missing video. Marking the job failed here
		// used to make one SQLITE_BUSY permanent: EnqueueMissing skips any
		// video that has a job row, so nothing would ever queue it again. It
		// takes the same path as any other failed step — the retry ladder, and
		// on the last attempt the error status and Activity row a reader can
		// act on. The video's identity is all failJob needs from the row.
		return true, w.failJob(ctx, job, &videos.Video{ID: job.VideoID}, run, "load video: "+err.Error())
	}
	if video == nil {
		// Defensive: summary_jobs cascades on the video's delete, so a job
		// without its row cannot happen through the store. Finish it rather
		// than retry a read that can never succeed.
		w.d.Logger.Warn("summarize worker: video missing", "job_id", job.ID, "video_id", job.VideoID)
		if ferr := w.d.Jobs.Finish(job.ID, summaryjobs.StateFailed, "video missing"); ferr != nil {
			w.d.Logger.Error("summarize worker: finish missing video", "job_id", job.ID, "err", ferr)
		}
		return true, nil
	}

	// No subtitles => clean terminal no_transcript state, not an error. Checked
	// before the analysis is announced: a video that is never analyzed must not
	// log a start line, or an import of subtitle-less videos fills the log with
	// analyses that begin and never end.
	transcript, terr := w.d.Videos.GetTranscript(video.ID)
	if terr != nil {
		return true, w.failJob(ctx, job, video, run, "load transcript: "+terr.Error())
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
		return true, w.failJob(ctx, job, video, run, "parse vtt: "+perr.Error())
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
	forSummary, spansUnusable := stripCues(parsed, sponsorSpans)
	if spansUnusable {
		// The spans covered every cue. The transcript falls back to the
		// unfiltered parse (see stripCues), and the spans have to be dropped
		// here too: leaving them in place would let the same bad data strip
		// every chapter and key point in the backstop below, which is the
		// failure the fallback exists to prevent.
		w.d.Logger.Warn("summarize worker: sponsor segments cover the whole transcript, filter skipped",
			"video_id", video.ID, "spans", len(sponsorSpans))
		sponsorSpans = nil
	}
	if n := len(parsed.Cues) - len(forSummary.Cues); n > 0 {
		w.d.Logger.Debug("summarize worker: sponsor segments withheld from summarizer",
			"video_id", video.ID, "cues_dropped", n, "spans", len(sponsorSpans))
	}

	// There is real work to do: everything from here on is logged against this
	// video — one identity, one token accumulator, one wall clock.
	run = w.startRun(ctx, job, video)

	// The pipeline is resumable: each artifact is saved the moment it is
	// produced, and a retry skips whatever a prior attempt already stored. So a
	// failure in the fragile key-points step (step 4) never discards the summary
	// or embeddings, and only that step re-runs.

	// Step 1 — prose summary. Persist on its own; skip if already saved.
	summary := video.Summary
	if summary == "" {
		sctx, done := run.step("summary")
		s, serr := w.d.Summarizer.SummarizeVideo(sctx, video.Title, int(video.DurationSeconds), forSummary)
		if serr != nil {
			return true, w.failJob(ctx, job, video, run, serr.Error())
		}
		if err := w.d.Videos.SetSummaryText(video.ID, s); err != nil {
			return true, w.failJob(ctx, job, video, run, err.Error())
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
			return true, w.failJob(ctx, job, video, run, err.Error())
		}
		// The in-depth text is the one investment an inbox read does make:
		// the Inbox page offers it to settle a maybe the short summary left
		// open. After the status, so a failure requeues a video whose summary
		// is already showing.
		if err := w.inDepthStep(ctx, video, run, forSummary.Cues); err != nil {
			return true, w.requeueJob(ctx, job, video, run, "indepth", err.Error())
		}
		outcome := "done_inbox"
		if video.ChannelKeepReads {
			// No chapters: an inbox read never runs the key-points step that
			// produces them, so this index is transcript and summary only.
			w.emit(video.ID, videos.SummaryDone, PhaseEmbedding)
			ectx, edone := run.step("embedding")
			if err := w.embedAndStore(ectx, video.ID, parsed, summary, nil); err != nil {
				return true, w.failJob(ctx, job, video, run, err.Error())
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

	// Step 3 — the in-depth summary. After "done", so a failure here requeues
	// without the Player falling back to a spinner over a summary it already
	// shows; before key points, which has no skip check and would otherwise
	// re-run on every retry of this step. Its own emit carries "done" too, so
	// the open Player still refetches the summary the moment it is saved.
	if err := w.inDepthStep(ctx, video, run, forSummary.Cues); err != nil {
		return true, w.requeueJob(ctx, job, video, run, "indepth", err.Error())
	}
	w.emit(video.ID, videos.SummaryDone, PhaseKeypoints)

	// Step 4 — key points (and chapters when yt-dlp didn't supply them). The
	// fragile call. It now runs BEFORE embedding rather than last, because the
	// chapters it writes are what chapter chunks are built from; embedding first
	// would index every video as though it had no chapters.
	ytChapters := decodeChapters(video.Chapters)
	kctx, done := run.step("keypoints")
	chapters, keyPoints, err := w.d.Summarizer.KeyPoints(kctx, summary, forSummary.Cues, ytChapters)
	if errors.Is(err, ErrKeyPointsUnparsable) {
		// The model answered with something other than JSON. A video with no
		// key points is better than a failed job, but the drop must be visible
		// and attributable — every chapter and key point is gone at once, and
		// without this line it looks like a video the model found nothing in.
		w.d.Logger.Warn("summarize worker: key points reply was not JSON; storing none",
			append(run.ident(), "err", err)...)
		chapters, keyPoints, err = nil, nil, nil
	}
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
		// which wipes the summary and zeroes embed_rev, but does not delete
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
		return true, w.requeueJob(ctx, job, video, run, "keypoints", err.Error())
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
		return true, w.requeueJob(ctx, job, video, run, "keypoints", err.Error())
	}
	done("chapters", len(chapters), "key_points", len(keyPoints))

	// Step 5 — embeddings, last so the index is built from the finished
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
		return true, w.requeueJob(ctx, job, video, run, "embedding", err.Error())
	}
	edone("chapters", len(chapters))
	w.emit(video.ID, videos.SummaryDone, "")

	run.finished("done")
	_ = w.d.Jobs.Finish(job.ID, summaryjobs.StateDone, "")
	activity.Record(w.d.Activity, activity.Event{
		Kind: activity.KindSummary, Outcome: activity.OutcomeOK,
		SubjectID: video.ID, Subject: video.Title, Summary: "summarized",
		Detail: fmt.Sprintf("%d key points", len(keyPoints)),
	})
	return true, nil
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

// emit calls OnPhase when set, so an SSE hub can push live summarize
// progress to the Player. It is a no-op when OnPhase is nil.
func (w *Worker) emit(videoID, status, phase string) {
	if w.d.OnPhase != nil {
		w.d.OnPhase(videoID, status, phase)
	}
}

// embedAndStore rebuilds the video's chunks from the finished analysis and
// replaces its index. The chunk recipe itself lives in rag.BuildVideoChunks.
func (w *Worker) embedAndStore(ctx context.Context, videoID string, parsed subtitles.Parsed, summaryText string, chapters []Chapter) error {
	rows := rag.BuildVideoChunks(parsed, summaryText, toRagChapters(chapters))
	if len(rows) == 0 {
		return errors.New("no chunks")
	}
	// Embed only what is not already stored. A re-index usually changes the
	// chapter chunks and leaves every transcript window as it was — an inbox
	// read that is later downloaded, a retry after a failed key-points step, a
	// reprocess — and embedding those again buys the same vectors at full price
	// and leaves the old ones behind as dead storage.
	reuse, err := w.d.Rag.ReusableTexts(ctx, videoID, w.d.EmbedModel)
	if err != nil {
		return err
	}
	vecs := make([][]float32, len(rows))
	var texts []string
	var fresh []int
	for i, r := range rows {
		if reuse[r.Text] > 0 {
			reuse[r.Text]--
			continue
		}
		texts = append(texts, r.Text)
		fresh = append(fresh, i)
	}
	if len(texts) > 0 {
		embedded, err := w.d.Embedder.EmbedBatched(ctx, texts, 0)
		if err != nil {
			return err
		}
		if len(embedded) != len(texts) {
			return fmt.Errorf("embedder returned %d vectors for %d texts", len(embedded), len(texts))
		}
		for j, i := range fresh {
			vecs[i] = embedded[j]
		}
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

// inDepthStep writes the in-depth summary unless one is already stored, which
// is what makes a retry, or the job a download queues after an inbox read,
// skip it. An error is the caller's to requeue on; the summary is untouched.
func (w *Worker) inDepthStep(ctx context.Context, video *videos.Video, run *analysisRun, cues []subtitles.Cue) error {
	have, err := w.d.Videos.InDepth(video.ID)
	if err != nil {
		return err
	}
	if have != "" {
		run.skipped("indepth", "already stored")
		return nil
	}
	w.emit(video.ID, videos.SummaryDone, PhaseInDepth)
	ictx, done := run.step("indepth")
	text, err := w.d.Summarizer.InDepth(ictx, video.Title, int(video.DurationSeconds), cues)
	if err != nil {
		return err
	}
	if err := w.d.Videos.SetInDepth(video.ID, text); err != nil {
		return err
	}
	done("words", len(strings.Fields(text)))
	return nil
}
